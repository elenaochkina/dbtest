// Package aws runs harness containers as ECS Fargate tasks.
package aws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/telemetry"
)

// fargateRunner starts one task per container.
type fargateRunner struct {
	ecs  *ecs.Client
	logs *cloudwatchlogs.Client
	cfg  fargateConfig
	tel  *telemetry.Telemetry
}

type fargateConfig struct {
	region           string
	cluster          string
	subnetIDs        []string
	securityGroupIDs []string
	logGroup         string
}

func New(tel *telemetry.Telemetry) (*fargateRunner, error) {
	cfg := loadConfig()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if cfg.region != "" {
		awsCfg.Region = cfg.region
	}

	return &fargateRunner{
		ecs:  ecs.NewFromConfig(awsCfg),
		logs: cloudwatchlogs.NewFromConfig(awsCfg),
		cfg:  cfg,
		tel:  tel,
	}, nil
}

// Run starts a task and returns once it is RUNNING. RunTask reports a task as
// PROVISIONING, so returning earlier would hand back a container that is not
// yet accepting work.
func (r *fargateRunner) Run(ctx context.Context, spec harness.Spec) (harness.Handle, error) {
	container, err := r.containerName(ctx, spec.Image)
	if err != nil {
		return harness.Handle{}, err
	}

	out, err := r.ecs.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(r.cfg.cluster),
		TaskDefinition: aws.String(spec.Image),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        r.cfg.subnetIDs,
				SecurityGroups: r.cfg.securityGroupIDs,
				AssignPublicIp: ecstypes.AssignPublicIpEnabled,
			},
		},
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{{
				Name:    aws.String(container),
				Command: spec.Args,
			}},
		},
	})
	if err != nil {
		return harness.Handle{}, fmt.Errorf("run task %q: %w", spec.Image, err)
	}
	// A rejected task is reported in Failures rather than as an error.
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return harness.Handle{}, fmt.Errorf("run task %q rejected: %s %s",
			spec.Image, aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return harness.Handle{}, fmt.Errorf("run task %q returned no task", spec.Image)
	}
	arn := aws.ToString(out.Tasks[0].TaskArn)

	if err := r.waitRunning(ctx, arn); err != nil {
		return harness.Handle{}, err
	}
	if r.tel != nil {
		r.tel.Logger.Info("started harness task",
			slog.String("task_arn", arn),
			slog.String("task_definition", spec.Image),
			slog.String("container", container),
		)
	}
	return harness.Handle{ID: arn, SelfExits: spec.SelfExits}, nil
}

// Stop terminates the task unless it exits on its own, then returns its output.
func (r *fargateRunner) Stop(ctx context.Context, h harness.Handle) ([]byte, error) {
	if !h.SelfExits {
		if _, err := r.ecs.StopTask(ctx, &ecs.StopTaskInput{
			Cluster: aws.String(r.cfg.cluster),
			Task:    aws.String(h.ID),
			Reason:  aws.String("harness stop"),
		}); err != nil && !isMissing(err) {
			return nil, fmt.Errorf("stop task: %w", err)
		}
	}

	task, err := r.waitStopped(ctx, h.ID)
	if err != nil {
		return nil, err
	}

	// Both run on a fresh context, since the failure being handled here is
	// often ctx expiring.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()

	out, collectErr := r.collect(cctx, task)

	if code := exitCode(task); code != 0 {
		return out, fmt.Errorf("container exited %d: %s", code, tail(out))
	}
	if collectErr != nil {
		return out, collectErr
	}
	return out, nil
}

func init() {
	harness.Register(harness.Fargate, func(tel *telemetry.Telemetry) (harness.Runner, error) {
		return New(tel)
	})
}

func loadConfig() fargateConfig {
	return fargateConfig{
		region:           os.Getenv("AWS_REGION"),
		cluster:          os.Getenv("AWS_ECS_CLUSTER"),
		subnetIDs:        splitNonEmpty(os.Getenv("AWS_ECS_SUBNET_IDS"), ","),
		securityGroupIDs: splitNonEmpty(os.Getenv("AWS_ECS_SECURITY_GROUP_IDS"), ","),
		logGroup:         os.Getenv("AWS_ECS_LOG_GROUP"),
	}
}

// validate fails at construction, so a worker throws an error immediately.
func (c fargateConfig) validate() error {
	var missing []string
	if c.cluster == "" {
		missing = append(missing, "AWS_ECS_CLUSTER")
	}
	if len(c.subnetIDs) == 0 {
		missing = append(missing, "AWS_ECS_SUBNET_IDS")
	}
	if len(c.securityGroupIDs) == 0 {
		missing = append(missing, "AWS_ECS_SECURITY_GROUP_IDS")
	}
	if c.logGroup == "" {
		missing = append(missing, "AWS_ECS_LOG_GROUP")
	}
	if len(missing) > 0 {
		return fmt.Errorf("fargate runner needs %s", strings.Join(missing, ", "))
	}
	return nil
}

