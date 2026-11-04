package console

import (
	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/internal/pricing"
	"github.com/elenaochkina/dbtest/internal/providercfg"
)

// logBatchMsg carries log lines that arrived since the previous batch.
type logBatchMsg []logbus.Line

// awsStatusMsg carries the result of an AWS check.
type awsStatusMsg providercfg.Status

// pricesMsg carries the prices fetched for one provider and instance class.
type pricesMsg struct {
	key    string
	prices pricing.Prices
	err    error
}
