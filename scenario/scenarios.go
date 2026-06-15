package scenario

import "github.com/elenaochkina/dbtest/workload"

// Behaviour-preserving scenarios: each is provision → workload(s) → teardown,
// mirroring the old -workload selection so the refactor changes structure, not
// runtime behaviour. New scenarios (e.g. save-result, scale) land as those
// steps are implemented.
// durabilityTables are the tables holding committed data the warehouse workload
// populates; the restart scenario fingerprints them on each side of the crash.
var durabilityTables = []string{"warehouse", "orders"}

func init() {
	Register("warehouse", provisionStep{}, workloadStep{workload.Warehouse})
	Register("benchmark", provisionStep{}, workloadStep{workload.Pgbench})
	Register("all",
		provisionStep{},
		workloadStep{workload.Warehouse},
		workloadStep{workload.Pgbench},
	)
	Register("restart",
		provisionStep{},
		workloadStep{workload.Warehouse},
		snapshotStep{label: "before_restart", tables: durabilityTables},
		restartStep{},
		verifyStep{label: "after_restart", baseline: "before_restart", tables: durabilityTables},
	)
}
