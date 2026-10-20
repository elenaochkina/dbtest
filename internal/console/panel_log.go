package console

import tea "charm.land/bubbletea/v2"

const logCapacity = 2000

// waitForLogs waits for the next batch of log lines and delivers it as a message.
func waitForLogs(logs <-chan logBatchMsg) tea.Cmd {
	return func() tea.Msg {
		return <-logs
	}
}

// appendLog adds the lines and drops the oldest beyond logCapacity.
func (m *model) appendLog(lines logBatchMsg) {
	m.log = append(m.log, lines...)
	if len(m.log) > logCapacity {
		m.log = m.log[len(m.log)-logCapacity:]
	}
}

// logLines returns the newest log lines that fit the height.
func (m model) logLines(height int) []string {
	lines := make([]string, 0, height)
	for _, line := range m.log[max(0, len(m.log)-height):] {
		lines = append(lines, timeStyle.Render(line.time.Format("15:04:05"))+" "+line.text)
	}
	return lines
}
