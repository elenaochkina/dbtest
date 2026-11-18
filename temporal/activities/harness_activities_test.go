package activities

import (
	"slices"
	"testing"
	"time"

	"github.com/elenaochkina/dbtest/experiment/search"
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

func TestTesterSpec(t *testing.T) {
	spec := benchSpec(BenchContainerInput{
		Image:      "dbtest/tester:dev",
		DSN:        "postgres://db",
		Warehouses: 32,
		Duration:   3 * time.Minute,
		RampUp:     time.Minute,
		Search: &search.Config{
			MinClients: 8, MaxClients: 128, Resolution: 4, MaxTrials: 12, Tolerance: 0.03, Threshold: 0.95,
		},
	})
	want := []string{"-dsn", "postgres://db", "-warehouses", "32", "-min-clients", "8", "-max-clients", "128",
		"-resolution", "4", "-max-trials", "12", "-tolerance", "0.03", "-threshold", "0.95",
		"-rampup", "1m0s", "-duration", "3m0s"}
	if !slices.Equal(spec.Args, want) || !spec.SelfExits || spec.Image != "dbtest/tester:dev" {
		t.Errorf("spec = %+v", spec)
	}
}

func TestParseTesterResult(t *testing.T) {
	out := []byte(`{"workload":"tpcc","phase":"trial","clients":8,"metrics":{"nopm":1000},"elapsed_s":240}
{"workload":"tpcc","phase":"search","metrics":{"peak_clients":8},"trials":[{"clients":8,"metrics":{"nopm":1000},"elapsed_s":240},{"clients":8,"repeat":true,"metrics":{"nopm":990},"elapsed_s":240}],"elapsed_s":480,"aborted":false}
`)
	res, err := parseBench(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != "search" || len(res.Trials) != 2 || !res.Trials[1].Repeat || res.Trials[0].Metrics["nopm"] != 1000 {
		t.Errorf("result = %+v", res)
	}
}
