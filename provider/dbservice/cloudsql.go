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
	"google.golang.org/api/googleapi"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// cloudsqlservice provisions Postgres on Cloud SQL.
type cloudsqlservice struct {
	client  *sqladmin.Service
	project string
	region  string
	cfg     cloudSQLConfig
	tel     *telemetry.Telemetry
}

// cloudSQLRegistration registers Cloud SQL; Build returns the service that provisions it.
type cloudSQLRegistration struct{}

func (cloudSQLRegistration) Name() provider.DBServiceName { return provider.CloudSQL }

func (cloudSQLRegistration) Clouds() []provider.CloudName { return []provider.CloudName{provider.GCP} }

func (cloudSQLRegistration) SupportsHighAvailability() bool { return true }

// Build builds Cloud SQL in the GCP cloud.
func (cloudSQLRegistration) Build(c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error) {
	gcpCloud, ok := c.(*gcpcloud.Cloud)
	if !ok {
		return nil, fmt.Errorf("cloudsql needs the gcp cloud, got %s", c.Name())
	}
	if gcpCloud.Project() == "" {
		return nil, errors.New("GCP_PROJECT is not set")
	}
	cfg := loadCloudSQLConfig()
	if len(cfg.authorizedNetworks) == 0 {
		return nil, errors.New("GCP_AUTHORIZED_NETWORKS is not set")
	}
	client, err := sqladmin.NewService(context.Background())
	if err != nil {
		return nil, fmt.Errorf("create cloud sql admin client: %w", err)
	}
	return &cloudsqlservice{client: client, project: gcpCloud.Project(), region: gcpCloud.Region(), cfg: cfg, tel: tel}, nil
}

// cloudSQLConfig holds the Cloud SQL settings from the GCP_* environment.
type cloudSQLConfig struct {
	tierOverride       string
	authorizedNetworks []string
}

// loadCloudSQLConfig reads GCP_SQL_TIER and GCP_AUTHORIZED_NETWORKS into a cloudSQLConfig.
func loadCloudSQLConfig() cloudSQLConfig {
	return cloudSQLConfig{
		tierOverride:       os.Getenv("GCP_SQL_TIER"),
		authorizedNetworks: splitNonEmpty(os.Getenv("GCP_AUTHORIZED_NETWORKS"), ","),
	}
}

// Provision creates a Cloud SQL instance sized from req and returns its public IP.
func (p *cloudsqlservice) Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error) {
	start := time.Now()

	if req.VCPU < 0 || req.MemoryMiB < 0 || req.DiskGiB < 0 {
		return provider.ClusterInfo{}, fmt.Errorf("invalid provision request: negative resource (vcpu=%v memory_mib=%d disk_gib=%d)", req.VCPU, req.MemoryMiB, req.DiskGiB)
	}
	if token == "" {
		token = "dbtest-" + uuid.NewString()
	}
	if password == "" {
		password = uuid.NewString()
	}

	instanceName := token
	tier := resolveCloudSQLTier(req, p.cfg.tierOverride)

	host, err := p.create(ctx, req, instanceName, tier, password)
	if err != nil {
		// The instance may already be billing; delete it on a fresh ctx.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		derr := p.Deprovision(cleanupCtx, instanceName)
		cancel()
		if derr != nil && p.tel != nil {
			p.tel.Logger.Warn("cleanup after failed provision did not delete instance",
				slog.String("instance", instanceName),
				slog.Any("error", derr),
			)
		}
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("cloudsql").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("instance", instanceName),
			slog.String("tier", tier),
			slog.String("endpoint", host),
			slog.Float64("vcpu", req.VCPU),
			slog.Int("memory_mib", req.MemoryMiB),
		)
	}

	target := provider.PGTarget{Host: host, Port: 5432, Database: "postgres", User: "postgres"}
	return provider.ClusterInfo{
		ID:       instanceName,
		Target:   target,
		Internal: target,
		Password: password,
	}, nil
}

