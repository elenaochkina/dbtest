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

// AWS is the AWS cloud, with the config its services share.
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
	if status, missing := missingVariables(
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

func (c *AWS) Runner() harness.RunnerName { return harness.Fargate }

func (c *AWS) Supports(service provider.DBServiceName) bool {
	return Supports(provider.AWS, service)
}

// Config returns the AWS config the cloud's services build their clients from.
func (c *AWS) Config() awssdk.Config { return c.config }

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
	Register(provider.AWS, NewAWS)
}

var _ Cloud = (*AWS)(nil)
