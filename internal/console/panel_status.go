package console

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/service"
)

type serviceStatus struct {
	name   string
	state  service.State
	detail string
}

// waitForEvent waits for the next service state change and delivers it as
// a message.
func waitForEvent(events <-chan service.Event) tea.Cmd {
	return func() tea.Msg {
		return <-events
	}
}

// setServiceState stores the new state of the named service.
func (m *model) setServiceState(event service.Event) {
	for index := range m.services {
		if m.services[index].name == event.Name {
			m.services[index].state = event.State
			m.services[index].detail = event.Detail
		}
	}
}

// statusLines returns one line per line of each service's detail, then the AWS row.
func (m model) statusLines() []string {
	lines := make([]string, 0, len(m.services))
	for _, status := range m.services {
		state := stateStyles[status.state].Render("● " + padRight(string(status.state), 13))
		for _, line := range strings.Split(status.detail, "\n") {
			name := status.name
			if label, address, found := strings.Cut(line, "\t"); found {
				name += " " + label
				line = address
			}
			lines = append(lines, fmt.Sprintf("%s %s %s", padRight(name, 12), state, line))
		}
	}
	return append(lines, m.awsLine())
}
