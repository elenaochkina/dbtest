// Package providercfg checks whether a cloud is ready to use.
package providercfg

import (
	"context"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

// CheckAWS reports whether the AWS cloud can run.
func CheckAWS(ctx context.Context) provider.Status {
	c, err := cloud.NewAWS()
	if err != nil {
		return provider.Status{State: provider.StateMisconfigured, Detail: err.Error()}
	}
	return c.Check(ctx)
}
