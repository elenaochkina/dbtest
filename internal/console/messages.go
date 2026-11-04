package console

import (
	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/internal/providercfg"
)

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logbus.Line

// awsStatusMsg carries the result of an AWS check.
type awsStatusMsg providercfg.Status

// awsTickMsg asks for the next periodic AWS check.
type awsTickMsg struct{}
