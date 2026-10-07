package aws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// rdsProvider provisions Postgres on Amazon RDS.
type rdsProvider struct {
	client *rds.Client
	cfg    awsConfig
	tel    *telemetry.Telemetry
}

// NewRDS creates an RDS-backed provider.
func NewRDS(tel *telemetry.Telemetry) (*rdsProvider, error) {
	cfg := loadConfig()
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &rdsProvider{client: client, cfg: cfg, tel: tel}, nil
}

// Provision creates an RDS instance sized from req
func (p *rdsProvider) Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error) {
	start := time.Now()

	if req.VCPU < 0 || req.MemoryMiB < 0 || req.DiskGiB < 0 {
		return provider.ClusterInfo{}, fmt.Errorf("invalid provision request: negative resource (vcpu=%v memory_mib=%d disk_gib=%d)", req.VCPU, req.MemoryMiB, req.DiskGiB)
	}

	// A retry must land on the same instance a prior attempt created, so the
	// identifier and password are derived from the caller's pinned token/password.
	if token == "" {
		token = "dbtest-" + uuid.NewString()
	}
	if password == "" {
		password = uuid.NewString()
	}

	// The token is the identifier, so the caller can name the instance before
	// it exists.
	instanceID := token
	instanceClass := resolveInstanceClass(req, p.cfg.InstanceClassOverride)

	input := &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String(instanceClass),
		AllocatedStorage:     aws.Int32(allocatedStorageGiB(req)),
		MasterUsername:       aws.String(p.cfg.Username),
		MasterUserPassword:   aws.String(password),
		PubliclyAccessible:   aws.Bool(p.cfg.Public),
		MultiAZ:              aws.Bool(req.HighAvailability),
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
		// A retried attempt finds the instance a prior attempt already created instead of creatong a new one and orphan the previous instance.
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
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("rds").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("instance_id", instanceID),
			slog.String("instance_class", instanceClass),
			slog.String("endpoint", host),
			slog.Float64("vcpu", req.VCPU),
			slog.Int("memory_mib", req.MemoryMiB),
		)
	}

	target := provider.PGTarget{Host: host, Port: port, Database: p.cfg.Database, User: p.cfg.Username}
	return provider.ClusterInfo{
		ID:     instanceID,
		Target: target,
		// The RDS endpoint resolves the same from the worker and from a task in
		// the VPC, so there is no second address to hand out.
		Internal: target,
		Password: password,
	}, nil
}

// waitForEndpoint polls DescribeDBInstances until the instance reports "available"
func (p *rdsProvider) waitForEndpoint(ctx context.Context, instanceID string) (string, int, error) {
	deadline := time.Now().Add(25 * time.Minute)
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

// resolveInstanceClass maps the ProvisionRequest onto a concrete RDS instance class.
// By default is the smallest class returns.
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
func (p *rdsProvider) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
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

func (p *rdsProvider) Deprovision(ctx context.Context, clusterID string) error {
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
		p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("rds").Inc()
		p.tel.Logger.Info("deprovisioned cluster", slog.String("instance_id", clusterID))
	}
	return nil
}

// Supports reports which disruptions RDS can apply. There is no ungraceful kill:
// the API offers a reboot and, on Multi-AZ, a reboot that fails over.
func (p *rdsProvider) Supports(req provider.ProvisionRequest, disruption provider.Disruption) bool {
	switch disruption {
	case provider.Restart:
		return true
	case provider.Failover:
		return req.HighAvailability
	}
	return false
}

// Disrupt reboots the instance and returns once it is available again, promoting
// the standby when asked for a failover.
func (p *rdsProvider) Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error) {
	var failover bool
	switch disruption {
	case provider.Restart:
	case provider.Failover:
		failover = true
	default:
		return provider.ClusterInfo{}, fmt.Errorf("aws cannot %s an instance", disruption)
	}

	var zoneBefore string
	if failover {
		inst, err := p.describe(ctx, cluster.ID)
		if err != nil {
			return provider.ClusterInfo{}, err
		}
		zoneBefore = aws.ToString(inst.AvailabilityZone)
	}

	start := time.Now()
	if _, err := p.client.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{
		DBInstanceIdentifier: aws.String(cluster.ID),
		ForceFailover:        aws.Bool(failover),
	}); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("reboot db instance: %w", err)
	}

	if err := p.waitForReboot(ctx, cluster.ID, zoneBefore); err != nil {
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Logger.Info("disrupted cluster",
			slog.String("disruption", string(disruption)),
			slog.String("instance_id", cluster.ID),
			slog.Duration("took", time.Since(start)),
		)
	}
	return cluster, nil
}

