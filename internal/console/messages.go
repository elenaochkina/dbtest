package console

import "time"

type logLine struct {
	time time.Time
	text string
}

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logLine