// create inserts the instance and waits until it is RUNNABLE, then returns its
// public IP. A retried attempt finds the instance a prior attempt created.
func (p *cloudsqlservice) create(ctx context.Context, req provider.ProvisionRequest, name, tier, password string) (string, error) {
	version, err := cloudSQLVersion(req.PostgresVersion)
	if err != nil {
		return "", err
	}

	availability := "ZONAL"
	if req.HighAvailability {
		availability = "REGIONAL"
	}
	var networks []*sqladmin.AclEntry
	for _, cidr := range p.cfg.authorizedNetworks {
		networks = append(networks, &sqladmin.AclEntry{Value: cidr})
	}

	instance := &sqladmin.DatabaseInstance{
		Name:            name,
		DatabaseVersion: version,
		Region:          p.region,
		RootPassword:    password,
		Settings: &sqladmin.Settings{
			Tier:             tier,
			Edition:          "ENTERPRISE",
			AvailabilityType: availability,
			DataDiskType:     "PD_SSD",
			DataDiskSizeGb:   cloudSQLDiskGiB(req),
			UserLabels:       map[string]string{"dbtest": "true"},
			IpConfiguration: &sqladmin.IpConfiguration{
				Ipv4Enabled:        true,
				AuthorizedNetworks: networks,
			},
			// High availability on Postgres needs backups with point-in-time recovery.
			BackupConfiguration: &sqladmin.BackupConfiguration{
				Enabled:                    req.HighAvailability,
				PointInTimeRecoveryEnabled: req.HighAvailability,
			},
		},
	}

	op, err := p.client.Instances.Insert(p.project, instance).Context(ctx).Do()
	switch {
	case err == nil:
		if err := p.waitForOperation(ctx, op.Name, 30*time.Minute); err != nil {
			return "", fmt.Errorf("create instance %s: %w", name, err)
		}
	case hasCode(err, http.StatusConflict):
		// A prior attempt already created the instance.
	default:
		return "", fmt.Errorf("create instance %s: %w", name, err)
	}

	inst, err := p.waitForState(ctx, name, "RUNNABLE", 30*time.Minute)
	if err != nil {
		return "", err
	}
	for _, ip := range inst.IpAddresses {
		if ip.Type == "PRIMARY" {
			return ip.IpAddress, nil
		}
	}
	return "", fmt.Errorf("instance %s has no public IP", name)
}

// resolveCloudSQLTier builds a db-custom-<vCPU>-<MiB> tier from req, unless override is set.
// Custom tiers take 1 or an even number of vCPUs and 0.9 to 6.5 GiB per vCPU in
// 256 MiB steps, with at least 3840 MiB.
func resolveCloudSQLTier(req provider.ProvisionRequest, override string) string {
	if override != "" {
		return override
	}
	vcpu := max(1, int(math.Ceil(req.VCPU)))
	if vcpu > 1 && vcpu%2 == 1 {
		vcpu++
	}
	for req.MemoryMiB > vcpu*6656 {
		if vcpu == 1 {
			vcpu = 2
		} else {
			vcpu += 2
		}
	}
	memory := max(req.MemoryMiB, 3840, int(math.Ceil(float64(vcpu)*0.9*1024)))
	memory = (memory + 255) / 256 * 256
	return fmt.Sprintf("db-custom-%d-%d", vcpu, memory)
}

// cloudSQLVersion maps a Postgres version such as "18" or "16.4" to POSTGRES_18
// or POSTGRES_16. Cloud SQL picks the minor version.
func cloudSQLVersion(version string) (string, error) {
	major, _, _ := strings.Cut(version, ".")
	if major == "" {
		return "", errors.New("cloud sql needs a postgres version")
	}
	return "POSTGRES_" + major, nil
}

// cloudSQLDiskGiB returns the requested disk size, at least the 20 GiB RDS uses.
func cloudSQLDiskGiB(req provider.ProvisionRequest) int64 {
	return int64(max(req.DiskGiB, 20))
}

// WaitForReady waits for the instance to be RUNNABLE and accept Postgres connections.
func (p *cloudsqlservice) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	if _, err := p.waitForState(ctx, cluster.ID, "RUNNABLE", 5*time.Minute); err != nil {
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
				p.tel.Logger.Info("cluster is ready", slog.String("instance", cluster.ID))
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("cluster %s did not accept connections within 5m", cluster.ID)
}

