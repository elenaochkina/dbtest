package hammerdb

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Result holds one TPROC-C run.
type Result struct {
	NOPM   float64
	TPM    float64
	Timing map[string]Timing // keyed by transaction: NEWORD, PAYMENT, DELIVERY, OSTAT, SLEV
}

// Timing holds one transaction's call count and latency percentiles.
type Timing struct {
	Calls float64 `json:"calls,string"`
	P50Ms float64 `json:"p50_ms,string"`
	P95Ms float64 `json:"p95_ms,string"`
	P99Ms float64 `json:"p99_ms,string"`
}

// Metrics returns NOPM, TPM and each transaction's percentiles.
func (r Result) Metrics() map[string]float64 {
	metrics := map[string]float64{"nopm": r.NOPM, "tpm": r.TPM}
	for name, timing := range r.Timing {
		prefix := strings.ToLower(name)
		metrics[prefix+"_p50_ms"] = timing.P50Ms
		metrics[prefix+"_p95_ms"] = timing.P95Ms
		metrics[prefix+"_p99_ms"] = timing.P99Ms
	}
	return metrics
}

// resultRe matches: TEST RESULT : System achieved N NOPM from N PostgreSQL TPM
var resultRe = regexp.MustCompile(`TEST RESULT : System achieved (\d+) NOPM from (\d+) PostgreSQL TPM`)

// parseResult reads NOPM and TPM from hammerdbcli output, and the transaction
// timing from the JSON that `job <id> timing` prints after the DBTEST TIMING line.
func parseResult(output string) (Result, error) {
	match := resultRe.FindStringSubmatch(output)
	if match == nil {
		return Result{}, fmt.Errorf("hammerdb output: TEST RESULT line not found\n%s", tail(output))
	}
	nopm, _ := strconv.ParseFloat(match[1], 64)
	tpm, _ := strconv.ParseFloat(match[2], 64)
	result := Result{NOPM: nopm, TPM: tpm}

	_, timing, found := strings.Cut(output, "DBTEST TIMING")
	if !found {
		return Result{}, fmt.Errorf("hammerdb output: no timing\n%s", tail(output))
	}
	if err := json.NewDecoder(strings.NewReader(timing)).Decode(&result.Timing); err != nil {
		return Result{}, fmt.Errorf("hammerdb timing: %w\n%s", err, tail(output))
	}
	return result, nil
}
