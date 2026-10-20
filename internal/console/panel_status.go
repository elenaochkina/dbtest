package console

import "fmt"

type component struct {
	name   string
	state  componentState
	detail string
}

// setComponentState stores the new state of the named component.
func (m *model) setComponentState(message componentStateMsg) {
	for index := range m.components {
		if m.components[index].name == message.name {
			m.components[index].state = message.state
			m.components[index].detail = message.detail
		}
	}
}

// statusLines returns one line per component.
func (m model) statusLines() []string {
	lines := make([]string, 0, len(m.components))
	for _, component := range m.components {
		state := stateStyles[component.state].Render("● " + padRight(string(component.state), 9))
		lines = append(lines, fmt.Sprintf("%s %s %s", padRight(component.name, 10), state, component.detail))
	}
	return lines
}