// Deprovision deletes the instance. An instance that is already gone counts as deleted.
func (p *cloudsqlservice) Deprovision(ctx context.Context, clusterID string) error {
	var lastErr error
	for attempt := range 3 {
		_, lastErr = p.client.Instances.Delete(p.project, clusterID).Context(ctx).Do()
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
				slog.String("instance", clusterID),
				slog.Any("error", lastErr),
			)
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("delete instance %s: %w", clusterID, lastErr)
	}
	if p.tel != nil {
		p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("cloudsql").Inc()
		p.tel.Logger.Info("deprovisioned cluster", slog.String("instance", clusterID))
	}
	return nil
}

// Disruptions returns restart, and failover on a regional instance.
// Cloud SQL has no ungraceful kill.
func (cloudSQLRegistration) Disruptions(req provider.ProvisionRequest) []provider.Disruption {
	if req.HighAvailability {
		return []provider.Disruption{provider.Restart, provider.Failover}
	}
	return []provider.Disruption{provider.Restart}
}

// Disrupt restarts or fails over the instance and returns once it is RUNNABLE again.
// The public IP survives both, so the returned ClusterInfo is unchanged.
func (p *cloudsqlservice) Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error) {
	start := time.Now()

	var op *sqladmin.Operation
	var err error
	switch disruption {
	case provider.Restart:
		op, err = p.client.Instances.Restart(p.project, cluster.ID).Context(ctx).Do()
	case provider.Failover:
		inst, getErr := p.client.Instances.Get(p.project, cluster.ID).Context(ctx).Do()
		if getErr != nil {
			return provider.ClusterInfo{}, fmt.Errorf("get instance %s: %w", cluster.ID, getErr)
		}
		op, err = p.client.Instances.Failover(p.project, cluster.ID, &sqladmin.InstancesFailoverRequest{
			FailoverContext: &sqladmin.FailoverContext{SettingsVersion: inst.Settings.SettingsVersion},
		}).Context(ctx).Do()
	default:
		return provider.ClusterInfo{}, fmt.Errorf("cloud sql cannot %s an instance", disruption)
	}
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("%s instance %s: %w", disruption, cluster.ID, err)
	}

	if err := p.waitForOperation(ctx, op.Name, 15*time.Minute); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("%s instance %s: %w", disruption, cluster.ID, err)
	}
	if _, err := p.waitForState(ctx, cluster.ID, "RUNNABLE", 15*time.Minute); err != nil {
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Logger.Info("disrupted cluster",
			slog.String("disruption", string(disruption)),
			slog.String("instance", cluster.ID),
			slog.Duration("took", time.Since(start)),
		)
	}
	return cluster, nil
}

// waitForOperation polls the operation until it is DONE and returns its error, if any.
func (p *cloudsqlservice) waitForOperation(ctx context.Context, opName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		op, err := p.client.Operations.Get(p.project, opName).Context(ctx).Do()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("operation %s not done within %s: %w", opName, timeout, ctx.Err())
			}
			return fmt.Errorf("get operation %s: %w", opName, err)
		}
		if op.Status == "DONE" {
			if op.Error != nil && len(op.Error.Errors) > 0 {
				e := op.Error.Errors[0]
				return fmt.Errorf("operation %s failed: %s: %s", opName, e.Code, e.Message)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation %s not done within %s: %w", opName, timeout, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// waitForState polls the instance until it reports state.
// It fails early if the instance reports FAILED.
func (p *cloudsqlservice) waitForState(ctx context.Context, name, state string, timeout time.Duration) (*sqladmin.DatabaseInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		inst, err := p.client.Instances.Get(p.project, name).Context(ctx).Do()
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
		case <-time.After(2 * time.Second):
		}
	}
}

// hasCode reports whether err is a Google API error with the given HTTP status.
func hasCode(err error, code int) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func init() {
	Register(cloudSQLRegistration{})
}

var _ DBService = (*cloudsqlservice)(nil)
