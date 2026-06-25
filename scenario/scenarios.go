package scenario

import (
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/workload"
)

// Behaviour-preserving scenarios: each is provision → workload(s) → teardown,
// mirroring the old -workload selection so the refactor changes structure, not
// runtime behaviour. New scenarios (e.g. save-result, scale) land as those
// steps are implemented.
// durabilityTables are the tables holding committed data the warehouse workload
// populates; the crash-recovery scenario fingerprints them on each side of the crash.
var durabilityTables = []string{"warehouse", "orders"}
var benchmarkResources = provider.ProvisionRequest{
	VCPU:      2,
	MemoryMiB: 2048,
}

func init() {
	Register("warehouse", provisionStep{}, workloadStep{workload.Warehouse})
	Register("benchmark", provisionStep{request: benchmarkResources}, workloadStep{workload.Pgbench}, saveResultStep{})
	Register("all",
		provisionStep{},
		workloadStep{workload.Warehouse},
		workloadStep{workload.Pgbench},
		saveResultStep{},
	)
	Register("crash-recovery",
		provisionStep{},
		workloadStep{workload.Warehouse},
		snapshotStep{label: "before_kill_process", tables: durabilityTables},
		killProcessStep{},
		verifyStep{label: "after_kill_process", baseline: "before_kill_process", tables: durabilityTables},
	)
}
