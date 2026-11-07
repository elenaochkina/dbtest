# Split provider into cloud and database service

## Problem

`provider` means two things today: the cloud where things run (AWS) and the
database under test (RDS, Aurora). The link between them is written by hand in
several places:

| Coupling | Where |
|---|---|
| Each of three packages logs in to AWS on its own | `provider/aws/aws.go`, `harness/aws/fargate.go`, `internal/providercfg/aws.go` |
| Database → container runner | `runnerFor` in `temporal/workflows/recovery_workflow.go` |
| "Not Docker" means AWS (HA step, images, cost warning) | `internal/console/recovery_form.go` |
| The readiness check is written for RDS and Aurora | `internal/providercfg/aws.go` |
| Disruption rules copied from each provider's `Supports` | `disruptions()` in `internal/console/recovery_form.go` |

Cloud SQL is the first non-AWS service, and each of these places would need
another hand edit.

## Goal

- A cloud owns login, region and where the probe and bench containers run.
- A database service creates, deletes and disrupts one database, and gets its
  cloud from outside.
- Adding a database service is one registration. No cloud, workflow or console
  edit.

Scope: local Docker, RDS, Aurora, Cloud SQL, AlloyDB. Other managed Postgres
(Supabase, Neon, Snowflake Postgres) must plug in later the same way, possibly
on several clouds each.

## Layout

```
provider/
  provider.go      shared types: CloudName, DBServiceName, ProvisionRequest,
                   ClusterInfo, Disruption, Status
  cloud/
    cloud.go       Cloud interface, registry, Supports
    local.go       Docker runner
    aws.go         one aws.Config, readiness check, Fargate runner
    gcp.go         ADC, readiness check, Cloud Run runner
  dbservice/
    dbservice.go   DBService interface, Descriptor, registry
    docker.go      local Docker Postgres
    rds.go
    aurora.go
    cloudsql.go
    alloydb.go     later
```

Imports go one way: `provider` ← `cloud` ← `dbservice`. Both packages are
flat, so importing one registers every cloud or service in it. A service gets
its credentials from the cloud's type (`rds.go` uses `cloud.AWS`).

## Interfaces

```go
// provider/cloud
type Cloud interface {
	Name() provider.CloudName
	Check(ctx context.Context) provider.Status
	Runner() harness.RunnerName
	Supports(service provider.DBServiceName) bool
}

// provider/dbservice
type DBService interface {
	Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error)
	WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error
	Deprovision(ctx context.Context, clusterID string) error
	Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error)
}

type Descriptor struct {
	Name             provider.DBServiceName
	Clouds           []provider.CloudName
	SupportsHighAvailability bool
	Disruptions      func(provider.ProvisionRequest) []provider.Disruption
	BuildDBService   func(cloud.Cloud, *telemetry.Telemetry) (DBService, error)
}
```

`DBService` is today's `Provider` without `Supports`. The supported disruptions
move to the descriptor as a static function, so the console and the workflow can
ask without credentials.

`SupportsHighAvailability` says whether the service can run with a standby, so the
console shows the HA step without checking for Docker.

Each constructor returns the interface (`DBService`, `Cloud`), so it goes into
the registry as it is, without an adapter like today's `newProvider`.

## Supports

A service lists its clouds when it registers. `dbservice.Register` records each
pair with `cloud.Host(cloudName, serviceName)`, and `Cloud.Supports` reads those
pairs. AWS supports RDS because RDS lists `aws`, not because AWS lists RDS, so a
new service never edits a cloud.

## Registry

Each package registers itself in `init()`. The registry stores the descriptor
and its constructor; `NewDBService` builds the service only when a run starts.

```go
// provider/dbservice/rds
func init() {
	dbservice.Register(dbservice.Descriptor{Name: provider.RDS, Clouds: []provider.CloudName{provider.AWS}, …, BuildDBService: NewRDSService})
}
```

Adding a service adds one file to `dbservice`; no binary changes.

| Function | Used by |
|---|---|
| `cloud.Names()`, `cloud.NewCloudProvider(name)` | console readiness rows and menu; the worker builds the run's cloud |
| `dbservice.Names()` | console menu, filtered by `Cloud.Supports` |
| `dbservice.Get(name)` returns `(Descriptor, bool)` | console: `SupportsHighAvailability` |
| `dbservice.Disruptions(name, req)` | console menu; the workflow check |
| `dbservice.NewDBService(name, cloud, tel)` | activities, in place of `provider.Run` |

## Run config

A run names both: `{Cloud: aws, DBService: rds}`. The workflow checks
`Cloud.Supports`, takes the runner from `Cloud.Runner()` (`runnerFor` is
removed) and checks the disruption against the descriptor. The state DB and
metrics get a `cloud` column and label.

## Console

- One readiness row per registered cloud.
- The form asks for the cloud, then the database service (filtered by
  `Supports`), then HA if the descriptor has `SupportsHighAvailability`, then the
  disruption (from the descriptor).
- `providerChoices`, `disruptions()` and `internal/providercfg` are removed.

## Steps

1. Types and registries in `provider`, `cloud` and `dbservice`.
2. `cloud/local.go` and `dbservice/docker.go`, from `provider/docker`.
3. `cloud/aws`, `dbservice/rds`, `dbservice/aurora`. One `aws.Config` shared
   with the Fargate runner. The readiness check moves from `providercfg`.
4. Workflows, activities, state DB and metrics.
5. Console.
6. `cloud/gcp` and `dbservice/cloudsql`, rebased from `gcp-provider`.

Docker becomes cloud `local` plus service `docker`, so the Grafana label is
unchanged.

## Before starting

Merge #29, #30 and #31 first. They change the console, the providers and the
metrics this rewrites.
