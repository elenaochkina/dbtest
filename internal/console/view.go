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

// render draws the STATUS and DASHBOARDS panels side by side, or one above the
// other when the window is narrow, then ACTIONS (or the open form) and LOG to
// fill the window.
func (m model) render() string {
	if m.width == 0 {
		return ""
	}
	inner := m.width - 4
	status := m.statusLines()
	dashboards := m.dashboardLines()
	middleTitle, middle := "ACTIONS", m.actionLines(inner)
	if m.form.step != "" {
		middleTitle, middle = "RECOVERY › "+m.form.step, m.form.lines()
	}

	// Side by side, each line is "│ left │ right │", so the two widths add up
	// to the window width minus 7 border and space cells.
	rightWidth := m.width * 2 / 5
	leftWidth := m.width - 7 - rightWidth
	sideBySide := rightWidth >= 42

	var lines []string
	if sideBySide {
		lines = append(lines, rule("┌", "┬", "STATUS", "", leftWidth+4)+rule("", "┐", "DASHBOARDS", "dbtest", rightWidth+4))
		for index := range max(len(status), len(dashboards)) {
			var left, right string
			if index < len(status) {
				left = status[index]
			}
			if index < len(dashboards) {
				right = dashboards[index]
			}
			lines = append(lines, rowPair(left, right, leftWidth, rightWidth))
		}
		lines = append(lines, rule("├", "┴", middleTitle, "", leftWidth+4)+borderStyle.Render(strings.Repeat("─", rightWidth+2)+"┤"))
	} else {
		lines = append(lines, rule("┌", "┐", "STATUS", "dbtest", m.width))
		for _, line := range status {
			lines = append(lines, row(line, inner))
		}
		// The DASHBOARDS panel is left out when it would leave no room for LOG.
		if m.height >= 6+len(status)+len(dashboards)+len(middle) {
			lines = append(lines, rule("├", "┤", "DASHBOARDS", "", m.width))
			for _, line := range dashboards {
				lines = append(lines, row(line, inner))
			}
		}
		lines = append(lines, rule("├", "┤", middleTitle, "", m.width))
	}
	for _, line := range middle {
		lines = append(lines, row(line, inner))
	}

	logHeight := m.height - 2 - len(lines)
	if inner < 20 || logHeight < 1 {
		return "terminal too small"
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
