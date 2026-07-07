package aws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// awsProvider provisions Postgres on Amazon RDS.
type awsProvider struct {
	client *rds.Client
	cfg    awsConfig
	tel    *telemetry.Telemetry
}

type awsConfig struct {
	Region                string   // AWS_REGION
	Username              string   // AWS_RDS_USERNAME (default "dbtest")
	Database              string   // AWS_RDS_DATABASE (default "postgres")
	InstanceClassOverride string   // AWS_RDS_INSTANCE_CLASS (optional; forces a class, bypassing the sizing table)
	Public                bool     // AWS_RDS_PUBLIC (default true)
	SubnetGroup           string   // AWS_RDS_SUBNET_GROUP (VPC-internal path)
	SecurityGroupIDs      []string // AWS_RDS_SECURITY_GROUP_IDS (VPC-internal path)
}

// New creates an RDS-backed provider.
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
	if b, err := strconv.ParseBool(os.Getenv("AWS_RDS_PUBLIC")); err == nil {
		public = b
	}
	return awsConfig{
		Region:                os.Getenv("AWS_REGION"),
		Username:              envOr("AWS_RDS_USERNAME", "dbtest"),
		Database:              envOr("AWS_RDS_DATABASE", "postgres"),
		InstanceClassOverride: os.Getenv("AWS_RDS_INSTANCE_CLASS"),
		Public:                public,
		SubnetGroup:           os.Getenv("AWS_RDS_SUBNET_GROUP"),
		SecurityGroupIDs:      splitNonEmpty(os.Getenv("AWS_RDS_SECURITY_GROUP_IDS"), ","),
	}
}

// envOr returns the value of env var key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Provision creates an RDS instance sized from req
func (p *awsProvider) Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error) {
	start := time.Now()

	if req.VCPU < 0 || req.MemoryMiB < 0 || req.DiskGiB < 0 {
		return provider.ClusterInfo{}, fmt.Errorf("invalid provision request: negative resource (vcpu=%v memory_mib=%d disk_gib=%d)", req.VCPU, req.MemoryMiB, req.DiskGiB)
	}

	// A retry must land on the same instance a prior attempt created, so the
	// identifier and password are derived from the caller's pinned token/password.
	if token == "" {
		token = uuid.NewString()
	}
	if password == "" {
		password = uuid.NewString()
	}

	instanceID := p.cfg.Database + "-" + token
	instanceClass := resolveInstanceClass(req, p.cfg.InstanceClassOverride)

	input := &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String(instanceClass),
		AllocatedStorage:     aws.Int32(allocatedStorageGiB(req)),
		MasterUsername:       aws.String(p.cfg.Username),
		MasterUserPassword:   aws.String(password),
		PubliclyAccessible:   aws.Bool(p.cfg.Public),
		Tags: []rdstypes.Tag{
			{Key: aws.String("dbtest"), Value: aws.String("true")},
		},
	}

	if p.cfg.Database != "postgres" {
		input.DBName = aws.String(p.cfg.Database)
	}
	if req.PostgresVersion != "" {
		input.EngineVersion = aws.String(req.PostgresVersion)
	}
	if p.cfg.SubnetGroup != "" {
		input.DBSubnetGroupName = aws.String(p.cfg.SubnetGroup)
	}
	// TODO(public path): when Public and no SG is configured, detect the runner's
	// egress IP and ensure a security group with ingress 5432/<ip>/32 (needs an EC2
	// client). Until then, supply AWS_RDS_SECURITY_GROUP_IDS for reachability.
	if len(p.cfg.SecurityGroupIDs) > 0 {
		input.VpcSecurityGroupIds = p.cfg.SecurityGroupIDs
	}

	if _, err := p.client.CreateDBInstance(ctx, input); err != nil {
		// A retried attempt finds the instance a prior attempt already created.
		// Because the identity is pinned, that instance carries the same master
		// password we hold here, so we adopt it and proceed to wait for its
		// endpoint rather than orphaning it and creating a second one.
		var exists *rdstypes.DBInstanceAlreadyExistsFault
		if !errors.As(err, &exists) {
			return provider.ClusterInfo{}, fmt.Errorf("create db instance: %w", err)
		}
	}

	host, port, err := p.waitForEndpoint(ctx, instanceID)
	if err != nil {
		// The instance is already billing; delete it on a fresh ctx so a failed
		// provision doesn't orphan it.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		derr := p.Deprovision(cleanupCtx, instanceID)
		cancel()
		if derr != nil && p.tel != nil {
			p.tel.Logger.Warn("cleanup after failed provision did not delete instance",
				slog.String("instance_id", instanceID),
				slog.Any("error", derr),
			)
		}
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("aws").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("instance_id", instanceID),
			slog.String("instance_class", instanceClass),
			slog.String("endpoint", host),
			slog.Float64("vcpu", req.VCPU),
			slog.Int("memory_mib", req.MemoryMiB),
		)
	}

	return provider.ClusterInfo{
		ID:       instanceID,
		Target:   provider.PGTarget{Host: host, Port: port, Database: p.cfg.Database, User: p.cfg.Username},
		Password: password,
	}, nil
}

