package aws

import (
	"context"
	"fmt"
	"os"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
)

// awsProvider provisions Postgres on Amazon RDS. It satisfies provider.Provider
// and is the cloud counterpart to the Docker provider: instead of a container it
// drives a single-instance RDS database through the AWS SDK.
type awsProvider struct {
	client *rds.Client
	cfg    awsConfig
	tel    *telemetry.Telemetry
}

// awsConfig holds the resolved, env-driven settings for the provider. Networking
// fields are only consulted when Public is false (the VPC-internal path); in the
// default public path the provider rides the account's default VPC and scopes a
// security group to the runner's egress IP.
type awsConfig struct {
	Region           string   // AWS_REGION
	InstanceClass    string   // AWS_RDS_INSTANCE_CLASS (optional; overrides sizing table)
	EngineVersion    string   // AWS_RDS_ENGINE_VERSION (optional; RDS default if empty)
	Public           bool     // AWS_RDS_PUBLIC (default true)
	SubnetGroup      string   // AWS_RDS_SUBNET_GROUP (VPC-internal path)
	SecurityGroupIDs []string // AWS_RDS_SECURITY_GROUP_IDS (VPC-internal path)
}

// New creates an RDS-backed provider. Credentials and region are resolved through
// the standard AWS credential chain (env vars, shared profile, or IAM role) via
// config.LoadDefaultConfig.
func New(tel *telemetry.Telemetry) (*awsProvider, error) {
	cfg := loadConfig()

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if cfg.Region != "" {
		awsCfg.Region = cfg.Region
	}

	return &awsProvider{
		client: rds.NewFromConfig(awsCfg),
		cfg:    cfg,
		tel:    tel,
	}, nil
}

// loadConfig reads the AWS_* / AWS_RDS_* environment into an awsConfig.
func loadConfig() awsConfig {
	public := true
	if v := os.Getenv("AWS_RDS_PUBLIC"); v == "false" {
		public = false
	}
	return awsConfig{
		Region:           os.Getenv("AWS_REGION"),
		InstanceClass:    os.Getenv("AWS_RDS_INSTANCE_CLASS"),
		EngineVersion:    os.Getenv("AWS_RDS_ENGINE_VERSION"),
		Public:           public,
		SubnetGroup:      os.Getenv("AWS_RDS_SUBNET_GROUP"),
		SecurityGroupIDs: splitNonEmpty(os.Getenv("AWS_RDS_SECURITY_GROUP_IDS"), ","),
	}
}

// Provision creates an RDS instance sized from req, then waits for its endpoint
// to be assigned so the returned ClusterInfo carries a usable DSN. (RDS does not
// publish the endpoint until the instance is "available", so the create and the
// wait are kept together here — see docs/cloud-provider.md, Decision 3.)
func (p *awsProvider) Provision(ctx context.Context, req provider.ProvisionRequest) (provider.ClusterInfo, error) {
	// TODO: resolve instance class + AllocatedStorage from req (provider/aws/sizing.go).
	// TODO: generate instance identifier ("dbtest-<uuid>") and a random master password.
	// TODO: if p.cfg.Public, detect egress IP and ensure a security group (ingress 5432/<ip>/32);
	//       else use p.cfg.SubnetGroup + p.cfg.SecurityGroupIDs.
	// TODO: client.CreateDBInstance(...) with engine "postgres", tags {dbtest=true, scenario=...}.
	// TODO: wait for status "available" + endpoint, then build the DSN.
	// TODO: observe p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("aws").
	return provider.ClusterInfo{}, fmt.Errorf("aws Provision: not implemented")
}

// WaitForReady verifies the instance is actually accepting Postgres connections.
// Provision already blocks on RDS reporting "available"; this performs the same
// pgx connectivity probe the Docker provider does so callers get a uniform signal.
func (p *awsProvider) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	// TODO: poll pgx.Connect(cluster.DSN) until success or a generous deadline (~15m for cold RDS).
	return fmt.Errorf("aws WaitForReady: not implemented")
}

// Deprovision deletes the RDS instance, skipping the final snapshot so teardown
// is fast and leaves nothing billable behind.
func (p *awsProvider) Deprovision(ctx context.Context, clusterID string) error {
	// TODO: client.DeleteDBInstance(SkipFinalSnapshot=true, DeleteAutomatedBackups=true) with retry.
	// TODO: increment p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("aws").
	return fmt.Errorf("aws Deprovision: not implemented")
}

// FailureInjector (KillProcess) is intentionally not implemented yet. The natural
// RDS analogue is RebootDBInstance(ForceFailover=true); add it here and restore
// the compile-time assertion below when failure-injection scenarios target AWS.
//
// var _ provider.FailureInjector = (*awsProvider)(nil)

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

// newProvider adapts New to the registry constructor signature.
func newProvider(tel *telemetry.Telemetry) (provider.Provider, error) {
	return New(tel)
}

func init() {
	provider.Register(provider.AWS, newProvider)
}

// Compile-time assertion that awsProvider satisfies the core Provider contract.
var _ provider.Provider = (*awsProvider)(nil)
