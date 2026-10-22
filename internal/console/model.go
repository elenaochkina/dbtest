package console

import (
	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/internal/stack"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

type model struct {
	width         int
	height        int
	components    []component
	events        <-chan stack.Event
	retry         func()
	startRecovery func(workflows.RecoveryWorkflowConfig) (string, error)
	cursor        int
	form          recoveryForm
	log           []logbus.Line
	collectLogs   func() []logbus.Line
}

// New returns the console model, showing the service launcher's components and
// the lines logged to logs.
func New(serviceLauncher *stack.ServiceLauncher, logs *logbus.Bus) tea.Model {
	return model{
		components: []component{
			{name: "state db", state: stack.StateStarting},
			{name: "temporal", state: stack.StateStarting},
			{name: "telemetry", state: stack.StateStarting},
			{name: "worker", state: stack.StateStarting},
		},
		events:        serviceLauncher.Events(),
		retry:         serviceLauncher.Retry,
		startRecovery: serviceLauncher.StartRecovery,
		collectLogs:   logs.Collect,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.events), collectLogs(m.collectLogs))
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
	case stack.Event:
		m.setComponentState(message)
		return m, waitForEvent(m.events)
	case logBatchMsg:
		m.appendLog(message)
		return m, collectLogs(m.collectLogs)
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}
