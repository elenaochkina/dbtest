package console

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/internal/pricing"
	"github.com/elenaochkina/dbtest/internal/providercfg"
	"github.com/elenaochkina/dbtest/internal/service"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

type model struct {
	width         int
	height        int
	services      []serviceStatus
	events        <-chan service.Event
	retry         func()
	startRecovery func(workflows.RecoveryWorkflowConfig) (string, error)
	cursor        int
	form          recoveryForm
	log           []logbus.Line
	collectLogs   func() []logbus.Line
	aws           providercfg.Status
	checkAWS      func(context.Context) providercfg.Status
	prices        map[string]priceFetch
	fetchPrices   func(context.Context, provider.ProviderName, string) (pricing.Prices, error)
}

// New returns the console model, showing the service launcher's services and
// the lines logged to logs.
func New(serviceLauncher *service.Launcher, logs *logbus.Bus, prices *pricing.Client) tea.Model {
	current := model{
		services: []serviceStatus{
			{name: "state db", state: service.StateStarting},
			{name: "temporal", state: service.StateStarting},
			{name: "telemetry", state: service.StateStarting},
			{name: "worker", state: service.StateStarting},
		},
		events:        serviceLauncher.Events(),
		retry:         serviceLauncher.RetryFailed,
		startRecovery: serviceLauncher.StartRecovery,
		collectLogs:   logs.Collect,
		checkAWS:      providercfg.CheckAWS,
		prices:        map[string]priceFetch{},
	}
	if prices != nil {
		current.fetchPrices = prices.Fetch
	}
	return current
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.events), collectLogs(m.collectLogs), checkAWS(m.checkAWS))
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
	case service.Event:
		m.setServiceState(message)
		return m, waitForEvent(m.events)
	case logBatchMsg:
		m.appendLog(message)
		return m, collectLogs(m.collectLogs)
	case awsStatusMsg:
		m.setAWSStatus(message)
	case pricesMsg:
		m.setPrices(message)
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}
