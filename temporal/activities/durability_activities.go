package activities

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
)

type FingerprintInput struct {
	Cluster provider.ClusterInfo
	Tables  []string // table names whose data is checksummed
}

// DurabilityActivities compute content fingerprints of a cluster's tables.
// Telemetry is the only dependency; the target DB is reached via the cluster
// DSN in the input.
type DurabilityActivities struct {
	tel *telemetry.Telemetry
}

func NewDurabilityActivities(tel *telemetry.Telemetry) *DurabilityActivities {
	return &DurabilityActivities{tel: tel}
}

// Fingerprint returns a content digest per table, keyed by table name.
func (a *DurabilityActivities) Fingerprint(ctx context.Context, input FingerprintInput) (map[string]string, error) {
	pool, err := pgadapter.Connect(input.Cluster.Target.URL(input.Cluster.Password), a.tel)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	digests := make(map[string]string, len(input.Tables))
	for _, table := range input.Tables {
		digest, err := validator.Fingerprint(ctx, pool, table, a.tel)
		if err != nil {
			return nil, fmt.Errorf("fingerprint %q: %w", table, err)
		}
		digests[table] = digest
	}
	return digests, nil
}
