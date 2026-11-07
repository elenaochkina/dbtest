//go:build ignore

// This file is an example for review, not built. It shows AWS as a cloud after
// the split, as provider/cloud/aws.go.
//
// Compared with today:
//   - LoadDefaultConfig runs once here instead of in provider/aws and
//     internal/providercfg; services build their clients from Config().
//   - Check is providercfg.CheckAWS, moved.
//   - Runner replaces the RDS and Aurora cases of runnerFor.
//   - harness/aws still loads its own config; sharing this one is open.
package cloud

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
)

type AWS struct {
	config awssdk.Config
}

// NewAWS loads the AWS config from the environment and the shared config files.
func NewAWS() (Cloud, error) {
	config, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &AWS{config: config}, nil
}

func (c *AWS) Name() provider.CloudName { return provider.AWS }

// Check reports whether the .env variables are set and the credentials are valid.
func (c *AWS) Check(ctx context.Context) provider.Status {
	if missing := missingVariables(); len(missing) > 0 {
		return provider.Status{State: provider.StateMisconfigured, Detail: "missing: run source .env (" + strings.Join(missing, ", ") + ")"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := sts.NewFromConfig(c.config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return provider.Status{State: provider.StateNeedsLogin, Detail: "run aws sso login" + profileFlag()}
	}
	return provider.Status{State: provider.StateReady, Detail: roleFromARN(awssdk.ToString(identity.Arn))}
}

func (c *AWS) Runner() harness.RunnerName { return harness.Fargate }

func (c *AWS) Supports(service provider.DBServiceName) bool {
	return Supports(provider.AWS, service)
}

// Config returns the AWS config the cloud's services build their clients from.
func (c *AWS) Config() awssdk.Config { return c.config }

// missingVariables, profileFlag and roleFromARN are unchanged from
// internal/providercfg/aws.go.

func init() {
	Register(provider.AWS, NewAWS)
}
