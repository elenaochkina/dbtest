// Package search finds the client count with the most throughput by running
// a timed benchmark at chosen client counts.
package search

import (
	"context"
	"errors"
	"slices"
)

// Config bounds a client search.
type Config struct {
	MinClients int
	MaxClients int
	// Resolution is the client gap at which a bisection stops.
	Resolution int
	// MaxTrials caps the timed runs, not counting the final drift run.
	MaxTrials int
	// Tolerance is the throughput fraction within which two runs count as equal.
	Tolerance float64
	// Threshold is the fraction of peak throughput that marks saturation and decline.
	Threshold float64
}

// Trial is one timed run at a client count.
type Trial struct {
	Clients    int
	Throughput float64
}

// Result holds every trial and the client counts found from them.
type Result struct {
	Trials []Trial
	// PeakClients is the fewest clients within Tolerance of the highest throughput.
	PeakClients    int
	PeakThroughput float64
	// SaturationClients is the fewest clients reaching Threshold of PeakThroughput.
	SaturationClients int
	// DeclineClients is the fewest clients above the peak below Threshold of
	// PeakThroughput, or 0 when no trial up to MaxClients fell that far.
	DeclineClients int
	// Drift is the throughput change when PeakClients runs again at the end.
	Drift float64
}

// Validate reports a config Run cannot use.
func (cfg Config) Validate() error {
	switch {
	case cfg.MinClients < 1 || cfg.MaxClients < cfg.MinClients:
		return errors.New("search needs 1 <= min clients <= max clients")
	case cfg.Resolution < 1:
		return errors.New("search needs a resolution of at least 1 client")
	case cfg.MaxTrials < 3:
		return errors.New("search needs at least 3 trials")
	case cfg.Tolerance < 0 || cfg.Tolerance >= 0.5:
		return errors.New("search needs a tolerance from 0 to 0.5")
	case cfg.Threshold <= 0 || cfg.Threshold >= 1:
		return errors.New("search needs a threshold between 0 and 1")
	}
	return nil
}

// Run runs trial at chosen client counts to find the peak, saturation and
// decline points. It doubles clients from MinClients until throughput falls
// below Threshold of the best run, bisects around the best run, then
// bisects the rising side for saturation and the falling side for decline.
// It runs no client count twice except the peak, once more at the end, to
// measure drift. trial runs one client count and returns its throughput. On
// an error Run returns the trials so far with it.
func Run(ctx context.Context, cfg Config, trial func(context.Context, int) (float64, error)) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	s := &searcher{cfg: cfg, trial: trial, throughput: map[int]float64{}}
	if err := s.run(ctx); err != nil {
		return s.result(), err
	}
	res := s.result()
	if res.PeakThroughput == 0 {
		return res, errors.New("search: no run had any throughput")
	}
	again, err := trial(ctx, res.PeakClients)
	if err != nil {
		return res, err
	}
	res.Drift = (again - res.PeakThroughput) / res.PeakThroughput
	return res, nil
}

type searcher struct {
	cfg        Config
	trial      func(context.Context, int) (float64, error)
	trials     []Trial
	throughput map[int]float64
}

var errBudget = errors.New("trial budget spent")

// measure runs clients once and returns its throughput.
func (s *searcher) measure(ctx context.Context, clients int) (float64, error) {
	if throughput, ok := s.throughput[clients]; ok {
		return throughput, nil
	}
	if len(s.trials) >= s.cfg.MaxTrials {
		return 0, errBudget
	}
	throughput, err := s.trial(ctx, clients)
	if err != nil {
		return 0, err
	}
	s.trials = append(s.trials, Trial{Clients: clients, Throughput: throughput})
	s.throughput[clients] = throughput
	return throughput, nil
}

// run runs trials until every bisection reaches Resolution or the budget is spent.
func (s *searcher) run(ctx context.Context) error {
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

// bracket doubles clients until throughput falls below Threshold of the best run.
func (s *searcher) bracket(ctx context.Context) error {
	best := 0.0
	for clients := s.cfg.MinClients; ; clients *= 2 {
		clients = min(clients, s.cfg.MaxClients)
		throughput, err := s.measure(ctx, clients)
		if err != nil {
			return err
		}
		best = max(best, throughput)
		if throughput < s.cfg.Threshold*best || clients == s.cfg.MaxClients {
			return nil
		}
	}
}

// refinePeak bisects the gaps beside the best run, the higher neighbour first.
// A neighbour above within Tolerance of the peak is a plateau, and its gap is
// left alone.
func (s *searcher) refinePeak(ctx context.Context) error {
	for {
		peak := s.peak()
		below, above := s.neighbours(peak)
		var gaps [][2]int
		if below > 0 && peak-below > s.cfg.Resolution {
			gaps = append(gaps, [2]int{below, peak})
		}
		plateau := s.throughput[above] >= (1-s.cfg.Tolerance)*s.throughput[peak]
		if above > 0 && above-peak > s.cfg.Resolution && !plateau {
			gaps = append(gaps, [2]int{peak, above})
		}
		if len(gaps) == 0 {
			return nil
		}
		if len(gaps) == 2 && s.throughput[above] > s.throughput[below] {
			gaps[0], gaps[1] = gaps[1], gaps[0]
		}
		if _, err := s.measure(ctx, (gaps[0][0]+gaps[0][1])/2); err != nil {
			return err
		}
	}
}

// refineSaturation bisects between the last run below the threshold and the
// first at or above it, on the rising side of the peak.
func (s *searcher) refineSaturation(ctx context.Context) error {
	for {
		peak := s.peak()
		target := s.cfg.Threshold * s.throughput[peak]
		low, high := 0, peak
		for _, clients := range s.measured() {
			if clients > peak {
				break
			}
			if s.throughput[clients] < target {
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
func (s *searcher) refineDecline(ctx context.Context) error {
	for {
		peak := s.peak()
		target := s.cfg.Threshold * s.throughput[peak]
		low, high := peak, 0
		for _, clients := range s.measured() {
			if clients <= peak {
				continue
			}
			if s.throughput[clients] < target {
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
func (s *searcher) measured() []int {
	counts := make([]int, 0, len(s.throughput))
	for clients := range s.throughput {
		counts = append(counts, clients)
	}
	slices.Sort(counts)
	return counts
}

// peak returns the fewest clients within Tolerance of the highest throughput.
func (s *searcher) peak() int {
	best := 0.0
	for _, throughput := range s.throughput {
		best = max(best, throughput)
	}
	for _, clients := range s.measured() {
		if s.throughput[clients] >= (1-s.cfg.Tolerance)*best {
			return clients
		}
	}
	return 0
}

// neighbours returns the measured client counts just below and above clients,
// or 0 where there is none.
func (s *searcher) neighbours(clients int) (int, int) {
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

// result derives the peak, saturation and decline from every trial so far.
func (s *searcher) result() Result {
	res := Result{Trials: s.trials}
	if len(s.throughput) == 0 {
		return res
	}
	res.PeakClients = s.peak()
	res.PeakThroughput = s.throughput[res.PeakClients]
	target := s.cfg.Threshold * res.PeakThroughput
	for _, clients := range s.measured() {
		if clients <= res.PeakClients && res.SaturationClients == 0 && s.throughput[clients] >= target {
			res.SaturationClients = clients
		}
		if clients > res.PeakClients && res.DeclineClients == 0 && s.throughput[clients] < target {
			res.DeclineClients = clients
		}
	}
	return res
}
