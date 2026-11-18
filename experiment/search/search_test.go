package search

import (
	"context"
	"errors"
	"math"
	"testing"
)

var defaultConfig = Config{
	MinClients: 8, MaxClients: 128, Resolution: 4, MaxTrials: 12, Tolerance: 0.03, Threshold: 0.95,
}

// hill rises linearly to peak throughput at top clients, stays flat to
// flatEnd, then falls by fall per client.
func hill(top, flatEnd int, peak, fall float64) func(int) float64 {
	return func(clients int) float64 {
		switch {
		case clients <= top:
			return peak * float64(clients) / float64(top)
		case clients <= flatEnd:
			return peak
		default:
			return math.Max(0, peak-fall*float64(clients-flatEnd))
		}
	}
}

// fixed returns a trial that reads throughput from curve and records each call.
func fixed(curve func(int) float64, calls *[]int) func(context.Context, int) (float64, error) {
	return func(_ context.Context, clients int) (float64, error) {
		*calls = append(*calls, clients)
		return curve(clients), nil
	}
}

func TestRunFindsPeakSaturationAndDecline(t *testing.T) {
	var calls []int
	// Rises to 30,000 at 24 clients, flat to 40, then loses 500 per client:
	// 95% of the peak is first reached at 23 and lost above 43.
	res, err := Run(context.Background(), defaultConfig, fixed(hill(24, 40, 30000, 500), &calls))
	if err != nil {
		t.Fatal(err)
	}
	if res.PeakThroughput != 30000 || res.PeakClients < 24 || res.PeakClients > 24+defaultConfig.Resolution {
		t.Errorf("peak = %d clients, %.0f; want 24..28 clients, 30000", res.PeakClients, res.PeakThroughput)
	}
	if res.SaturationClients < 23 || res.SaturationClients > 23+defaultConfig.Resolution {
		t.Errorf("saturation = %d; want 23..27", res.SaturationClients)
	}
	if res.DeclineClients < 44 || res.DeclineClients > 44+defaultConfig.Resolution {
		t.Errorf("decline = %d; want 44..48", res.DeclineClients)
	}
	if len(res.Trials) > defaultConfig.MaxTrials {
		t.Errorf("%d trials; want at most %d", len(res.Trials), defaultConfig.MaxTrials)
	}
	if last := calls[len(calls)-1]; last != res.PeakClients || res.Drift != 0 {
		t.Errorf("last call %d, drift %v; want the peak run again with no drift", last, res.Drift)
	}
	seen := map[int]bool{}
	for _, p := range res.Trials {
		if seen[p.Clients] {
			t.Errorf("clients %d tried twice", p.Clients)
		}
		seen[p.Clients] = true
	}
}

func TestRunWithoutDeclineReportsZero(t *testing.T) {
	var calls []int
	res, err := Run(context.Background(), defaultConfig, fixed(hill(40, 1000, 20000, 0), &calls))
	if err != nil {
		t.Fatal(err)
	}
	if res.DeclineClients != 0 {
		t.Errorf("decline = %d; want 0 when throughput never falls", res.DeclineClients)
	}
	if res.PeakClients > 40+defaultConfig.Resolution {
		t.Errorf("peak = %d; want the fewest clients at the plateau, near 40", res.PeakClients)
	}
}

func TestRunStopsAtTheTrialBudget(t *testing.T) {
	var calls []int
	cfg := defaultConfig
	cfg.MaxTrials = 4
	res, err := Run(context.Background(), cfg, fixed(hill(24, 40, 30000, 500), &calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trials) != 4 || len(calls) != 5 {
		t.Errorf("%d trials and %d calls; want 4 trials plus the drift run", len(res.Trials), len(calls))
	}
}

func TestRunReturnsTrialsSoFarOnError(t *testing.T) {
	failure := errors.New("trial failed")
	trial := func(_ context.Context, clients int) (float64, error) {
		if clients == 32 {
			return 0, failure
		}
		return float64(clients), nil
	}
	res, err := Run(context.Background(), defaultConfig, trial)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v; want the trial's error", err)
	}
	if len(res.Trials) != 2 {
		t.Errorf("%d trials; want the 8 and 16 runs", len(res.Trials))
	}
}

func TestConfigValidate(t *testing.T) {
	bad := defaultConfig
	bad.Threshold = 1
	if bad.Validate() == nil {
		t.Error("threshold 1 accepted")
	}
	bad = defaultConfig
	bad.MaxClients = 4
	if bad.Validate() == nil {
		t.Error("max clients below min accepted")
	}
}
