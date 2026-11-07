package console

import (
	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/provider"
)

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logbus.Line

// cloudStatusMsg carries the result of one cloud's check.
type cloudStatusMsg struct {
	name   provider.CloudName
	status provider.Status
}
