package gcp

import (
	"testing"

	"google.golang.org/api/googleapi"
	logging "google.golang.org/api/logging/v2"
	runapi "google.golang.org/api/run/v2"
)

func TestExecutionName(t *testing.T) {
	op := &runapi.GoogleLongrunningOperation{
		Name:     "projects/p/locations/us-west1/operations/op-1",
		Metadata: googleapi.RawMessage(`{"@type":"type.googleapis.com/google.cloud.run.v2.Execution","name":"projects/p/locations/us-west1/jobs/dbtest-probe/executions/dbtest-probe-x7k2q"}`),
	}
	got, err := executionName(op)
	if want := "projects/p/locations/us-west1/jobs/dbtest-probe/executions/dbtest-probe-x7k2q"; err != nil || got != want {
		t.Errorf("executionName() = %q, %v; want %q", got, err, want)
	}
	if _, err := executionName(&runapi.GoogleLongrunningOperation{Name: "op-2"}); err == nil {
		t.Error("executionName() found an execution in empty metadata")
	}
}

func TestEntryText(t *testing.T) {
	result := `{"samples":10,"down_ms":1500}`
	if got := string(entryText(&logging.LogEntry{JsonPayload: googleapi.RawMessage(result)})); got != result {
		t.Errorf("JSON entry: got %q, want %q", got, result)
	}
	if got := string(entryText(&logging.LogEntry{TextPayload: "starting probe"})); got != "starting probe" {
		t.Errorf("text entry: got %q", got)
	}
}

func TestExecutionFailed(t *testing.T) {
	completed := []*runapi.GoogleCloudRunV2Condition{{Type: "Completed", Message: "Task dbtest-probe-x7k2q-task0 failed"}}
	for _, tc := range []struct {
		name      string
		execution runapi.GoogleCloudRunV2Execution
		selfExits bool
		failed    bool
	}{
		{"cancelled probe", runapi.GoogleCloudRunV2Execution{CancelledCount: 1}, false, false},
		{"failed probe", runapi.GoogleCloudRunV2Execution{FailedCount: 1, Conditions: completed}, false, true},
		{"succeeded bench", runapi.GoogleCloudRunV2Execution{SucceededCount: 1}, true, false},
		{"cancelled bench", runapi.GoogleCloudRunV2Execution{CancelledCount: 1}, true, true},
		{"failed bench", runapi.GoogleCloudRunV2Execution{FailedCount: 1, Conditions: completed}, true, true},
	} {
		if got := executionFailed(&tc.execution, tc.selfExits); (got != "") != tc.failed {
			t.Errorf("%s: executionFailed() = %q, want failed=%v", tc.name, got, tc.failed)
		}
	}
}
