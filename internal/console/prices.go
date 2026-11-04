package console

import (
	"context"
	"fmt"
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/pricing"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

// priceFetch is one fetch of prices for a provider and instance class. done is
// false while the fetch runs.
type priceFetch struct {
	prices pricing.Prices
	err    error
	done   bool
}

// priceKey returns the map key for cfg's provider and instance class, such as
// "aurora db.r6g.large".
func priceKey(cfg workflows.RecoveryWorkflowConfig) string {
	return string(cfg.Provider) + " " + pricing.InstanceClass(cfg.Provider, cfg.Request)
}

// fetchPricesFor starts fetching cfg's prices in the background, unless the
// provider is Docker or the prices are fetched or being fetched.
func (m *model) fetchPricesFor(cfg workflows.RecoveryWorkflowConfig) tea.Cmd {
	if cfg.Provider == provider.Docker || m.fetchPrices == nil {
		return nil
	}
	key := priceKey(cfg)
	if entry, found := m.prices[key]; found && entry.err == nil {
		return nil
	}
	m.prices[key] = priceFetch{}
	fetch := m.fetchPrices
	class := pricing.InstanceClass(cfg.Provider, cfg.Request)
	return func() tea.Msg {
		prices, err := fetch(context.Background(), cfg.Provider, class)
		return pricesMsg{key: key, prices: prices, err: err}
	}
}

// setPrices stores a finished fetch and logs a failure.
func (m *model) setPrices(message pricesMsg) {
	m.prices[message.key] = priceFetch{prices: message.prices, err: message.err, done: true}
	if message.err != nil {
		slog.Warn("price fetch failed", "prices", message.key, "error", message.err)
	}
}

// estimateLine returns the confirm step's cost line for an AWS run, or "" for
// Docker.
func (m model) estimateLine() string {
	cfg := m.form.cfg
	if cfg.Provider == provider.Docker {
		return ""
	}
	label := padRight("estimated cost", 19)
	entry, found := m.prices[priceKey(cfg)]
	switch {
	case m.fetchPrices == nil:
		return label + "unavailable"
	case !found || !entry.done:
		return label + "checking…"
	case entry.err != nil:
		return label + "unavailable (see log)"
	}
	hourly, err := pricing.HourlyCost(entry.prices, cfg.Provider, cfg.Request)
	if err != nil {
		return label + "unavailable (" + err.Error() + ")"
	}
	minutes := estimatedMinutes(cfg)
	class := pricing.InstanceClass(cfg.Provider, cfg.Request)
	return label + fmt.Sprintf("~$%.2f  (%s, ~%.0f min)", pricing.Cost(hourly, minutes), class, minutes)
}

// estimatedMinutes guesses how long a recovery run keeps AWS resources: 5
// minutes to provision, a settle before the first disruption, then a minute to
// recover and a settle for each repetition, and a minute to deprovision.
func estimatedMinutes(cfg workflows.RecoveryWorkflowConfig) float64 {
	settle := cfg.Settle.Minutes()
	return 5 + settle + float64(cfg.Repetitions)*(1+settle) + 1
}
