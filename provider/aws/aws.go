package aws

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// awsConfig holds the settings shared by the RDS and Aurora providers.
type awsConfig struct {
	Region                string
	Username              string
	Database              string
	InstanceClassOverride string
	AuroraInstanceClass   string
	Public                bool
	SubnetGroup           string
	SecurityGroupIDs      []string
}

// loadConfig reads the AWS_* / AWS_RDS_* environment into an awsConfig.
func loadConfig() awsConfig {
	public := true
	if b, err := strconv.ParseBool(os.Getenv("AWS_RDS_PUBLIC")); err == nil {
		public = b
	}
	return awsConfig{
		Region:                os.Getenv("AWS_REGION"),
		Username:              envOr("AWS_RDS_USERNAME", "dbtest"),
		Database:              envOr("AWS_RDS_DATABASE", "postgres"),
		InstanceClassOverride: os.Getenv("AWS_RDS_INSTANCE_CLASS"),
		AuroraInstanceClass:   os.Getenv("AWS_AURORA_INSTANCE_CLASS"),
		Public:                public,
		SubnetGroup:           os.Getenv("AWS_RDS_SUBNET_GROUP"),
		SecurityGroupIDs:      splitNonEmpty(os.Getenv("AWS_RDS_SECURITY_GROUP_IDS"), ","),
	}
}

// newClient builds an RDS API client for the configured region.
func newClient(cfg awsConfig) (*rds.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if cfg.Region != "" {
		awsCfg.Region = cfg.Region
	}
	return rds.NewFromConfig(awsCfg), nil
}

// envOr returns the value of env var key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitNonEmpty splits s on sep, dropping empty fields. Returns nil for "".
func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, sep) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
