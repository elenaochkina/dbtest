package aws

import (
	"slices"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/provider/dbservice"
	"github.com/elenaochkina/dbtest/telemetry"
)

// legacyRDS runs the RDS database service through the old provider registry.
type legacyRDS struct {
	dbservice.DBService
}

func (legacyRDS) Supports(req provider.ProvisionRequest, disruption provider.Disruption) bool {
	return slices.Contains(dbservice.Disruptions(provider.RDS, req), disruption)
}

func newProvider(tel *telemetry.Telemetry) (provider.Provider, error) {
	c, err := cloud.NewCloudProvider(provider.AWS)
	if err != nil {
		return nil, err
	}
	service, err := dbservice.NewDBService(provider.RDS, c, tel)
	if err != nil {
		return nil, err
	}
	return legacyRDS{service}, nil
}

func init() {
	provider.Register(provider.RDS, newProvider)
}