// describeContainer returns the task definition's container name and its log
// stream prefix. RunTask overrides address the container by name, and the log
// stream is named after both.
func (r *fargateRunner) describeContainer(ctx context.Context, taskDef string) (name, prefix string, err error) {
	out, err := r.ecs.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(taskDef),
	})
	if err != nil {
		return "", "", fmt.Errorf("describe task definition %q: %w", taskDef, err)
	}
	defs := out.TaskDefinition.ContainerDefinitions
	if len(defs) == 0 {
		return "", "", fmt.Errorf("task definition %q has no container", taskDef)
	}
	name = aws.ToString(defs[0].Name)
	if defs[0].LogConfiguration != nil {
		prefix = defs[0].LogConfiguration.Options["awslogs-stream-prefix"]
	}
	return name, prefix, nil
}

func (r *fargateRunner) containerName(ctx context.Context, taskDef string) (string, error) {
	name, _, err := r.describeContainer(ctx, taskDef)
	return name, err
}

// waitRunning polls until the task is RUNNING. A task that goes straight to
// STOPPED never ran, and its stoppedReason says why.
func (r *fargateRunner) waitRunning(ctx context.Context, arn string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for {
		task, err := r.describeTask(ctx, arn)
		if err != nil {
			return err
		}
		switch aws.ToString(task.LastStatus) {
		case "RUNNING":
			return nil
		case "STOPPED":
			return fmt.Errorf("task %s stopped before running: %s", arn, stopReason(task))
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("task %s did not reach RUNNING: %w", arn, err)
		}
	}
}

func (r *fargateRunner) waitStopped(ctx context.Context, arn string) (*ecstypes.Task, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()

	for {
		task, err := r.describeTask(ctx, arn)
		if err != nil {
			return nil, err
		}
		if aws.ToString(task.LastStatus) == "STOPPED" {
			return task, nil
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return nil, fmt.Errorf("task %s did not stop: %w", arn, err)
		}
	}
}

func (r *fargateRunner) describeTask(ctx context.Context, arn string) (*ecstypes.Task, error) {
	out, err := r.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(r.cfg.cluster),
		Tasks:   []string{arn},
	})
	if err != nil {
		return nil, fmt.Errorf("describe task %s: %w", arn, err)
	}
	if len(out.Tasks) == 0 {
		return nil, fmt.Errorf("task %s not found", arn)
	}
	return &out.Tasks[0], nil
}

// collect returns the task's output.
// Poll until the event count settles.
func (r *fargateRunner) collect(ctx context.Context, task *ecstypes.Task) ([]byte, error) {
	_, prefix, err := r.describeContainer(ctx, aws.ToString(task.TaskDefinitionArn))
	if err != nil {
		return nil, err
	}
	if len(task.Containers) == 0 {
		return nil, fmt.Errorf("task %s has no container", aws.ToString(task.TaskArn))
	}
	stream := strings.Join([]string{
		prefix,
		aws.ToString(task.Containers[0].Name),
		taskID(aws.ToString(task.TaskArn)),
	}, "/")

	var last []byte
	stable := 0
	for {
		out, err := r.readStream(ctx, stream)
		if err != nil {
			if !isMissing(err) {
				return nil, err
			}
			out = nil // the stream appears only once the first event lands
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

		if err := pause(ctx, 2*time.Second); err != nil {
			if len(last) > 0 {
				return last, nil
			}
			return nil, fmt.Errorf("no output from stream %q: %w", stream, err)
		}
	}
}

func (r *fargateRunner) readStream(ctx context.Context, stream string) ([]byte, error) {
	var (
		buf   strings.Builder
		token *string
	)
	for {
		out, err := r.logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  aws.String(r.cfg.logGroup),
			LogStreamName: aws.String(stream),
			StartFromHead: aws.Bool(true),
			NextToken:     token,
		})
		if err != nil {
			return nil, fmt.Errorf("get log events %q: %w", stream, err)
		}
		for _, e := range out.Events {
			buf.WriteString(aws.ToString(e.Message))
			buf.WriteByte('\n')
		}
		// A repeated token means the end of the stream.
		if len(out.Events) == 0 || (token != nil && aws.ToString(out.NextForwardToken) == aws.ToString(token)) {
			return []byte(buf.String()), nil
		}
		token = out.NextForwardToken
	}
}

// taskID is the last segment of a task ARN, which is what names the log stream.
func taskID(arn string) string {
	parts := strings.Split(arn, "/")
	return parts[len(parts)-1]
}

func exitCode(task *ecstypes.Task) int32 {
	if len(task.Containers) == 0 || task.Containers[0].ExitCode == nil {
		return 0
	}
	return *task.Containers[0].ExitCode
}

func stopReason(task *ecstypes.Task) string {
	reason := aws.ToString(task.StoppedReason)
	if len(task.Containers) > 0 {
		if c := aws.ToString(task.Containers[0].Reason); c != "" {
			reason += ": " + c
		}
	}
	return reason
}

// isMissing reports whether the resource is gone, which a second Stop finds.
func isMissing(err error) bool {
	var notFound *ecstypes.ResourceNotFoundException
	var invalid *ecstypes.InvalidParameterException
	return errors.As(err, &notFound) || errors.As(err, &invalid) ||
		strings.Contains(err.Error(), "ResourceNotFoundException")
}

func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func tail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, "\n")
}

func splitNonEmpty(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
