//go:build ignore

// This file is an example for review, not built. It shows RDS as a database
// service after the split, in its own package provider/dbservice/rds.
// It uses the AWS cloud from provider/cloud/aws_example.go.
//
// Compared with provider/aws/rds.go:
//   - LoadDefaultConfig moves to the cloud; RDS builds its client from Config().
//   - Supports(req, disruption) becomes the static disruptions(req) in the descriptor.
//   - newProvider is gone: NewRDSService returns DBService.
//   - rdsConfig keeps only RDS settings; the region comes from the AWS config.
package dbservice

import (
	"fmt"

	rdsapi "github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/provider/dbservice"
	"github.com/elenaochkina/dbtest/telemetry"
)

// rdsservice provisions Postgres on Amazon RDS.
type rdsservice struct {
	client *rdsapi.Client
	cfg    rdsConfig
	tel    *telemetry.Telemetry
}

// NewRDSService builds RDS in the AWS cloud.
func NewRDSService(c cloud.Cloud, tel *telemetry.Telemetry) (dbservice.DBService, error) {
	awsCloud, ok := c.(*cloud.AWS)
	if !ok {
		return nil, fmt.Errorf("rds needs the aws cloud, got %s", c.Name())
	}
	return &rdsservice{client: rdsapi.NewFromConfig(awsCloud.Config()), cfg: loadConfig(), tel: tel}, nil
}

// Provision, WaitForReady, Deprovision and Disrupt are unchanged from
// provider/aws/rds.go.

// disruptions lists what RDS can apply: a reboot, and a failover with a standby.
func disruptions(req provider.ProvisionRequest) []provider.Disruption {
	if req.HighAvailability {
		return []provider.Disruption{provider.Restart, provider.Failover}
	}
	return []provider.Disruption{provider.Restart}
}

func init() {
	dbservice.Register(dbservice.Descriptor{
		Name:             provider.RDS,
		Clouds:           []provider.CloudName{provider.AWS},
		HighAvailability: true,
		Disruptions:      disruptions,
		BuildDBService:   NewRDSService,
	})
}
