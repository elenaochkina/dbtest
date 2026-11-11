// Package dbservicetest checks that a database service keeps the dbservice
// contract. Every registered service runs CheckRegistration; Run provisions a
// real cluster and puts it through each disruption the service declares.
package dbservicetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/provider/dbservice"
	"github.com/elenaochkina/dbtest/telemetry"
)

// CheckRegistration checks what the registration declares, without credentials.
func CheckRegistration(t *testing.T, registration dbservice.Registration) {
	t.Helper()
	name := registration.Name()
	if name == "" {
		t.Fatal("Name() is empty")
	}
	if got, ok := dbservice.Get(name); !ok || got.Name() != name {
		t.Errorf("Get(%s) does not return the registration", name)
	}

	if len(registration.Clouds()) == 0 {
		t.Error("Clouds() is empty")
	}
	for _, c := range registration.Clouds() {
		if !slices.Contains(cloud.Names(), c) {
			t.Errorf("runs in %s, which is not a registered cloud", c)
		}
		if !cloud.Supports(c, name) {
			t.Errorf("cloud.Supports(%s, %s) = false", c, name)
		}
	}

	single := registration.Disruptions(provider.ProvisionRequest{})
	if len(single) == 0 {
		t.Error("Disruptions() is empty without a standby")
	}
	if slices.Contains(single, provider.Failover) {
		t.Error("offers failover without a standby")
	}
	standby := registration.Disruptions(provider.ProvisionRequest{HighAvailability: true})
	for _, disruption := range single {
		if !slices.Contains(standby, disruption) {
			t.Errorf("offers %s without a standby but not with one", disruption)
		}
	}
	if registration.SupportsHighAvailability() != slices.Contains(standby, provider.Failover) {
		t.Errorf("SupportsHighAvailability() = %v but failover with a standby = %v",
			registration.SupportsHighAvailability(), slices.Contains(standby, provider.Failover))
	}

	if _, err := registration.Build(elsewhere{}, nil); err == nil {
		t.Error("Build returned no error for a cloud the service does not run in")
	}
}

// Run provisions a cluster from req in c, checks it accepts connections, applies
// each disruption the service declares for req, and deprovisions it twice.
func Run(t *testing.T, name provider.DBServiceName, c cloud.Cloud, req provider.ProvisionRequest) {
	t.Helper()
	ctx := context.Background()
	service, err := dbservice.NewDBService(name, c, nil)
	if err != nil {
		t.Fatal(err)
	}

	token := "dbtest-conformance-" + time.Now().UTC().Format("20060102150405")
	cluster, err := service.Provision(ctx, req, token, "conformance-"+token)
	t.Cleanup(func() {
		if err := service.Deprovision(context.WithoutCancel(ctx), token); err != nil {
			t.Errorf("deprovision in cleanup: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	checkCluster(t, cluster)
	if err := service.WaitForReady(ctx, cluster); err != nil {
		t.Fatalf("wait for ready: %v", err)
	}
	connect(t, cluster)

	registration, _ := dbservice.Get(name)
	for _, disruption := range registration.Disruptions(req) {
		if cluster, err = service.Disrupt(ctx, cluster, disruption); err != nil {
			t.Fatalf("%s: %v", disruption, err)
		}
		checkCluster(t, cluster)
		if err := service.WaitForReady(ctx, cluster); err != nil {
			t.Fatalf("wait for ready after %s: %v", disruption, err)
		}
		connect(t, cluster)
	}

	if err := service.Deprovision(ctx, cluster.ID); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
	if err := service.Deprovision(ctx, cluster.ID); err != nil {
		t.Errorf("second deprovision of a deleted cluster: %v", err)
	}
}

// checkCluster checks the fields every caller of Provision and Disrupt relies on.
func checkCluster(t *testing.T, cluster provider.ClusterInfo) {
	t.Helper()
	if cluster.ID == "" || cluster.Password == "" {
		t.Errorf("cluster %+v has no ID or password", cluster)
	}
	for label, connInfo := range map[string]provider.PGConnInfo{"public": cluster.PublicConnInfo, "private": cluster.PrivateConnInfo} {
		if connInfo.Host == "" || connInfo.Port == 0 || connInfo.User == "" || connInfo.Database == "" {
			t.Errorf("%s %+v is incomplete", label, connInfo)
		}
	}
}

// connect runs SELECT 1 through the address the worker uses.
func connect(t *testing.T, cluster provider.ClusterInfo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cluster.PublicConnInfo.URL(cluster.Password))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(new(int)); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
}

// elsewhere is a cloud no service runs in.
type elsewhere struct{}

func (elsewhere) Name() provider.CloudName { return "conformance-elsewhere" }
func (elsewhere) Check(context.Context) provider.Status {
	return provider.Status{State: provider.StateReady}
}
func (elsewhere) Runners() []harness.RunnerName { return nil }
func (elsewhere) NewRunner(harness.RunnerName, *telemetry.Telemetry) (harness.Runner, error) {
	return nil, errors.New("no runners")
}
func (elsewhere) Images(harness.RunnerName) harness.Images { return harness.Images{} }
func (elsewhere) Supports(provider.DBServiceName) bool     { return false }
