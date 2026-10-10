package aws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// auroraProvider provisions Aurora PostgreSQL clusters.
type auroraProvider struct {
	client *rds.Client
	cfg    awsConfig
	tel    *telemetry.Telemetry
}

// NewAurora creates an Aurora-backed provider.
func NewAurora(tel *telemetry.Telemetry) (*auroraProvider, error) {
	cfg := loadConfig()
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &auroraProvider{client: client, cfg: cfg, tel: tel}, nil
}

// Provision creates a cluster with a writer and, for high availability, one reader.
func (p *auroraProvider) Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error) {
	start := time.Now()

	if req.VCPU < 0 || req.MemoryMiB < 0 {
		return provider.ClusterInfo{}, fmt.Errorf("invalid provision request: negative resource (vcpu=%v memory_mib=%d)", req.VCPU, req.MemoryMiB)
	}
	if token == "" {
		token = "dbtest-" + uuid.NewString()
	}
	if password == "" {
		password = uuid.NewString()
	}

	clusterID := token
	instanceClass := resolveAuroraClass(req, p.cfg.AuroraInstanceClass)
	instanceIDs := []string{clusterID + "-1"}
	if req.HighAvailability {
		instanceIDs = append(instanceIDs, clusterID+"-2")
	}

	host, port, err := p.create(ctx, req, clusterID, instanceIDs, instanceClass, password)
	if err != nil {
		// The cluster may already be billing; delete it on a fresh ctx.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		derr := p.Deprovision(cleanupCtx, clusterID)
		cancel()
		if derr != nil && p.tel != nil {
			p.tel.Logger.Warn("cleanup after failed provision did not delete cluster",
				slog.String("cluster_id", clusterID),
				slog.Any("error", derr),
			)
		}
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("aurora").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("cluster_id", clusterID),
			slog.String("instance_class", instanceClass),
			slog.Int("instances", len(instanceIDs)),
			slog.String("endpoint", host),
		)
	}

	target := provider.PGTarget{Host: host, Port: port, Database: p.cfg.Database, User: p.cfg.Username}
	return provider.ClusterInfo{
		ID:       clusterID,
		Target:   target,
		Internal: target,
		Password: password,
	}, nil
}

// create creates the cluster and its instances, then waits for the writer endpoint.
// A retried attempt finds what a prior attempt created.
func (p *auroraProvider) create(ctx context.Context, req provider.ProvisionRequest, clusterID string, instanceIDs []string, instanceClass, password string) (string, int, error) {
	version, err := p.engineVersion(ctx, req.PostgresVersion)
	if err != nil {
		return "", 0, err
	}

	tags := []rdstypes.Tag{{Key: aws.String("dbtest"), Value: aws.String("true")}}
	input := &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-postgresql"),
		EngineVersion:       version,
		MasterUsername:      aws.String(p.cfg.Username),
		MasterUserPassword:  aws.String(password),
		Tags:                tags,
	}
	if p.cfg.Database != "postgres" {
		input.DatabaseName = aws.String(p.cfg.Database)
	}
	if p.cfg.SubnetGroup != "" {
		input.DBSubnetGroupName = aws.String(p.cfg.SubnetGroup)
	}
	if len(p.cfg.SecurityGroupIDs) > 0 {
		input.VpcSecurityGroupIds = p.cfg.SecurityGroupIDs
	}
	if _, err := p.client.CreateDBCluster(ctx, input); err != nil {
		var exists *rdstypes.DBClusterAlreadyExistsFault
		if !errors.As(err, &exists) {
			return "", 0, fmt.Errorf("create db cluster: %w", err)
		}
	}

	// The first instance becomes the writer.
	for _, id := range instanceIDs {
		_, err := p.client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
			DBClusterIdentifier:  aws.String(clusterID),
			Engine:               aws.String("aurora-postgresql"),
			DBInstanceClass:      aws.String(instanceClass),
			PubliclyAccessible:   aws.Bool(p.cfg.Public),
			Tags:                 tags,
		})
		if err != nil {
			var exists *rdstypes.DBInstanceAlreadyExistsFault
			if !errors.As(err, &exists) {
				return "", 0, fmt.Errorf("create db instance %s: %w", id, err)
			}
		}
	}

	return p.waitForEndpoint(ctx, clusterID, len(instanceIDs))
}

