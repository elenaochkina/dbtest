// Package gcp is Google Cloud: its project, region, readiness check and Cloud Run runner.
package gcp

import (
	"context"
	"os"
	"time"

	"golang.org/x/oauth2/google"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
)

// GCPCloud is Google Cloud, with the project and region its services share.
type GCPCloud struct {
	project string
	region  string
}

// New reads the project and region from the environment.
func New() (cloud.Cloud, error) {
	region := os.Getenv("GCP_REGION")
	if region == "" {
		region = "us-west1"
	}
	return &GCPCloud{project: os.Getenv("GCP_PROJECT"), region: region}, nil
}

func (c *GCPCloud) Name() provider.CloudName { return provider.GCP }

// Check reports whether the .env variables are set and Application Default
// Credentials can get a token.
func (c *GCPCloud) Check(ctx context.Context) provider.Status {
	if status, missing := cloud.MissingVariables("GCP_PROJECT", "GCP_AUTHORIZED_NETWORKS"); missing {
		return status
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	credentials, err := google.FindDefaultCredentials(ctx, sqladmin.SqlserviceAdminScope)
	if err == nil {
		_, err = credentials.TokenSource.Token()
	}
	if err != nil {
		return provider.Status{State: provider.StateNeedsLogin, Detail: "run gcloud auth application-default login"}
	}
	return provider.Status{State: provider.StateReady, Detail: "project " + c.project}
}

// Runners lists Cloud Run.
func (c *GCPCloud) Runners() []harness.RunnerName { return []harness.RunnerName{harness.CloudRun} }

// NewRunner builds the Cloud Run runner in the cloud's project and region.
func (c *GCPCloud) NewRunner(name harness.RunnerName, tel *telemetry.Telemetry) (harness.Runner, error) {
	if name != harness.CloudRun {
		return nil, cloud.UnknownRunner(c, name)
	}
	return newCloudRunRunner(c.project, c.region, tel)
}

// Images returns the probe and bench jobs from .env, defaulting to the jobs terraform/gcp creates.
func (c *GCPCloud) Images(harness.RunnerName) harness.Images {
	return harness.Images{
		Probe: cloud.EnvOr("GCP_CLOUDRUN_PROBE_JOB", harness.ProbeName),
		Bench: cloud.EnvOr("GCP_CLOUDRUN_BENCH_JOB", harness.BenchName),
	}
}

func (c *GCPCloud) Supports(service provider.DBServiceName) bool {
	return cloud.Supports(provider.GCP, service)
}

// Project returns the project the cloud's services create resources in.
func (c *GCPCloud) Project() string { return c.project }

// Region returns the region the cloud's services create resources in.
func (c *GCPCloud) Region() string { return c.region }

func init() {
	cloud.Register(provider.GCP, New)
}

var _ cloud.Cloud = (*GCPCloud)(nil)
