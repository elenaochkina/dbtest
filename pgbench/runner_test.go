package pgbench

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestMeasuredTPSSkipsWarmUp(t *testing.T) {
	out := `pgbench (18.1)
progress: 5.0 s, 100.0 tps, lat 1.000 ms stddev 0.100, 0 failed
progress: 10.0 s, 900.0 tps, lat 1.000 ms stddev 0.100, 0 failed
progress: 15.0 s, 1000.0 tps, lat 1.000 ms stddev 0.100, 0 failed
progress: 20.0 s, 1200.0 tps, lat 1.000 ms stddev 0.100, 0 failed
tps = 800.0 (without initial connection time)
`
	tps, noise, err := measuredTPS(out, 10*time.Second)
	if err != nil || tps != 1100 || math.Abs(noise-100.0/1100) > 1e-9 {
		t.Errorf("tps %v, noise %v, err %v; want 1100 from the last two intervals, noise 100/1100", tps, noise, err)
	}
	if _, _, err := measuredTPS(out, time.Minute); err == nil {
		t.Error("no interval after a 1 minute warm-up accepted")
	}
}

func TestMeasuredLatenciesSkipsWarmUpAndFailures(t *testing.T) {
	thread0 := "0 1 2000 0 1700000000 2000\n0 7 3000 0 1700000010 503000\n"
	thread1 := "1 1 4000 0 1700000001 4000\n1 9 failed 0 1700000011 0\n1 8 1000 0 1700000012 1000\n"
	latencies, err := measuredLatencies([]string{thread0, thread1}, 10*time.Second)
	if err != nil || !slices.Equal(latencies, []float64{1, 3}) {
		t.Errorf("latencies %v, err %v; want [1 3]: the two started 10 s or more after the first", latencies, err)
	}
}

func TestPercentile(t *testing.T) {
	var values []float64
	for value := 1; value <= 100; value++ {
		values = append(values, float64(value))
	}
	if p50, p95, p99 := percentile(values, 0.50), percentile(values, 0.95), percentile(values, 0.99); p50 != 50 || p95 != 95 || p99 != 99 {
		t.Errorf("p50 %v, p95 %v, p99 %v; want 50, 95, 99", p50, p95, p99)
	}
}
