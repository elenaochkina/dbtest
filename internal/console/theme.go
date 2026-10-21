package console

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elenaochkina/dbtest/internal/stack"
)

var (
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	titleStyle  = lipgloss.NewStyle().Bold(true)
	cursorStyle = lipgloss.NewStyle().Bold(true)
	timeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	stateStyles = map[stack.State]lipgloss.Style{
		stack.StateStarting: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		stack.StateUp:       lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		stack.StateError:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

// rule draws a horizontal border line with a title on the left and an
// optional label on the right.
func rule(left, right, title, label string, width int) string {
	head := "─ " + titleStyle.Render(title) + " "
	tail := ""
	if label != "" {
		tail = " " + titleStyle.Render(label) + " ─"
	}
	fill := max(0, width-2-lipgloss.Width(head)-lipgloss.Width(tail))
	return borderStyle.Render(left) + head + borderStyle.Render(strings.Repeat("─", fill)) + tail + borderStyle.Render(right)
}

// row draws one content line between side borders, cut or padded to width.
func row(content string, width int) string {
	content = lipgloss.NewStyle().MaxWidth(width).Render(content)
	side := borderStyle.Render("│")
	return side + " " + padRight(content, width) + " " + side
}

// padRight adds spaces until the text is width cells wide.
func padRight(text string, width int) string {
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}
