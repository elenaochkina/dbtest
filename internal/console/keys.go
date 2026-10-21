package console

import tea "charm.land/bubbletea/v2"

// handleKey moves the cursor, runs the selected action, retries failed
// components or quits.
func (m model) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key := message.String(); key {
	case "up":
		m.cursor = max(0, m.cursor-1)
	case "down":
		m.cursor = min(len(actions)-1, m.cursor+1)
	case "1", "2", "3", "4", "5", "6":
		m.cursor = int(key[0] - '1')
	case "enter":
		if actions[m.cursor] == "exit" {
			return m, tea.Quit
		}
	case "r":
		m.retry()
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}
