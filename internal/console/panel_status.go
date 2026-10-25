package console

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/service"
)

type component struct {
	name   string
	state  service.State
	detail string
}

// waitForEvent waits for the next component state change and delivers it as
// a message.
func waitForEvent(events <-chan service.Event) tea.Cmd {
	return func() tea.Msg {
		return <-events
	}
}

// setComponentState stores the new state of the named component.
func (m *model) setComponentState(event service.Event) {
	for index := range m.components {
		if m.components[index].name == event.Name {
			m.components[index].state = event.State
			m.components[index].detail = event.Detail
		}
	}
}

// statusLines returns one line per line of each component's detail, then the AWS row.
func (m model) statusLines() []string {
	lines := make([]string, 0, len(m.components))
	for _, component := range m.components {
		state := stateStyles[component.state].Render("● " + padRight(string(component.state), 13))
		for _, line := range strings.Split(component.detail, "\n") {
			name := component.name
			if label, address, found := strings.Cut(line, "\t"); found {
				name += " " + label
				line = address
			}
			lines = append(lines, fmt.Sprintf("%s %s %s", padRight(name, 12), state, line))
		}
	}
	return append(lines, m.awsLine())
}
