package hammerdb

import (
	"context"
	"errors"
	"math"
	"testing"
)

var defaultSearch = SearchConfig{
	MinClients: 8, MaxClients: 128, Resolution: 4, MaxProbes: 12, Tolerance: 0.03, Threshold: 0.95,
}

// hill rises linearly to peak NOPM at top clients, stays flat to flatEnd, then
// falls by fall NOPM per client.
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

// fixed returns a probe that reads NOPM from curve and records each call.
func fixed(curve func(int) float64, calls *[]int) func(context.Context, int) (Result, error) {
	return func(_ context.Context, clients int) (Result, error) {
		*calls = append(*calls, clients)
		return Result{NOPM: curve(clients)}, nil
	}
}

func TestSearchFindsPeakSaturationAndDecline(t *testing.T) {
	var calls []int
	// Rises to 30,000 at 24 clients, flat to 40, then loses 500 per client:
	// 95% of the peak is first reached at 23 and lost above 43.
	res, err := Search(context.Background(), defaultSearch, fixed(hill(24, 40, 30000, 500), &calls))
	if err != nil {
		t.Fatal(err)
	}
	if res.PeakNOPM != 30000 || res.PeakClients < 24 || res.PeakClients > 24+defaultSearch.Resolution {
		t.Errorf("peak = %d clients, %.0f NOPM; want 24..28 clients, 30000", res.PeakClients, res.PeakNOPM)
	}
	if res.SaturationClients < 23 || res.SaturationClients > 23+defaultSearch.Resolution {
		t.Errorf("saturation = %d; want 23..27", res.SaturationClients)
	}
	if res.DeclineClients < 44 || res.DeclineClients > 44+defaultSearch.Resolution {
		t.Errorf("decline = %d; want 44..48", res.DeclineClients)
	}
	if len(res.Probes) > defaultSearch.MaxProbes {
		t.Errorf("%d probes; want at most %d", len(res.Probes), defaultSearch.MaxProbes)
	}
	if last := calls[len(calls)-1]; last != res.PeakClients || res.Drift != 0 {
		t.Errorf("last call %d, drift %v; want the peak run again with no drift", last, res.Drift)
	}
	seen := map[int]bool{}
	for _, p := range res.Probes {
		if seen[p.Clients] {
			t.Errorf("clients %d probed twice", p.Clients)
		}
		seen[p.Clients] = true
	}
}

func TestSearchWithoutDeclineReportsZero(t *testing.T) {
	var calls []int
	res, err := Search(context.Background(), defaultSearch, fixed(hill(40, 1000, 20000, 0), &calls))
	if err != nil {
		t.Fatal(err)
	}
	if res.DeclineClients != 0 {
		t.Errorf("decline = %d; want 0 when throughput never falls", res.DeclineClients)
	}
	if res.PeakClients > 40+defaultSearch.Resolution {
		t.Errorf("peak = %d; want the fewest clients at the plateau, near 40", res.PeakClients)
	}
}

func TestSearchStopsAtTheProbeBudget(t *testing.T) {
	var calls []int
	cfg := defaultSearch
	cfg.MaxProbes = 4
	res, err := Search(context.Background(), cfg, fixed(hill(24, 40, 30000, 500), &calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Probes) != 4 || len(calls) != 5 {
		t.Errorf("%d probes and %d calls; want 4 probes plus the drift run", len(res.Probes), len(calls))
	}
}

func TestSearchReturnsProbesSoFarOnError(t *testing.T) {
	failure := errors.New("hammerdb failed")
	probe := func(_ context.Context, clients int) (Result, error) {
		if clients == 32 {
			return Result{}, failure
		}
		return Result{NOPM: float64(clients)}, nil
	}
	res, err := Search(context.Background(), defaultSearch, probe)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v; want the probe's error", err)
	}
	if len(res.Probes) != 2 {
		t.Errorf("%d probes; want the 8 and 16 runs", len(res.Probes))
	}
}

func TestSearchConfigValidate(t *testing.T) {
	bad := defaultSearch
	bad.Threshold = 1
	if bad.Validate() == nil {
		t.Error("threshold 1 accepted")
	}
	bad = defaultSearch
	bad.MaxClients = 4
	if bad.Validate() == nil {
		t.Error("max clients below min accepted")
	}
}
