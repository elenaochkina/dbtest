// Package gcp is Google Cloud: its project, region and readiness check.
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
	"github.com/elenaochkina/dbtest/provider/cloud/local"
	"github.com/elenaochkina/dbtest/telemetry"
)

// Cloud is Google Cloud, with the project and region its services share.
type Cloud struct {
	project string
	region  string
}

// New reads the project and region from the environment.
func New() (cloud.Cloud, error) {
	region := os.Getenv("GCP_REGION")
	if region == "" {
		region = "us-west1"
	}
	return &Cloud{project: os.Getenv("GCP_PROJECT"), region: region}, nil
}

func (c *Cloud) Name() provider.CloudName { return provider.GCP }

// Check reports whether the .env variables are set and Application Default
// Credentials can get a token.
func (c *Cloud) Check(ctx context.Context) provider.Status {
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

// Runners lists Docker on this machine until a Cloud Run runner exists.
func (c *Cloud) Runners() []harness.RunnerName { return []harness.RunnerName{harness.Docker} }

// NewRunner builds the local cloud's Docker runner.
func (c *Cloud) NewRunner(name harness.RunnerName, tel *telemetry.Telemetry) (harness.Runner, error) {
	if name != harness.Docker {
		return nil, cloud.UnknownRunner(c, name)
	}
	machine, err := local.New()
	if err != nil {
		return nil, err
	}
	return machine.NewRunner(name, tel)
}

func (c *Cloud) Supports(service provider.DBServiceName) bool {
	return cloud.Supports(provider.GCP, service)
}

// Project returns the project the cloud's services create resources in.
func (c *Cloud) Project() string { return c.project }

// Region returns the region the cloud's services create resources in.
func (c *Cloud) Region() string { return c.region }

func init() {
	cloud.Register(provider.GCP, New)
}

var _ cloud.Cloud = (*Cloud)(nil)
