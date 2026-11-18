package hammerdb

import (
	"context"
	"errors"
	"slices"
)

// SearchConfig bounds a client search.
type SearchConfig struct {
	MinClients int
	MaxClients int
	// Resolution is the client gap at which a bisection stops.
	Resolution int
	// MaxProbes caps the timed runs, not counting the final drift run.
	MaxProbes int
	// Tolerance is the NOPM fraction within which two runs count as equal.
	Tolerance float64
	// Threshold is the fraction of peak NOPM that marks saturation and decline.
	Threshold float64
}

// Probe is one timed run at a client count.
type Probe struct {
	Clients int
	Result  Result
}

// SearchResult holds every probe and the client counts found from them.
type SearchResult struct {
	Probes []Probe
	// PeakClients is the fewest clients within Tolerance of the highest NOPM.
	PeakClients int
	PeakNOPM    float64
	// SaturationClients is the fewest clients reaching Threshold of PeakNOPM.
	SaturationClients int
	// DeclineClients is the fewest clients above the peak below Threshold of
	// PeakNOPM, or 0 when no probe up to MaxClients fell that far.
	DeclineClients int
	// Drift is the NOPM change when PeakClients runs again at the end.
	Drift float64
}

// Validate reports a config Search cannot use.
func (cfg SearchConfig) Validate() error {
	switch {
	case cfg.MinClients < 1 || cfg.MaxClients < cfg.MinClients:
		return errors.New("search needs 1 <= min clients <= max clients")
	case cfg.Resolution < 1:
		return errors.New("search needs a resolution of at least 1 client")
	case cfg.MaxProbes < 3:
		return errors.New("search needs at least 3 probes")
	case cfg.Tolerance < 0 || cfg.Tolerance >= 0.5:
		return errors.New("search needs a tolerance from 0 to 0.5")
	case cfg.Threshold <= 0 || cfg.Threshold >= 1:
		return errors.New("search needs a threshold between 0 and 1")
	}
	return nil
}

// Search runs the timed driver at chosen client counts to find the peak,
// saturation and decline points. It doubles clients from MinClients until NOPM
// falls below Threshold of the best run, bisects around the best run, then
// bisects the rising side for saturation and the falling side for decline.
// It runs no client count twice except the peak, once more at the end, to
// measure drift. probe runs one client count. On an error Search returns the
// probes so far with it.
func Search(ctx context.Context, cfg SearchConfig, probe func(context.Context, int) (Result, error)) (SearchResult, error) {
	if err := cfg.Validate(); err != nil {
		return SearchResult{}, err
	}
	s := &search{cfg: cfg, probe: probe, nopm: map[int]float64{}}
	if err := s.run(ctx); err != nil {
		return s.result(), err
	}
	res := s.result()
	if res.PeakNOPM == 0 {
		return res, errors.New("search: no run completed any new orders")
	}
	again, err := probe(ctx, res.PeakClients)
	if err != nil {
		return res, err
	}
	res.Drift = (again.NOPM - res.PeakNOPM) / res.PeakNOPM
	return res, nil
}

type search struct {
	cfg    SearchConfig
	probe  func(context.Context, int) (Result, error)
	probes []Probe
	nopm   map[int]float64
}

var errBudget = errors.New("probe budget spent")

// measure runs clients once and returns its NOPM.
func (s *search) measure(ctx context.Context, clients int) (float64, error) {
	if nopm, ok := s.nopm[clients]; ok {
		return nopm, nil
	}
	if len(s.probes) >= s.cfg.MaxProbes {
		return 0, errBudget
	}
	res, err := s.probe(ctx, clients)
	if err != nil {
		return 0, err
	}
	s.probes = append(s.probes, Probe{Clients: clients, Result: res})
	s.nopm[clients] = res.NOPM
	return res.NOPM, nil
}

// run probes until every bisection reaches Resolution or the budget is spent.
func (s *search) run(ctx context.Context) error {
	err := s.bracket(ctx)
	if err == nil {
		err = s.refinePeak(ctx)
	}
	if err == nil {
		err = s.refineSaturation(ctx)
	}
	if err == nil {
		err = s.refineDecline(ctx)
	}
	if errors.Is(err, errBudget) {
		return nil
	}
	return err
}

