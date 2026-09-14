// Package probe measures database availability and holds the result contract
// between the probe container and whatever reads its stdout.
package probe

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Outage is one uninterrupted run of failed observations, bracketed by successes.
type Outage struct {
	LastOK       time.Time      `json:"last_ok"`        // last success before it
	FirstFailure time.Time      `json:"first_failure"`  // first observation that failed
	FirstOKAfter time.Time      `json:"first_ok_after"` // first success after it
	DownMs       float64        `json:"down_ms"`        // LastOK → FirstOKAfter
	Failures     int            `json:"failures"`
	Errors       map[string]int `json:"errors"`       // classification → count
	LostCommits  int64          `json:"lost_commits"` // acked before, missing after

	// consecutiveOK is the current run of successes. Unexported, so it stays out
	// of the JSON.
	consecutiveOK int
}

// Availability is one detector's findings.
type Availability struct {
	Outages       []Outage `json:"outages"`
	LongestDownMs float64  `json:"longest_down_ms"`
	Recovered     bool     `json:"recovered"` // saw at least one outage begin and end
}

// Result is what the harness reads off stdout.
type Result struct {
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	IntervalMs float64   `json:"interval_ms"`
	Samples    int       `json:"samples"`

	Readable Availability `json:"readable"` // connect + read the counter
	Writable Availability `json:"writable"` // counter advanced and acked

	AckedCommits int64  `json:"acked_commits"`
	LostCommits  int64  `json:"lost_commits"` // acked, then missing after recovery
	Error        string `json:"error,omitempty"`
}

// Parse reads the result out of a container's output: the last line that parses
// as a Result. Runners return stdout and stderr interleaved, so the last
// non-empty line is a log line as often as it is the result.
func Parse(out []byte) (Result, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var res Result
		if err := json.Unmarshal([]byte(line), &res); err == nil {
			return res, nil
		}
	}
	return Result{}, fmt.Errorf("no probe result in %d lines of output", len(lines))
}
