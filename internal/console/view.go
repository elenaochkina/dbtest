package console

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "dbtest"
	return view
}

// render draws the STATUS, ACTIONS and LOG panels to fill the window.
func (m model) render() string {
	if m.width == 0 {
		return ""
	}
	inner := m.width - 4
	status := m.statusLines()
	actions := m.actionLines(inner)
	logHeight := m.height - 4 - len(status) - len(actions)
	if inner < 20 || logHeight < 1 {
		return "terminal too small"
	}

	lines := []string{rule("┌", "┐", "STATUS", "dbtest", m.width)}
	for _, line := range status {
		lines = append(lines, row(line, inner))
	}
	lines = append(lines, rule("├", "┤", "ACTIONS", "", m.width))
	for _, line := range actions {
		lines = append(lines, row(line, inner))
	}
	lines = append(lines, rule("├", "┤", "LOG", "", m.width))
	logLines := m.logLines(logHeight)
	for index := range logHeight {
		line := ""
		if index < len(logLines) {
			line = logLines[index]
		}
		lines = append(lines, row(line, inner))
	}
	lines = append(lines, borderStyle.Render("└"+strings.Repeat("─", m.width-2)+"┘"))
	return strings.Join(lines, "\n")
}
