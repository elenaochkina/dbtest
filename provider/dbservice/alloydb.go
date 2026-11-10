package dbservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	gcpcloud "github.com/elenaochkina/dbtest/provider/cloud/gcp"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	alloydb "google.golang.org/api/alloydb/v1"
	"google.golang.org/api/googleapi"
)

// alloydbservice provisions Postgres on AlloyDB: one cluster with one primary instance.
type alloydbservice struct {
	client  *alloydb.Service
	project string
	region  string
	cfg     alloyDBConfig
	tel     *telemetry.Telemetry
}

// alloyDBRegistration registers AlloyDB; Build returns the service that provisions it.
type alloyDBRegistration struct{}

func (alloyDBRegistration) Name() provider.DBServiceName { return provider.AlloyDB }

func (alloyDBRegistration) Clouds() []provider.CloudName { return []provider.CloudName{provider.GCP} }

func (alloyDBRegistration) SupportsHighAvailability() bool { return true }

// Build builds AlloyDB in the GCP cloud.
func (alloyDBRegistration) Build(c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error) {
	gcpCloud, ok := c.(*gcpcloud.GCPCloud)
	if !ok {
		return nil, fmt.Errorf("alloydb needs the gcp cloud, got %s", c.Name())
	}
	if gcpCloud.Project() == "" {
		return nil, errors.New("GCP_PROJECT is not set")
	}
	cfg := loadAlloyDBConfig()
	if len(cfg.authorizedNetworks) == 0 {
		return nil, errors.New("GCP_AUTHORIZED_NETWORKS is not set")
	}
	client, err := alloydb.NewService(context.Background())
	if err != nil {
		return nil, fmt.Errorf("create alloydb client: %w", err)
	}
	return &alloydbservice{client: client, project: gcpCloud.Project(), region: gcpCloud.Region(), cfg: cfg, tel: tel}, nil
}

// alloyDBConfig holds the AlloyDB settings from the GCP_* environment.
type alloyDBConfig struct {
	network            string
	authorizedNetworks []string
}

// loadAlloyDBConfig reads GCP_ALLOYDB_NETWORK (default dbtest) and GCP_AUTHORIZED_NETWORKS into an alloyDBConfig.
func loadAlloyDBConfig() alloyDBConfig {
	return alloyDBConfig{
		network:            cloud.EnvOr("GCP_ALLOYDB_NETWORK", "dbtest"),
		authorizedNetworks: splitNonEmpty(os.Getenv("GCP_AUTHORIZED_NETWORKS"), ","),
	}
}

// Provision creates a cluster and its primary instance sized from req and returns the instance's public IP.
func (p *alloydbservice) Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error) {
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
	cpuCount := alloyDBCPUCount(req)

	host, err := p.create(ctx, req, clusterID, cpuCount, password)
	if err != nil {
		// The cluster may already be billing; delete it on a fresh ctx.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		derr := p.Deprovision(cleanupCtx, clusterID)
		cancel()
		if derr != nil && p.tel != nil {
			p.tel.Logger.Warn("cleanup after failed provision did not delete cluster",
				slog.String("cluster", clusterID),
				slog.Any("error", derr),
			)
		}
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("alloydb").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("cluster", clusterID),
			slog.Int64("cpu_count", cpuCount),
			slog.String("endpoint", host),
			slog.Float64("vcpu", req.VCPU),
			slog.Int("memory_mib", req.MemoryMiB),
		)
	}

	target := provider.PGTarget{Host: host, Port: 5432, Database: "postgres", User: "postgres"}
	return provider.ClusterInfo{
		ID:       clusterID,
		Target:   target,
		Internal: target,
		Password: password,
	}, nil
}

