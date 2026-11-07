// Package aws is the AWS cloud: its config, readiness check and runners.
package aws

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
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
)

// Cloud is the AWS cloud, with the config its services share.
type Cloud struct {
	config awssdk.Config
}

// New loads the AWS config from the environment and the shared config files.
func New() (cloud.Cloud, error) {
	config, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &Cloud{config: config}, nil
}

func (c *Cloud) Name() provider.CloudName { return provider.AWS }

// Check reports whether the .env variables are set and the credentials are valid.
func (c *Cloud) Check(ctx context.Context) provider.Status {
	if status, missing := cloud.MissingVariables(
		"AWS_REGION",
		"AWS_RDS_SECURITY_GROUP_IDS",
		"AWS_ECS_CLUSTER",
		"AWS_ECS_SUBNET_IDS",
		"AWS_ECS_SECURITY_GROUP_IDS",
		"AWS_ECS_LOG_GROUP",
	); missing {
		return status
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := sts.NewFromConfig(c.config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return provider.Status{State: provider.StateNeedsLogin, Detail: "run aws sso login" + profileFlag()}
	}
	return provider.Status{State: provider.StateReady, Detail: roleFromARN(awssdk.ToString(identity.Arn))}
}

func (c *Cloud) Runners() []harness.RunnerName { return []harness.RunnerName{harness.Fargate} }

// NewRunner builds the Fargate runner on the cloud's config.
func (c *Cloud) NewRunner(name harness.RunnerName, tel *telemetry.Telemetry) (harness.Runner, error) {
	if name != harness.Fargate {
		return nil, cloud.UnknownRunner(c, name)
	}
	return newFargateRunner(c.config, tel)
}

// Images returns the probe and bench task definitions from .env, defaulting to
// the families terraform/aws creates.
func (c *Cloud) Images(harness.RunnerName) harness.Images {
	return harness.Images{
		Probe: cloud.EnvOr("AWS_ECS_PROBE_TASK_DEFINITION", "dbtest-probe"),
		Bench: cloud.EnvOr("AWS_ECS_BENCH_TASK_DEFINITION", "dbtest-bench"),
	}
}

func (c *Cloud) Supports(service provider.DBServiceName) bool {
	return cloud.Supports(provider.AWS, service)
}

// Config returns the AWS config the cloud's services build their clients from.
func (c *Cloud) Config() awssdk.Config { return c.config }

// profileFlag returns " --profile <name>" when AWS_PROFILE is set.
func profileFlag() string {
	if profile := os.Getenv("AWS_PROFILE"); profile != "" {
		return " --profile " + profile
	}
	return ""
}

// roleFromARN returns "role <name>" for an assumed-role ARN, otherwise the ARN.
func roleFromARN(arn string) string {
	_, rest, found := strings.Cut(arn, ":assumed-role/")
	if !found {
		return arn
	}
	role, _, _ := strings.Cut(rest, "/")
	return "role " + role
}

func init() {
	cloud.Register(provider.AWS, New)
}

var _ cloud.Cloud = (*Cloud)(nil)
