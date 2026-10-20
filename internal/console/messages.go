package console

import "time"

type componentState string

const (
	stateStarting componentState = "starting"
	stateUp       componentState = "up"
	stateError    componentState = "error"
)

// componentStateMsg reports that a component changed state.
type componentStateMsg struct {
	name   string
	state  componentState
	detail string
}

type logLine struct {
	time time.Time
	text string
}

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logLine
