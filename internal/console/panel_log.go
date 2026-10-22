package console

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/logbus"
)

const logCapacity = 2000

// collectLogs waits 75 ms, then delivers the lines logged since the previous
// collection as a message.
func collectLogs(collect func() []logbus.Line) tea.Cmd {
	return tea.Tick(75*time.Millisecond, func(time.Time) tea.Msg {
		return logBatchMsg(collect())
	})
}

// appendLog adds the lines and drops the oldest beyond logCapacity.
func (m *model) appendLog(lines logBatchMsg) {
	m.log = append(m.log, lines...)
	if len(m.log) > logCapacity {
		m.log = m.log[len(m.log)-logCapacity:]
	}
}

// logLines returns the newest log lines that fit the height, as time, level,
// message and attributes.
func (m model) logLines(height int) []string {
	lines := make([]string, 0, height)
	for _, line := range m.log[max(0, len(m.log)-height):] {
		text := timeStyle.Render(line.Time.Format("15:04:05")) + " " + levelStyle(line.Level).Render(padRight(line.Level.String(), 5)) + " " + line.Message
		if line.Attrs != "" {
			text += " " + timeStyle.Render(line.Attrs)
		}
		lines = append(lines, text)
	}
	return lines
}
