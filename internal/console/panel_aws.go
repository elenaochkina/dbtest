package console

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elenaochkina/dbtest/internal/service"
	"github.com/elenaochkina/dbtest/provider"
)

// checkAWS checks AWS in the background and delivers the result as a message.
func checkAWS(check func(context.Context) provider.Status) tea.Cmd {
	return func() tea.Msg {
		return awsStatusMsg(check(context.Background()))
	}
}

// setAWSStatus stores the AWS status and logs the detail when AWS is not ready.
func (m *model) setAWSStatus(status awsStatusMsg) {
	m.aws = provider.Status(status)
	if m.aws.State != provider.StateReady {
		slog.Warn("aws not ready", "state", m.aws.State, "detail", m.aws.Detail)
	}
}

// awsLine returns the AWS row: checking until the first result, then the
// state and its detail.
func (m model) awsLine() string {
	state := string(m.aws.State)
	if state == "" {
		state = "checking"
	}
	return padRight("aws", 12) + " " + awsStyle(m.aws.State).Render("● "+padRight(state, 13)) + " " + m.aws.Detail
}

func awsStyle(state provider.State) lipgloss.Style {
	switch state {
	case provider.StateReady:
		return stateStyles[service.StateUp]
	case provider.StateMisconfigured:
		return stateStyles[service.StateError]
	}
	return stateStyles[service.StateStarting]
}