// engineVersion resolves a major version such as "16" to Aurora's default minor.
// A full version is used as given, and an empty one leaves the choice to AWS.
func (p *auroraProvider) engineVersion(ctx context.Context, version string) (*string, error) {
	if version == "" {
		return nil, nil
	}
	if strings.Contains(version, ".") {
		return aws.String(version), nil
	}
	out, err := p.client.DescribeDBEngineVersions(ctx, &rds.DescribeDBEngineVersionsInput{
		Engine:        aws.String("aurora-postgresql"),
		EngineVersion: aws.String(version),
		DefaultOnly:   aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("describe aurora-postgresql versions: %w", err)
	}
	if len(out.DBEngineVersions) == 0 {
		return nil, fmt.Errorf("no aurora-postgresql version for %q", version)
	}
	return out.DBEngineVersions[0].EngineVersion, nil
}

// waitForEndpoint waits until the cluster and all of its instances are available,
// then returns the writer endpoint.
func (p *auroraProvider) waitForEndpoint(ctx context.Context, clusterID string, instances int) (string, int, error) {
	var host string
	var port int
	ready, err := poll(ctx, 20*time.Minute, 15*time.Second, func(ctx context.Context) (bool, error) {
		cluster, err := p.describeCluster(ctx, clusterID)
		if err != nil {
			return false, err
		}
		if aws.ToString(cluster.Status) != "available" || cluster.Endpoint == nil {
			return false, nil
		}
		out, err := p.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
			Filters: []rdstypes.Filter{{Name: aws.String("db-cluster-id"), Values: []string{clusterID}}},
		})
		if err != nil {
			return false, fmt.Errorf("describe instances of cluster %s: %w", clusterID, err)
		}
		available := 0
		for _, inst := range out.DBInstances {
			if aws.ToString(inst.DBInstanceStatus) == "available" {
				available++
			}
		}
		if available < instances {
			return false, nil
		}
		host, port = aws.ToString(cluster.Endpoint), int(aws.ToInt32(cluster.Port))
		return true, nil
	})
	if err != nil {
		return "", 0, err
	}
	if !ready {
		return "", 0, fmt.Errorf("cluster %s did not become available within 20m", clusterID)
	}
	return host, port, nil
}

// resolveAuroraClass maps the ProvisionRequest onto an Aurora instance class.
// The table starts at db.r6g.large because Aurora's smaller classes are burstable.
func resolveAuroraClass(req provider.ProvisionRequest, override string) string {
	if override != "" {
		return override
	}
	table := []struct {
		name      string
		vcpu      float64
		memoryMiB int
	}{
		{"db.r6g.large", 2, 16384},
		{"db.r6g.xlarge", 4, 32768},
		{"db.r6g.2xlarge", 8, 65536},
		{"db.r6g.4xlarge", 16, 131072},
	}
	for _, c := range table {
		if c.vcpu >= req.VCPU && c.memoryMiB >= req.MemoryMiB {
			return c.name
		}
	}
	return table[len(table)-1].name
}

// WaitForReady verifies the writer endpoint is accepting Postgres connections.
func (p *auroraProvider) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
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
				p.tel.Logger.Info("cluster is ready", slog.String("cluster_id", cluster.ID))
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("cluster %s did not accept connections within 5m", cluster.ID)
}

