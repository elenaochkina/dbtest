package console

import (
	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/providercfg"
)

// handleKey moves the cursor, runs the selected action, retries failed
// components and checks AWS again, or quits.
func (m model) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.form.step != "" {
		return m.handleFormKey(message)
	}
	switch key := message.String(); key {
	case "left":
		m.cursor = max(0, m.cursor-1)
	case "right":
		m.cursor = min(len(actions)-1, m.cursor+1)
	case "1", "2", "3", "4":
		m.cursor = int(key[0] - '1')
	case "enter":
		switch actions[m.cursor] {
		case "start recovery":
			m.form = openRecoveryForm()
		case "exit":
			return m, tea.Quit
		}
	case "r":
		m.retry()
		m.aws = providercfg.Status{}
		return m, checkAWS(m.checkAWS)
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// handleFormKey moves the form's cursor or edits its input, shows the next or
// previous step, returns to the menu, and starts the recovery after the last step.
func (m model) handleFormKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "up":
		m.form = m.form.move(-1)
	case "down":
		m.form = m.form.move(1)
	case "enter":
		form, done := m.form.next()
		if done {
			m.form = recoveryForm{}
			return m, startRecovery(m.startRecovery, form.cfg)
		}
		m.form = form
		if form.step == "confirm" {
			return m, m.fetchPricesFor(form.cfg)
		}
	case "esc":
		m.form = m.form.back()
	case "m":
		m.form = recoveryForm{}
	case "backspace":
		m.form = m.form.erase()
	case "q", "ctrl+c":
		return m, tea.Quit
	default:
		m.form = m.form.typeText(message.Text)
	}
	return m, nil
}