// create creates the cluster, then its primary instance, waits until the instance is READY,
// and returns its public IP. A retried attempt finds what a prior attempt created.
func (p *alloydbservice) create(ctx context.Context, req provider.ProvisionRequest, clusterID string, cpuCount int64, password string) (string, error) {
	version, err := gcpPostgresVersion(req.PostgresVersion)
	if err != nil {
		return "", err
	}

	cluster := &alloydb.Cluster{
		DatabaseVersion: version,
		InitialUser:     &alloydb.UserPassword{User: "postgres", Password: password},
		NetworkConfig:   &alloydb.NetworkConfig{Network: fmt.Sprintf("projects/%s/global/networks/%s", p.project, p.cfg.network)},
		Labels:          map[string]string{"dbtest": "true"},
	}
	op, err := p.client.Projects.Locations.Clusters.Create(p.location(), cluster).ClusterId(clusterID).Context(ctx).Do()
	switch {
	case err == nil:
		if err := p.waitForOperation(ctx, op.Name, 30*time.Minute); err != nil {
			return "", fmt.Errorf("create cluster %s: %w", clusterID, err)
		}
	case hasCode(err, http.StatusConflict):
		// A prior attempt already created the cluster.
	default:
		return "", fmt.Errorf("create cluster %s: %w", clusterID, err)
	}

	availability := "ZONAL"
	if req.HighAvailability {
		availability = "REGIONAL"
	}
	var networks []*alloydb.AuthorizedNetwork
	for _, cidr := range p.cfg.authorizedNetworks {
		networks = append(networks, &alloydb.AuthorizedNetwork{CidrRange: cidr})
	}
	instance := &alloydb.Instance{
		InstanceType:     "PRIMARY",
		AvailabilityType: availability,
		MachineConfig:    &alloydb.MachineConfig{CpuCount: cpuCount},
		// AlloyDB requires password complexity checks on an instance with a public IP.
		DatabaseFlags: map[string]string{"password.enforce_complexity": "on"},
		NetworkConfig: &alloydb.InstanceNetworkConfig{
			EnablePublicIp:             true,
			AuthorizedExternalNetworks: networks,
		},
		Labels: map[string]string{"dbtest": "true"},
	}
	op, err = p.client.Projects.Locations.Clusters.Instances.Create(p.clusterName(clusterID), instance).InstanceId("primary").Context(ctx).Do()
	switch {
	case err == nil:
		if err := p.waitForOperation(ctx, op.Name, 30*time.Minute); err != nil {
			return "", fmt.Errorf("create instance in %s: %w", clusterID, err)
		}
	case hasCode(err, http.StatusConflict):
		// A prior attempt already created the instance.
	default:
		return "", fmt.Errorf("create instance in %s: %w", clusterID, err)
	}

	inst, err := p.waitForState(ctx, p.instanceName(clusterID), "READY", 30*time.Minute)
	if err != nil {
		return "", err
	}
	if inst.PublicIpAddress == "" {
		return "", fmt.Errorf("cluster %s has no public IP", clusterID)
	}
	return inst.PublicIpAddress, nil
}

// alloyDBCPUCount returns the smallest machine with req's vCPUs and memory.
// AlloyDB machines have 2 to 128 vCPUs and 8 GiB per vCPU.
func alloyDBCPUCount(req provider.ProvisionRequest) int64 {
	need := max(math.Ceil(req.VCPU), math.Ceil(float64(req.MemoryMiB)/8192))
	for _, count := range []int64{2, 4, 8, 16, 32, 64, 96, 128} {
		if float64(count) >= need {
			return count
		}
	}
	return 128
}

// location returns the project and region path that holds the clusters.
func (p *alloydbservice) location() string {
	return fmt.Sprintf("projects/%s/locations/%s", p.project, p.region)
}

// clusterName returns the full resource name of the cluster.
func (p *alloydbservice) clusterName(clusterID string) string {
	return p.location() + "/clusters/" + clusterID
}

// instanceName returns the full resource name of the cluster's primary instance.
func (p *alloydbservice) instanceName(clusterID string) string {
	return p.clusterName(clusterID) + "/instances/primary"
}

// WaitForReady waits for the primary instance to be READY and accept Postgres connections.
func (p *alloydbservice) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	if _, err := p.waitForState(ctx, p.instanceName(cluster.ID), "READY", 5*time.Minute); err != nil {
		return err
	}
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
				p.tel.Logger.Info("cluster is ready", slog.String("cluster", cluster.ID))
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("cluster %s did not accept connections within 5m", cluster.ID)
}

