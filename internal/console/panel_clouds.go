package console

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elenaochkina/dbtest/internal/service"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

type cloudStatus struct {
	name   provider.CloudName
	status provider.Status
}

// checkCloud builds the named cloud and checks it.
func checkCloud(ctx context.Context, name provider.CloudName) provider.Status {
	c, err := cloud.NewCloudProvider(name)
	if err != nil {
		return provider.Status{State: provider.StateMisconfigured, Detail: err.Error()}
	}
	return c.Check(ctx)
}

// checkClouds checks each cloud in the background and delivers each result as a message.
func checkClouds(clouds []cloudStatus, check func(context.Context, provider.CloudName) provider.Status) tea.Cmd {
	var commands []tea.Cmd
	for _, c := range clouds {
		commands = append(commands, func() tea.Msg {
			return cloudStatusMsg{name: c.name, status: check(context.Background(), c.name)}
		})
	}
	return tea.Batch(commands...)
}

// setCloudStatus stores the cloud's status and logs the detail when it is not ready.
func (m *model) setCloudStatus(message cloudStatusMsg) {
	for index := range m.clouds {
		if m.clouds[index].name == message.name {
			m.clouds[index].status = message.status
		}
	}
	if message.status.State != provider.StateReady {
		slog.Warn("cloud not ready", "cloud", message.name, "state", message.status.State, "detail", message.status.Detail)
	}
}

// cloudLines returns one row per cloud: checking until the first result, then
// the state and its detail.
func (m model) cloudLines() []string {
	var lines []string
	for _, c := range m.clouds {
		state := string(c.status.State)
		if state == "" {
			state = "checking"
		}
		lines = append(lines, padRight(string(c.name), 12)+" "+cloudStyle(c.status.State).Render("● "+padRight(state, 13))+" "+c.status.Detail)
	}
	return lines
}

func cloudStyle(state provider.State) lipgloss.Style {
	switch state {
	case provider.StateReady:
		return stateStyles[service.StateUp]
	case provider.StateMisconfigured:
		return stateStyles[service.StateError]
	}
	return stateStyles[service.StateStarting]
}
