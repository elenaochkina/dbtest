package console

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// newTestModel returns a model with all components starting and no log feed.
func newTestModel() model {
	return model{
		components: []component{
			{name: "state db", state: stateStarting},
			{name: "temporal", state: stateStarting},
			{name: "telemetry", state: stateStarting},
		},
	}
}

// send passes each message to Update and returns the resulting model.
func send(current model, messages ...tea.Msg) model {
	for _, message := range messages {
		next, _ := current.Update(message)
		current = next.(model)
	}
	return current
}

func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}

func TestCursorStopsAtTop(t *testing.T) {
	current := send(newTestModel(), keyPress("up"))
	if current.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", current.cursor)
	}
}

func TestCursorStopsAtBottom(t *testing.T) {
	current := newTestModel()
	for range len(actions) + 3 {
		current = send(current, keyPress("down"))
	}
	if want := len(actions) - 1; current.cursor != want {
		t.Fatalf("cursor = %d, want %d", current.cursor, want)
	}
}

func TestDigitSelectsAction(t *testing.T) {
	current := send(newTestModel(), keyPress("3"))
	if current.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", current.cursor)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, keys := range [][]string{{"q"}, {"6", "enter"}} {
		current := newTestModel()
		var command tea.Cmd
		for _, key := range keys {
			var next tea.Model
			next, command = current.Update(keyPress(key))
			current = next.(model)
		}
		if command == nil {
			t.Fatalf("%v: no command, want quit", keys)
		}
		if _, isQuit := command().(tea.QuitMsg); !isQuit {
			t.Fatalf("%v: command is not quit", keys)
		}
	}
}

func TestEnterOnOtherActionDoesNotQuit(t *testing.T) {
	_, command := send(newTestModel(), keyPress("1")).Update(keyPress("enter"))
	if command != nil {
		t.Fatal("enter on start pgbench returned a command")
	}
}

func TestComponentStateIsStored(t *testing.T) {
	current := send(newTestModel(), componentStateMsg{name: "temporal", state: stateUp, detail: "localhost:7233"})
	temporal := current.components[1]
	if temporal.state != stateUp || temporal.detail != "localhost:7233" {
		t.Fatalf("temporal = %+v, want up at localhost:7233", temporal)
	}
	if current.components[0].state != stateStarting {
		t.Fatal("state db changed too")
	}
}

func TestLogKeepsNewestLines(t *testing.T) {
	batch := make(logBatchMsg, logCapacity+5)
	for index := range batch {
		batch[index] = logLine{time: time.Now(), text: fmt.Sprint(index)}
	}
	current := send(newTestModel(), batch)
	if len(current.log) != logCapacity {
		t.Fatalf("log has %d lines, want %d", len(current.log), logCapacity)
	}
	if current.log[0].text != "5" {
		t.Fatalf("oldest line = %q, want 5", current.log[0].text)
	}
}

func TestRenderFillsWindow(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 15}, {Width: 80, Height: 24}, {Width: 200, Height: 60}} {
		lines := strings.Split(send(newTestModel(), size).render(), "\n")
		if len(lines) != size.Height {
			t.Errorf("%dx%d: %d lines", size.Width, size.Height, len(lines))
		}
		for number, line := range lines {
			if width := lipgloss.Width(line); width != size.Width {
				t.Errorf("%dx%d: line %d is %d wide", size.Width, size.Height, number, width)
			}
		}
	}
}

func TestRenderTooSmall(t *testing.T) {
	current := send(newTestModel(), tea.WindowSizeMsg{Width: 30, Height: 8})
	if got := current.render(); got != "terminal too small" {
		t.Fatalf("render = %q, want terminal too small", got)
	}
}