// Deprovision deletes the cluster with its instances and waits until it is gone.
// A cluster that is already gone counts as deleted.
func (p *alloydbservice) Deprovision(ctx context.Context, clusterID string) error {
	var op *alloydb.Operation
	var lastErr error
	for attempt := range 3 {
		op, lastErr = p.client.Projects.Locations.Clusters.Delete(p.clusterName(clusterID)).Force(true).Context(ctx).Do()
		if lastErr == nil {
			break
		}
		if hasCode(lastErr, http.StatusNotFound) {
			lastErr = nil
			break
		}
		if p.tel != nil {
			p.tel.Logger.Warn("deprovision attempt failed",
				slog.Int("attempt", attempt+1),
				slog.String("cluster", clusterID),
				slog.Any("error", lastErr),
			)
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr == nil && op != nil {
		lastErr = p.waitForOperation(ctx, op.Name, 15*time.Minute)
	}
	if lastErr != nil {
		return fmt.Errorf("delete cluster %s: %w", clusterID, lastErr)
	}
	if p.tel != nil {
		p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("alloydb").Inc()
		p.tel.Logger.Info("deprovisioned cluster", slog.String("cluster", clusterID))
	}
	return nil
}

// Disruptions returns restart and crash, and failover on a regional instance.
func (alloyDBRegistration) Disruptions(req provider.ProvisionRequest) []provider.Disruption {
	if req.HighAvailability {
		return []provider.Disruption{provider.Restart, provider.Crash, provider.Failover}
	}
	return []provider.Disruption{provider.Restart, provider.Crash}
}

// SwitchoverReadiness reports whether a regional instance accepts a failover now, from a
// validate-only failover request. A zonal instance always reports true.
func (p *alloydbservice) SwitchoverReadiness(ctx context.Context, cluster provider.ClusterInfo) (bool, error) {
	name := p.instanceName(cluster.ID)
	instances := p.client.Projects.Locations.Clusters.Instances
	inst, err := instances.Get(name).Context(ctx).Do()
	if err != nil {
		return false, fmt.Errorf("get instance %s: %w", name, err)
	}
	if inst.AvailabilityType != "REGIONAL" {
		return true, nil
	}
	_, err = instances.Failover(name, &alloydb.FailoverInstanceRequest{ValidateOnly: true}).Context(ctx).Do()
	switch {
	case err == nil:
		return true, nil
	case isFailoverLease(err):
		if p.tel != nil {
			p.tel.Logger.Info("failover not accepted yet",
				slog.String("cluster", cluster.ID),
				slog.String("reason", err.Error()),
			)
		}
		return false, nil
	}
	return false, fmt.Errorf("check failover on cluster %s: %w", cluster.ID, err)
}

// isFailoverLease reports whether err is AlloyDB refusing a failover during its lease.
func isFailoverLease(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest &&
		strings.Contains(apiErr.Message, "failover lease validation failed")
}

// Disrupt restarts, crashes or fails over the primary instance and returns once it is READY again.
// Crash stops the instance's VM. The public IP survives all three, so the returned ClusterInfo is unchanged.
func (p *alloydbservice) Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error) {
	start := time.Now()
	name := p.instanceName(cluster.ID)
	instances := p.client.Projects.Locations.Clusters.Instances

	var op *alloydb.Operation
	var err error
	switch disruption {
	case provider.Restart:
		op, err = instances.Restart(name, &alloydb.RestartInstanceRequest{}).Context(ctx).Do()
	case provider.Crash:
		op, err = instances.InjectFault(name, &alloydb.InjectFaultRequest{FaultType: "STOP_VM"}).Context(ctx).Do()
	case provider.Failover:
		op, err = instances.Failover(name, &alloydb.FailoverInstanceRequest{}).Context(ctx).Do()
	default:
		return provider.ClusterInfo{}, fmt.Errorf("alloydb cannot %s an instance", disruption)
	}
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("%s cluster %s: %w", disruption, cluster.ID, err)
	}

	if err := p.waitForOperation(ctx, op.Name, 15*time.Minute); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("%s cluster %s: %w", disruption, cluster.ID, err)
	}
	if _, err := p.waitForState(ctx, name, "READY", 15*time.Minute); err != nil {
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Logger.Info("disrupted cluster",
			slog.String("disruption", string(disruption)),
			slog.String("cluster", cluster.ID),
			slog.Duration("took", time.Since(start)),
		)
	}
	return cluster, nil
}

// waitForOperation polls the operation until it is done and returns its error, if any.
func (p *alloydbservice) waitForOperation(ctx context.Context, opName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		op, err := p.client.Projects.Locations.Operations.Get(opName).Context(ctx).Do()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("operation %s not done within %s: %w", opName, timeout, ctx.Err())
			}
			return fmt.Errorf("get operation %s: %w", opName, err)
		}
		if op.Done {
			if op.Error != nil {
				return fmt.Errorf("operation %s failed: %d: %s", opName, op.Error.Code, op.Error.Message)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation %s not done within %s: %w", opName, timeout, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

// waitForState polls the instance until it reports state.
// It fails early if the instance reports FAILED.
func (p *alloydbservice) waitForState(ctx context.Context, name, state string, timeout time.Duration) (*alloydb.Instance, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		inst, err := p.client.Projects.Locations.Clusters.Instances.Get(name).Context(ctx).Do()
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("instance %s not %s within %s: %w", name, state, timeout, ctx.Err())
			}
			return nil, fmt.Errorf("get instance %s: %w", name, err)
		}
		if inst.State == state {
			return inst, nil
		}
		if inst.State == "FAILED" {
			return nil, fmt.Errorf("instance %s is FAILED", name)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("instance %s not %s within %s: %w", name, state, timeout, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

func init() {
	Register(alloyDBRegistration{})
}

var _ DBService = (*alloydbservice)(nil)
