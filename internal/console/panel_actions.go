package console

import (
	"fmt"
	"strings"
)

var actions = []string{
	"start pgbench",
	"start recovery",
	"configure providers",
	"list resources",
	"past workflows",
	"exit",
}

// actionLines lays the actions out in as many columns as fit the width.
func (m model) actionLines(width int) []string {
	cellWidth := 0
	for number, action := range actions {
		cellWidth = max(cellWidth, len(fmt.Sprintf("> %d %s", number+1, action))+2)
	}
	columns := max(1, width/cellWidth)

	var lines []string
	var line strings.Builder
	for number, action := range actions {
		marker := "  "
		if number == m.cursor {
			marker = "> "
		}
		cell := padRight(fmt.Sprintf("%s%d %s", marker, number+1, action), cellWidth)
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
