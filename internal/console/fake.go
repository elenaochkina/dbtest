package console

import (
	"fmt"
	"time"
)

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