// Deprovision deletes the cluster's instances, then the cluster. A missing
// cluster counts as success.
func (p *auroraProvider) Deprovision(ctx context.Context, clusterID string) error {
	cluster, err := p.describeCluster(ctx, clusterID)
	var clusterGone *rdstypes.DBClusterNotFoundFault
	if errors.As(err, &clusterGone) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, m := range cluster.DBClusterMembers {
		id := aws.ToString(m.DBInstanceIdentifier)
		if _, err := p.client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
		}); err != nil {
			var gone *rdstypes.DBInstanceNotFoundFault
			var busy *rdstypes.InvalidDBInstanceStateFault
			if !errors.As(err, &gone) && !errors.As(err, &busy) {
				return fmt.Errorf("delete db instance %s: %w", id, err)
			}
		}
	}

	// AWS refuses to delete the cluster until every member is deleting.
	for {
		_, err := p.client.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier:    aws.String(clusterID),
			SkipFinalSnapshot:      aws.Bool(true),
			DeleteAutomatedBackups: aws.Bool(true),
		})
		if err == nil || errors.As(err, &clusterGone) {
			break
		}
		var busy *rdstypes.InvalidDBClusterStateFault
		if !errors.As(err, &busy) {
			return fmt.Errorf("delete db cluster %s: %w", clusterID, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("delete db cluster %s: %w", clusterID, ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}

	if p.tel != nil {
		p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("aurora").Inc()
		p.tel.Logger.Info("deprovisioned cluster", slog.String("cluster_id", clusterID))
	}
	return nil
}

// Supports reports which disruptions Aurora can apply. A failover needs a
// reader to promote.
func (p *auroraProvider) Supports(req provider.ProvisionRequest, disruption provider.Disruption) bool {
	switch disruption {
	case provider.Restart, provider.Crash:
		return true
	case provider.Failover:
		return req.HighAvailability
	}
	return false
}

// Disrupt reboots the writer, crashes it, or promotes the reader, and returns
// once the cluster has settled. The writer endpoint follows the promotion, so
// the returned ClusterInfo is unchanged.
func (p *auroraProvider) Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error) {
	before, err := p.describeCluster(ctx, cluster.ID)
	if err != nil {
		return provider.ClusterInfo{}, err
	}
	writer, reader := roles(before)
	start := time.Now()

	switch disruption {
	case provider.Restart:
		if writer == "" {
			return provider.ClusterInfo{}, fmt.Errorf("cluster %s has no writer", cluster.ID)
		}
		if _, err := p.client.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{
			DBInstanceIdentifier: aws.String(writer),
		}); err != nil {
			return provider.ClusterInfo{}, fmt.Errorf("reboot db instance %s: %w", writer, err)
		}
		if err := p.waitForReboot(ctx, writer); err != nil {
			return provider.ClusterInfo{}, err
		}

	case provider.Crash:
		if err := injectCrash(ctx, cluster); err != nil {
			return provider.ClusterInfo{}, err
		}
		// Aurora restarts the writer in place; the endpoint answering again is the settle.
		if err := p.WaitForReady(ctx, cluster); err != nil {
			return provider.ClusterInfo{}, err
		}
		after, err := p.describeCluster(ctx, cluster.ID)
		if err != nil {
			return provider.ClusterInfo{}, err
		}
		if w, _ := roles(after); w != writer && p.tel != nil {
			p.tel.Logger.Warn("crash moved the writer",
				slog.String("cluster_id", cluster.ID),
				slog.String("before", writer),
				slog.String("after", w),
			)
		}

	case provider.Failover:
		if reader == "" {
			return provider.ClusterInfo{}, fmt.Errorf("cluster %s has no reader to fail over to", cluster.ID)
		}
		if _, err := p.client.FailoverDBCluster(ctx, &rds.FailoverDBClusterInput{
			DBClusterIdentifier:        aws.String(cluster.ID),
			TargetDBInstanceIdentifier: aws.String(reader),
		}); err != nil {
			return provider.ClusterInfo{}, fmt.Errorf("failover db cluster %s: %w", cluster.ID, err)
		}
		promoted, err := poll(ctx, 15*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
			c, err := p.describeCluster(ctx, cluster.ID)
			if err != nil {
				return false, err
			}
			w, _ := roles(c)
			return w == reader && aws.ToString(c.Status) == "available", nil
		})
		if err != nil {
			return provider.ClusterInfo{}, err
		}
		if !promoted {
			return provider.ClusterInfo{}, fmt.Errorf("cluster %s did not promote %s within 15m", cluster.ID, reader)
		}

	default:
		return provider.ClusterInfo{}, fmt.Errorf("aurora cannot %s a cluster", disruption)
	}

	if p.tel != nil {
		p.tel.Logger.Info("disrupted cluster",
			slog.String("disruption", string(disruption)),
			slog.String("cluster_id", cluster.ID),
			slog.Duration("took", time.Since(start)),
		)
	}
	return cluster, nil
}

