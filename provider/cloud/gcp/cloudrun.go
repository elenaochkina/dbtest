package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"time"

	"google.golang.org/api/googleapi"
	logging "google.golang.org/api/logging/v2"
	runapi "google.golang.org/api/run/v2"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/telemetry"
)

// cloudRunRunner runs containers as Cloud Run job executions, one execution per container.
type cloudRunRunner struct {
	run     *runapi.Service
	logs    *logging.Service
	project string
	region  string
	tel     *telemetry.Telemetry
}

// newCloudRunRunner builds a Cloud Run runner in the cloud's project and region.
func newCloudRunRunner(project, region string, tel *telemetry.Telemetry) (harness.Runner, error) {
	if project == "" {
		return nil, errors.New("cloud run runner needs GCP_PROJECT")
	}
	ctx := context.Background()
	run, err := runapi.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloud run client: %w", err)
	}
	logs, err := logging.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloud logging client: %w", err)
	}
	return &cloudRunRunner{run: run, logs: logs, project: project, region: region, tel: tel}, nil
}

// Run starts an execution of the job named spec.Image with spec.Args and returns once its task is running.
func (r *cloudRunRunner) Run(ctx context.Context, spec harness.Spec) (harness.Handle, error) {
	job := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", r.project, r.region, spec.Image)
	op, err := r.run.Projects.Locations.Jobs.Run(job, &runapi.GoogleCloudRunV2RunJobRequest{
		Overrides: &runapi.GoogleCloudRunV2Overrides{
			ContainerOverrides: []*runapi.GoogleCloudRunV2ContainerOverride{{Args: spec.Args}},
		},
	}).Context(ctx).Do()
	if err != nil {
		return harness.Handle{}, fmt.Errorf("run job %q: %w", spec.Image, err)
	}
	name, err := executionName(op)
	if err != nil {
		return harness.Handle{}, err
	}
	if err := r.waitRunning(ctx, name, spec.SelfExits); err != nil {
		return harness.Handle{}, err
	}
	if r.tel != nil {
		r.tel.Logger.Info("started harness execution",
			slog.String("execution", name),
			slog.String("job", spec.Image),
		)
	}
	return harness.Handle{ID: name, SelfExits: spec.SelfExits}, nil
}

// Stop cancels the execution unless it exits on its own, waits until it ends, and returns its output.
func (r *cloudRunRunner) Stop(ctx context.Context, h harness.Handle) ([]byte, error) {
	if !h.SelfExits {
		_, err := r.run.Projects.Locations.Jobs.Executions.Cancel(h.ID, &runapi.GoogleCloudRunV2CancelExecutionRequest{}).Context(ctx).Do()
		if err != nil && !hasCode(err, http.StatusBadRequest) && !hasCode(err, http.StatusNotFound) {
			return nil, fmt.Errorf("cancel execution %s: %w", h.ID, err)
		}
	}
	execution, err := r.waitDone(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	collectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	out, collectErr := r.collect(collectCtx, execution)
	if failed := executionFailed(execution, h.SelfExits); failed != "" {
		return out, fmt.Errorf("execution %s %s: %s", path.Base(h.ID), failed, harness.Tail(out))
	}
	return out, collectErr
}

// waitRunning polls until the execution's task is running. A self-exiting execution may succeed before it is seen running.
func (r *cloudRunRunner) waitRunning(ctx context.Context, name string, selfExits bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		execution, err := r.run.Projects.Locations.Jobs.Executions.Get(name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("get execution %s: %w", name, err)
		}
		if execution.RunningCount > 0 {
			return nil
		}
		if execution.CompletionTime != "" {
			if selfExits && execution.SucceededCount > 0 {
				return nil
			}
			return fmt.Errorf("execution %s ended before running: %s", name, conditionMessage(execution))
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("execution %s did not start running: %w", name, err)
		}
	}
}

// waitDone polls until the execution has a completion time.
func (r *cloudRunRunner) waitDone(ctx context.Context, name string) (*runapi.GoogleCloudRunV2Execution, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	for {
		execution, err := r.run.Projects.Locations.Jobs.Executions.Get(name).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("get execution %s: %w", name, err)
		}
		if execution.CompletionTime != "" {
			return execution, nil
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return nil, fmt.Errorf("execution %s did not end: %w", name, err)
		}
	}
}

// collect reads the execution's log entries and polls until their total length stops changing.
func (r *cloudRunRunner) collect(ctx context.Context, execution *runapi.GoogleCloudRunV2Execution) ([]byte, error) {
	filter := fmt.Sprintf(`resource.type="cloud_run_job" AND labels."run.googleapis.com/execution_name"=%q AND timestamp>=%q`,
		path.Base(execution.Name), execution.CreateTime)
	var last []byte
	stable := 0
	for {
		out, err := r.readLogs(ctx, filter)
		if err != nil {
			return last, err
		}
		if len(out) > 0 && len(out) == len(last) {
			stable++
			if stable >= 2 {
				return out, nil
			}
		} else {
			stable = 0
		}
		last = out
		if err := pause(ctx, 5*time.Second); err != nil {
			if len(last) > 0 {
				return last, nil
			}
			return nil, fmt.Errorf("no output from execution %s: %w", path.Base(execution.Name), err)
		}
	}
}

// readLogs returns every entry matching filter in time order, one line per entry.
func (r *cloudRunRunner) readLogs(ctx context.Context, filter string) ([]byte, error) {
	var out []byte
	request := &logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + r.project},
		Filter:        filter,
		OrderBy:       "timestamp asc",
		PageSize:      1000,
	}
	err := r.logs.Entries.List(request).Pages(ctx, func(page *logging.ListLogEntriesResponse) error {
		for _, entry := range page.Entries {
			out = append(out, entryText(entry)...)
			out = append(out, '\n')
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}
	return out, nil
}

// entryText returns a log entry's JSON payload, or its text when it has none.
func entryText(entry *logging.LogEntry) []byte {
	if len(entry.JsonPayload) > 0 {
		return entry.JsonPayload
	}
	return []byte(entry.TextPayload)
}

// executionName returns the execution named in a RunJob operation's metadata.
func executionName(op *runapi.GoogleLongrunningOperation) (string, error) {
	var execution runapi.GoogleCloudRunV2Execution
	if err := json.Unmarshal(op.Metadata, &execution); err != nil || execution.Name == "" {
		return "", fmt.Errorf("run job operation %s names no execution", op.Name)
	}
	return execution.Name, nil
}

// executionFailed describes how an ended execution failed, or returns "" if it did not.
// A cancelled execution has failed only if it was expected to exit on its own.
func executionFailed(execution *runapi.GoogleCloudRunV2Execution, selfExits bool) string {
	switch {
	case execution.FailedCount > 0:
		return "failed: " + conditionMessage(execution)
	case selfExits && execution.SucceededCount == 0:
		return "did not succeed: " + conditionMessage(execution)
	}
	return ""
}

// conditionMessage returns the message of the execution's Completed condition.
func conditionMessage(execution *runapi.GoogleCloudRunV2Execution) string {
	for _, condition := range execution.Conditions {
		if condition.Type == "Completed" && condition.Message != "" {
			return condition.Message
		}
	}
	return "no reason given"
}

func hasCode(err error, code int) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// pause waits for d or until ctx ends.
func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

var _ harness.Runner = (*cloudRunRunner)(nil)
