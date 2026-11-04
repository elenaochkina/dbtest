package console

import (
	"context"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elenaochkina/dbtest/internal/providercfg"
	"github.com/elenaochkina/dbtest/internal/service"
)

// checkAWS checks AWS in the background and delivers the result as a message.
func checkAWS(check func(context.Context) providercfg.Status) tea.Cmd {
	return func() tea.Msg {
		return awsStatusMsg(check(context.Background()))
	}
}

// awsTick waits 15 seconds, then asks for the next AWS check.
func awsTick() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg {
		return awsTickMsg{}
	})
}

// setAWSStatus stores the AWS status and logs the detail when AWS is not ready.
func (m *model) setAWSStatus(status awsStatusMsg) {
	changed := providercfg.Status(status) != m.aws
	m.aws = providercfg.Status(status)
	if changed && m.aws.State != providercfg.StateReady {
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

func awsStyle(state providercfg.State) lipgloss.Style {
	switch state {
	case providercfg.StateReady:
		return stateStyles[service.StateUp]
	case providercfg.StateMisconfigured:
		return stateStyles[service.StateError]
	}
	return stateStyles[service.StateStarting]
}
