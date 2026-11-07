package cloud

import (
	"context"
	"os"
	"time"

	"golang.org/x/oauth2/google"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
)

// GCP is Google Cloud, with the project and region its services share.
type GCP struct {
	project string
	region  string
}

// NewGCP reads the project and region from the environment.
func NewGCP() (Cloud, error) {
	region := os.Getenv("GCP_REGION")
	if region == "" {
		region = "us-west1"
	}
	return &GCP{project: os.Getenv("GCP_PROJECT"), region: region}, nil
}

func (c *GCP) Name() provider.CloudName { return provider.GCP }

// Check reports whether the .env variables are set and Application Default
// Credentials can get a token.
func (c *GCP) Check(ctx context.Context) provider.Status {
	if status, missing := missingVariables("GCP_PROJECT", "GCP_AUTHORIZED_NETWORKS"); missing {
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

// Runner runs the probe and bench containers on this machine until a Cloud Run runner exists.
func (c *GCP) Runner() harness.RunnerName { return harness.Docker }

func (c *GCP) Supports(service provider.DBServiceName) bool {
	return Supports(provider.GCP, service)
}

// Project returns the project the cloud's services create resources in.
func (c *GCP) Project() string { return c.project }

// Region returns the region the cloud's services create resources in.
func (c *GCP) Region() string { return c.region }

func init() {
	Register(provider.GCP, NewGCP)
}

var _ Cloud = (*GCP)(nil)
