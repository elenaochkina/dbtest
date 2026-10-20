package console

import tea "charm.land/bubbletea/v2"

type model struct {
	width      int
	height     int
	components []component
	cursor     int
	log        []logLine
	logs       <-chan logBatchMsg
}

// New returns the console model.
func New() tea.Model {
	return model{
		components: []component{
			{name: "state db", state: stateStarting},
			{name: "temporal", state: stateStarting},
			{name: "telemetry", state: stateStarting},
		},
		logs: fakeLogs(),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(fakeStartup(), waitForLogs(m.logs))
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
	case componentStateMsg:
		m.setComponentState(message)
	case logBatchMsg:
		m.appendLog(message)
		return m, waitForLogs(m.logs)
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}
