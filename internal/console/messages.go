package console

import (
	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/provider"
)

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logbus.Line

// awsStatusMsg carries the result of an AWS check.
type awsStatusMsg provider.Status