// waitForEndpoint polls DescribeDBInstances until the instance reports "available"
func (p *awsProvider) waitForEndpoint(ctx context.Context, instanceID string) (string, int, error) {
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		out, err := p.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
			DBInstanceIdentifier: aws.String(instanceID),
		})
		if err != nil {
			return "", 0, fmt.Errorf("describe db instance: %w", err)
		}
		if len(out.DBInstances) > 0 {
			inst := out.DBInstances[0]
			if aws.ToString(inst.DBInstanceStatus) == "available" && inst.Endpoint != nil && inst.Endpoint.Address != nil {
				return aws.ToString(inst.Endpoint.Address), int(aws.ToInt32(inst.Endpoint.Port)), nil
			}
		}
		time.Sleep(15 * time.Second)
	}
	return "", 0, fmt.Errorf("instance %s did not become available within 15m", instanceID)
}

// resolveInstanceClass maps the ProvisionRequest onto a concrete RDS instance
// class. AWS_RDS_INSTANCE_CLASS (passed through as override) wins verbatim;
// otherwise the smallest class satisfying both vCPU and memory is chosen, falling
// back to the largest in the table.
func resolveInstanceClass(req provider.ProvisionRequest, override string) string {
	if override != "" {
		return override
	}
	table := []struct {
		name      string
		vcpu      float64
		memoryMiB int
	}{
		{"db.t3.small", 2, 2048},
		{"db.t3.medium", 2, 4096},
		{"db.t3.large", 2, 8192},
		{"db.t3.xlarge", 4, 16384},
		{"db.t3.2xlarge", 8, 32768},
	}
	for _, c := range table {
		if c.vcpu >= req.VCPU && c.memoryMiB >= req.MemoryMiB {
			return c.name
		}
	}
	return table[len(table)-1].name
}

func allocatedStorageGiB(req provider.ProvisionRequest) int32 {
	if req.DiskGiB < 20 {
		return 20
	}
	return int32(req.DiskGiB)
}

// WaitForReady verifies the instance is actually accepting Postgres connections.
func (p *awsProvider) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.Connect(connCtx, cluster.Target.URL(cluster.Password))
		cancel()
		if err == nil {
			conn.Close(context.Background())
			if p.tel != nil {
				p.tel.Logger.Info("cluster is ready", slog.String("instance_id", cluster.ID))
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("cluster %s did not accept connections within 5m", cluster.ID)
}

func (p *awsProvider) Deprovision(ctx context.Context, clusterID string) error {
	var lastErr error
	for attempt := range 3 {
		_, lastErr = p.client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier:   aws.String(clusterID),
			SkipFinalSnapshot:      aws.Bool(true),
			DeleteAutomatedBackups: aws.Bool(true),
		})
		if lastErr == nil {
			break
		}
		var notFound *rdstypes.DBInstanceNotFoundFault
		if errors.As(lastErr, &notFound) {
			lastErr = nil // already gone — treat as success
			break
		}
		if p.tel != nil {
			p.tel.Logger.Warn("deprovision attempt failed",
				slog.Int("attempt", attempt+1),
				slog.String("instance_id", clusterID),
				slog.Any("error", lastErr),
			)
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return lastErr
	}
	if p.tel != nil {
		p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("aws").Inc()
		p.tel.Logger.Info("deprovisioned cluster", slog.String("instance_id", clusterID))
	}
	return nil
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
