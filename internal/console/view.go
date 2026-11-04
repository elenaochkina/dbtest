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

// render draws the STATUS, ACTIONS (or the open form) and LOG panels to fill the window.
func (m model) render() string {
	if m.width == 0 {
		return ""
	}
	inner := m.width - 4
	status := m.statusLines()
	middleTitle, middle := "ACTIONS", m.actionLines(inner)
	if m.form.step != "" {
		middleTitle, middle = "RECOVERY › "+m.form.step, m.form.lines(m.estimateLine())
	}
	logHeight := m.height - 4 - len(status) - len(middle)
	if inner < 20 || logHeight < 1 {
		return "terminal too small"
	}

	lines := []string{rule("┌", "┐", "STATUS", "dbtest", m.width)}
	for _, line := range status {
		lines = append(lines, row(line, inner))
	}
	lines = append(lines, rule("├", "┤", middleTitle, "", m.width))
	for _, line := range middle {
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