// injectCrash crashes the writer's Postgres with Aurora's fault injection query.
// The query cannot return normally: the server dies under it, so a lost
// connection or a crash-shutdown error is success.
func injectCrash(ctx context.Context, cluster provider.ClusterInfo) error {
	connCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(connCtx, cluster.Target.URL(cluster.Password))
	if err != nil {
		return fmt.Errorf("connect to writer of %s: %w", cluster.ID, err)
	}
	defer conn.Close(context.Background())

	_, err = conn.Exec(connCtx, "SELECT aurora_inject_crash('instance')")
	if err == nil {
		return fmt.Errorf("aurora_inject_crash returned without crashing %s", cluster.ID)
	}
	if connCtx.Err() != nil {
		return fmt.Errorf("aurora_inject_crash on %s timed out: %w", cluster.ID, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && !strings.HasPrefix(pgErr.Code, "57P") {
		return fmt.Errorf("aurora_inject_crash on %s: %w", cluster.ID, err)
	}
	return nil
}

// waitForReboot waits for the instance to leave "available" and come back.
func (p *auroraProvider) waitForReboot(ctx context.Context, instanceID string) error {
	status := func(want func(string) bool) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			out, err := p.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
				DBInstanceIdentifier: aws.String(instanceID),
			})
			if err != nil {
				return false, fmt.Errorf("describe db instance %s: %w", instanceID, err)
			}
			return len(out.DBInstances) > 0 && want(aws.ToString(out.DBInstances[0].DBInstanceStatus)), nil
		}
	}

	left, err := poll(ctx, 2*time.Minute, 2*time.Second, status(func(s string) bool { return s != "available" }))
	if err != nil {
		return err
	}
	if !left {
		return fmt.Errorf("instance %s never left available after a reboot request", instanceID)
	}
	back, err := poll(ctx, 15*time.Minute, 2*time.Second, status(func(s string) bool { return s == "available" }))
	if err != nil {
		return err
	}
	if !back {
		return fmt.Errorf("instance %s was not available again within 15m", instanceID)
	}
	return nil
}

// describeCluster returns the cluster's current state.
func (p *auroraProvider) describeCluster(ctx context.Context, clusterID string) (rdstypes.DBCluster, error) {
	out, err := p.client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	if err != nil {
		return rdstypes.DBCluster{}, fmt.Errorf("describe db cluster %s: %w", clusterID, err)
	}
	if len(out.DBClusters) == 0 {
		return rdstypes.DBCluster{}, fmt.Errorf("cluster %s not found", clusterID)
	}
	return out.DBClusters[0], nil
}

// roles returns the cluster's writer and its first reader.
func roles(c rdstypes.DBCluster) (writer, reader string) {
	for _, m := range c.DBClusterMembers {
		id := aws.ToString(m.DBInstanceIdentifier)
		if aws.ToBool(m.IsClusterWriter) {
			writer = id
		} else if reader == "" {
			reader = id
		}
	}
	return writer, reader
}

// poll calls check every interval until it returns true. It returns false if
// the timeout passes first, and an error only if a check fails.
func poll(ctx context.Context, timeout, interval time.Duration, check func(context.Context) (bool, error)) (bool, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	for {
		ok, err := check(ctx)
		if err != nil {
			if parent.Err() != nil {
				return false, parent.Err()
			}
			// The deadline expiring mid-call is the timeout, not a failed check.
			if ctx.Err() != nil {
				return false, nil
			}
			return false, err
		}
		if ok {
			return true, nil
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return false, parent.Err()
			}
			return false, nil
		case <-time.After(interval):
		}
	}
}

// newAuroraProvider adapts NewAurora to the registry constructor signature.
func newAuroraProvider(tel *telemetry.Telemetry) (provider.Provider, error) {
	return NewAurora(tel)
}

func init() {
	provider.Register(provider.Aurora, newAuroraProvider)
}

// Compile-time assertion that auroraProvider satisfies the Provider contract.
var _ provider.Provider = (*auroraProvider)(nil)
