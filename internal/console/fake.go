package console

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeStartup reports each component as up after a short delay.
func fakeStartup() tea.Cmd {
	return tea.Batch(
		fakeUp(1*time.Second, "telemetry", "http://localhost:9090/metrics"),
		fakeUp(2*time.Second, "state db", "postgres://localhost:5433/postgres"),
		fakeUp(3*time.Second, "temporal", "localhost:7233"),
	)
}

// fakeUp sends a componentStateMsg marking the component up after the delay.
func fakeUp(delay time.Duration, name, detail string) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return componentStateMsg{name: name, state: stateUp, detail: detail}
	})
}

// fakeLogs sends a made-up log line every second.
func fakeLogs() <-chan logBatchMsg {
	logs := make(chan logBatchMsg)
	go func() {
		for count := 1; ; count++ {
			time.Sleep(time.Second)
			logs <- logBatchMsg{{time: time.Now(), text: fmt.Sprintf("fake log line %d", count)}}
		}
	}()
	return logs
}
