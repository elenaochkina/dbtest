package console

import (
	"fmt"
	"strings"
)

// action is an entry in the ACTIONS menu; its value is its position.
type action int

const (
	actionStartPgbench action = iota
	actionStartRecovery
	actionPastWorkflows
	actionExit
)

var actions = [...]string{
	actionStartPgbench:  "start pgbench",
	actionStartRecovery: "start recovery",
	actionPastWorkflows: "past workflows",
	actionExit:          "exit",
}

// actionLines lays the actions out in as many columns as fit the width.
func (m model) actionLines(width int) []string {
	cellWidth := 0
	for number, label := range actions {
		cellWidth = max(cellWidth, len(fmt.Sprintf("> %d %s", number+1, label))+2)
	}
	columns := max(1, width/cellWidth)

	var lines []string
	var line strings.Builder
	for number, label := range actions {
		marker := "  "
		if number == m.cursor {
			marker = "> "
		}
		cell := padRight(fmt.Sprintf("%s%d %s", marker, number+1, label), cellWidth)
		if number == m.cursor {
			cell = cursorStyle.Render(cell)
		}
		line.WriteString(cell)
		if (number+1)%columns == 0 || number == len(actions)-1 {
			lines = append(lines, line.String())
			line.Reset()
		}
	}
	return lines
}
