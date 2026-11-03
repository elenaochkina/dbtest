package console

import (
	"log/slog"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elenaochkina/dbtest/internal/service"
)

var (
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	cursorStyle = lipgloss.NewStyle().Bold(true)
	timeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	keyStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	actionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("183"))

	stateStyles = map[service.State]lipgloss.Style{
		service.StateStarting: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		service.StateUp:       lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		service.StateError:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

// levelStyle returns yellow for warnings, red for errors and plain otherwise.
func levelStyle(level slog.Level) lipgloss.Style {
	switch {
	case level >= slog.LevelError:
		return stateStyles[service.StateError]
	case level >= slog.LevelWarn:
		return stateStyles[service.StateStarting]
	}
	return lipgloss.NewStyle()
}

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

// keyHelp shows action and key pairs, each action in lilac and its key in
// white, separated by grey dots.
func keyHelp(actionsAndKeys ...string) string {
	var pairs []string
	for index := 0; index+1 < len(actionsAndKeys); index += 2 {
		pairs = append(pairs, actionStyle.Render(actionsAndKeys[index])+" "+keyStyle.Render(actionsAndKeys[index+1]))
	}
	return strings.Join(pairs, helpStyle.Render(" · "))
}
