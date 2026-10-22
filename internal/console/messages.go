package console

import "github.com/elenaochkina/dbtest/internal/logbus"

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logbus.Line
