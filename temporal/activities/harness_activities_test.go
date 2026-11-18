package activities

import (
	"slices"
	"testing"
	"time"
)

func TestParseBench(t *testing.T) {
	out := []byte(`time=... level=INFO msg="pgbench started"
{"workload":"tpcc","phase":"run","metrics":{"nopm":214543},"elapsed_s":61.2,"aborted":false}
`)
	res, err := parseBench(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != "run" || res.Metrics["nopm"] != 214543 {
		t.Errorf("result = %+v", res)
	}
	if _, err := parseBench([]byte("no result here")); err == nil {
		t.Error("parseBench without a JSON line returned no error")
	}
}

func TestBenchSpec(t *testing.T) {
	spec := benchSpec(BenchContainerInput{
		DSN:      "postgres://db",
		Workload: "tpcc",
		Clients:  8,
		Duration: 10 * time.Minute,
		RampUp:   2 * time.Minute,
	})
	want := []string{"-dsn", "postgres://db", "-workload", "tpcc", "-clients", "8", "-duration", "10m0s", "-rampup", "2m0s"}
	if !slices.Equal(spec.Args, want) || !spec.SelfExits {
		t.Errorf("spec = %+v", spec)
	}

	spec = benchSpec(BenchContainerInput{DSN: "postgres://db", Workload: "tpcc"})
	if !slices.Contains(spec.Args, "-rampup") || spec.Args[len(spec.Args)-1] != "0s" {
		t.Errorf("zero ramp-up args = %v, want -rampup 0s", spec.Args)
	}
}
