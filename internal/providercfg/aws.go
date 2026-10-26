package providercfg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// CheckAWS reports whether the RDS and Aurora providers can run: the .env
// variables are set and the credentials are valid.
func CheckAWS(ctx context.Context) Status {
	if missing := missingAWSVariables(); len(missing) > 0 {
		count := fmt.Sprintf("%d variables", len(missing))
		if len(missing) == 1 {
			count = "1 variable"
		}
		return Status{State: StateMisconfigured, Detail: count + " missing: run source .env (" + strings.Join(missing, ", ") + ")"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return Status{State: StateMisconfigured, Detail: err.Error()}
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Status{State: StateNeedsLogin, Detail: "run aws sso login" + profileFlag()}
	}
	return Status{State: StateReady, Detail: roleFromARN(aws.ToString(identity.Arn))}
}

// missingAWSVariables returns the variables from .env that the RDS and Aurora
// providers and the Fargate runner need and that are not set.
func missingAWSVariables() []string {
	var missing []string
	for _, name := range []string{
		"AWS_REGION",
		"AWS_RDS_SECURITY_GROUP_IDS",
		"AWS_ECS_CLUSTER",
		"AWS_ECS_SUBNET_IDS",
		"AWS_ECS_SECURITY_GROUP_IDS",
		"AWS_ECS_LOG_GROUP",
	} {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

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