// describe returns the instance's current state.
func (p *rdsProvider) describe(ctx context.Context, instanceID string) (rdstypes.DBInstance, error) {
	out, err := p.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(instanceID),
	})
	if err != nil {
		return rdstypes.DBInstance{}, fmt.Errorf("describe db instance %s: %w", instanceID, err)
	}
	if len(out.DBInstances) == 0 {
		return rdstypes.DBInstance{}, fmt.Errorf("instance %s not found", instanceID)
	}
	return out.DBInstances[0], nil
}

// waitForReboot waits for the instance to leave "available" and come back.
// for failover needs to point to a different AZ wher a former standby (primary after failover) instance resides
func (p *rdsProvider) waitForReboot(ctx context.Context, instanceID, zoneBefore string) error {
	// Guards againist false positive and wait until instance leaves "available" status
	left, err := p.waitForStatus(ctx, instanceID, 2*time.Minute, func(inst rdstypes.DBInstance) bool {
		return aws.ToString(inst.DBInstanceStatus) != "available"
	})
	if err != nil {
		return err
	}
	// A reboot takes minutes and the poll is every two seconds, so never seeing the
	// transition means the reboot did not take.
	if !left {
		return fmt.Errorf("instance %s never left available after a reboot request", instanceID)
	}

	// Traffic is restored once the instance reports available again.
	back, err := p.waitForStatus(ctx, instanceID, 15*time.Minute, func(inst rdstypes.DBInstance) bool {
		return aws.ToString(inst.DBInstanceStatus) == "available"
	})
	if err != nil {
		return err
	}
	if !back {
		return fmt.Errorf("instance %s was not available again within 15m", instanceID)
	}

	if zoneBefore == "" {
		return nil
	}

	// The zone swap is the last thing a failover updates.
	synced, err := p.waitForStatus(ctx, instanceID, 15*time.Minute, func(inst rdstypes.DBInstance) bool {
		return aws.ToString(inst.DBInstanceStatus) == "available" &&
			aws.ToString(inst.AvailabilityZone) != zoneBefore
	})
	if err != nil {
		return err
	}
	if !synced {
		return fmt.Errorf("instance %s still reports its primary in %s 15m after a failover", instanceID, zoneBefore)
	}
	return nil
}

// waitForStatus polls DescribeDBInstances until want returns true. It returns
// false if the timeout passes first, and an error only if the poll itself fails.
func (p *rdsProvider) waitForStatus(ctx context.Context, instanceID string, timeout time.Duration, want func(rdstypes.DBInstance) bool) (bool, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	for {
		out, err := p.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
			DBInstanceIdentifier: aws.String(instanceID),
		})
		if err != nil {
			if parent.Err() != nil {
				return false, parent.Err()
			}
			// The deadline expiring mid-call is the timeout, not a failed poll.
			if ctx.Err() != nil {
				return false, nil
			}
			if p.tel != nil {
				p.tel.Logger.Error("describe db instance failed",
					slog.String("instance_id", instanceID),
					slog.Any("error", err),
				)
			}
			return false, fmt.Errorf("describe db instance %s: %w", instanceID, err)
		}
		if len(out.DBInstances) > 0 && want(out.DBInstances[0]) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return false, parent.Err()
			}
			return false, nil
		case <-time.After(2 * time.Second):
		}
	}
}

// newProvider adapts NewRDS to the registry constructor signature.
func newProvider(tel *telemetry.Telemetry) (provider.Provider, error) {
	return NewRDS(tel)
}

func init() {
	provider.Register(provider.RDS, newProvider)
}

// Compile-time assertion that rdsProvider satisfies the core Provider contract.
var _ provider.Provider = (*rdsProvider)(nil)