// bracket doubles clients until NOPM falls below Threshold of the best run.
func (s *search) bracket(ctx context.Context) error {
	best := 0.0
	for clients := s.cfg.MinClients; ; clients *= 2 {
		clients = min(clients, s.cfg.MaxClients)
		nopm, err := s.measure(ctx, clients)
		if err != nil {
			return err
		}
		best = max(best, nopm)
		if nopm < s.cfg.Threshold*best || clients == s.cfg.MaxClients {
			return nil
		}
	}
}

// refinePeak bisects the gaps beside the best run, the higher neighbour first.
// A neighbour above within Tolerance of the peak is a plateau, and its gap is
// left alone.
func (s *search) refinePeak(ctx context.Context) error {
	for {
		peak := s.peak()
		below, above := s.neighbours(peak)
		var gaps [][2]int
		if below > 0 && peak-below > s.cfg.Resolution {
			gaps = append(gaps, [2]int{below, peak})
		}
		plateau := s.nopm[above] >= (1-s.cfg.Tolerance)*s.nopm[peak]
		if above > 0 && above-peak > s.cfg.Resolution && !plateau {
			gaps = append(gaps, [2]int{peak, above})
		}
		if len(gaps) == 0 {
			return nil
		}
		if len(gaps) == 2 && s.nopm[above] > s.nopm[below] {
			gaps[0], gaps[1] = gaps[1], gaps[0]
		}
		if _, err := s.measure(ctx, (gaps[0][0]+gaps[0][1])/2); err != nil {
			return err
		}
	}
}

// refineSaturation bisects between the last run below the threshold and the
// first at or above it, on the rising side of the peak.
func (s *search) refineSaturation(ctx context.Context) error {
	for {
		peak := s.peak()
		target := s.cfg.Threshold * s.nopm[peak]
		low, high := 0, peak
		for _, clients := range s.measured() {
			if clients > peak {
				break
			}
			if s.nopm[clients] < target {
				low, high = clients, peak
			} else if low > 0 && clients < high {
				high = clients
			}
		}
		if low == 0 || high-low <= s.cfg.Resolution {
			return nil
		}
		if _, err := s.measure(ctx, (low+high)/2); err != nil {
			return err
		}
	}
}

// refineDecline bisects between the peak and the first run above it that is
// below the threshold.
func (s *search) refineDecline(ctx context.Context) error {
	for {
		peak := s.peak()
		target := s.cfg.Threshold * s.nopm[peak]
		low, high := peak, 0
		for _, clients := range s.measured() {
			if clients <= peak {
				continue
			}
			if s.nopm[clients] < target {
				high = clients
				break
			}
			low = clients
		}
		if high == 0 || high-low <= s.cfg.Resolution {
			return nil
		}
		if _, err := s.measure(ctx, (low+high)/2); err != nil {
			return err
		}
	}
}

// measured returns the client counts run so far, ascending.
func (s *search) measured() []int {
	counts := make([]int, 0, len(s.nopm))
	for clients := range s.nopm {
		counts = append(counts, clients)
	}
	slices.Sort(counts)
	return counts
}

// peak returns the fewest clients within Tolerance of the highest NOPM.
func (s *search) peak() int {
	best := 0.0
	for _, nopm := range s.nopm {
		best = max(best, nopm)
	}
	for _, clients := range s.measured() {
		if s.nopm[clients] >= (1-s.cfg.Tolerance)*best {
			return clients
		}
	}
	return 0
}

// neighbours returns the measured client counts just below and above clients,
// or 0 where there is none.
func (s *search) neighbours(clients int) (int, int) {
	below, above := 0, 0
	for _, c := range s.measured() {
		if c < clients {
			below = c
		}
		if c > clients && above == 0 {
			above = c
		}
	}
	return below, above
}

// result derives the peak, saturation and decline from every probe so far.
func (s *search) result() SearchResult {
	res := SearchResult{Probes: s.probes}
	if len(s.nopm) == 0 {
		return res
	}
	res.PeakClients = s.peak()
	res.PeakNOPM = s.nopm[res.PeakClients]
	target := s.cfg.Threshold * res.PeakNOPM
	for _, clients := range s.measured() {
		if clients <= res.PeakClients && res.SaturationClients == 0 && s.nopm[clients] >= target {
			res.SaturationClients = clients
		}
		if clients > res.PeakClients && res.DeclineClients == 0 && s.nopm[clients] < target {
			res.DeclineClients = clients
		}
	}
	return res
}
