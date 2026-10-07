# AWS Database Architecture: RDS PostgreSQL vs. Amazon Aurora PostgreSQL

When evaluating high-availability (HA) database strategies for automation and continuous availability probing, understanding the architectural differences between **Standard RDS** and **Amazon Aurora** is critical. Their underlying structural differences alter failover behavior, application disruption profiles, and infrastructure recovery metrics.

---

## 1. Architectural Comparison

The fundamental difference lies in how compute nodes interact with storage.

| Architectural Component | Standard RDS PostgreSQL | Amazon Aurora PostgreSQL |
| :--- | :--- | :--- |
| **Storage Architecture** | Dedicated EBS volumes mirrored via block-level synchronous replication. | Cloud-native shared storage tier. Data is stripped across 6 storage nodes in 3 AZs. |
| **Compute vs Storage** | **Coupled:** The database node and storage volume exist together on a single VM. | **Decoupled:** Compute instances handle query execution; the distributed storage plane handles data. |
| **Failover Mechanism** | DNS switch swaps traffic from the Primary VM to a completely separate Standby VM. | A Reader instance is instantly promoted to a Writer node on the same shared volume. |
| **Rebuilding / Syncing** | **Required:** The old primary must be caught up/rebuilt to match the new primary's data gap. | **Not Required:** Recovering nodes simply reconnect as Readers directly to the shared storage tier. |

---

## 2. Failover Lifecycle & Probe Profile

Your client-side database probe container will record completely different failure signatures based on the platform type.

### Standard RDS Multi-AZ Lifecycle (The 4-Stage Model)
1. **Stage 1: The Outage (10s - 60s):** Connection is completely dropped as DNS updates. Returns `refused`, `reset`, or `timeout` across both reads and writes.
2. **Stage 2: Degraded Primary (Minutes):** Node D serves traffic asynchronously. Node A boots up and quietly catches up on transaction logs. App experiences **100% success with ultra-low latency**.
3. **Stage 3: The Handshake Handover / Write Blip (1s - 5s):** Node A catches up. AWS activates synchronous mode. Node D freezes incoming writes briefly to confirm lockstep execution. **Creates a predictable "Write-Only Failures" blip**; reads are completely unaffected.
4. **Stage 4: Fully Restored:** Synchronous HA is re-established. The AWS API finally swaps the `AvailabilityZone` metadata string.

### Amazon Aurora Failover Lifecycle (The Compressed Model)
1. **Stage 1: The Outage (10s - 30s):** Cluster manager detects the node failure and flips the CNAME to a selected Reader node.
2. **Stage 2: Storage Replay (Seconds):** The new Writer executes a quick parallel log replay from the shared storage tier to handle uncommitted actions. Connections briefly timeout.
3. **Stage 3: Fully Restored:** The database is online instantly. The old writer boots up and reconnects purely as a Reader node. **There is no variable catch-up window or delayed write-stalling blip.**

---

## 3. Deep-Dive Pros and Cons

### Standard RDS PostgreSQL
*   **Pros:**
    *   **Predictable Baseline Performance:** Minimal background interference or storage throttling under steady-state write loads.
    *   **Cost-Effective for Low to Medium Writes:** No abstract per-I/O pricing models; pricing is bounded by provisioned storage/IOPS limits.
    *   **Strict Standard Behavior:** Behaves identically to standard community PostgreSQL deployments, easing migration to bare metal or multi-cloud.
*   **Cons:**
    *   **The Post-Failover Blip:** Re-coupling the replication engine under synchronous constraints creates an un-deletable application latency spike minutes after the system appears "healthy."
    *   **Degraded Performance During Catch-Up:** A long outage creates a huge log gap, extending the time the cluster operates without full high-availability protection.
    *   **Storage Scaling Bottlenecks:** Modifying storage or increasing disk size locks the database into storage-modifying states that restrict further scaling modifications for hours.

### Amazon Aurora PostgreSQL
*   **Pros:**
    *   **Ultra-Fast Failovers:** Promotion completes in seconds because data blocks do not need to move or synchronize over VMs.
    *   **No Delayed Write Stalls:** The removal of the instance-to-instance sync handshake removes hidden secondary outages.
    *   **Scalable Read Replicas:** Up to 15 Read Replicas can be added, all querying the exact same shared volume with near-zero replication lag.
    *   **Storage Auto-scaling:** Storage automatically scales up and down dynamically without any maintenance windows or API modification states.
*   **Cons:**
    *   **Abstract Pricing Models:** Billable "I/O Operations" make forecasting costs difficult for highly transactional write-intensive applications.
    *   **Lock-In:** The storage layer is entirely proprietary to AWS, reducing portability to outside infrastructures.
    *   **Storage Replay Failures:** If a massive, complex transaction crashes mid-flight, the storage-level log replay can occasionally cause unexpected startup delays on promotion.

---

## 4. Automation Orchestration Impacts (Golang Drivers)

When building infrastructure automation frameworks, the API constraints require unique testing patterns:

```text
Standard RDS Workflow:
RebootDBInstance (ForceFailover) ──▶ Wait Status == "available" ──▶ Wait AZ String Changes (Stage 4 Complete)

Amazon Aurora Workflow:
FailoverDBCluster ───────────────▶ Wait Cluster Status == "available" + DBClusterMembers.IsClusterWriter Roles Swap
```

*   **For RDS:** Your `waitForReboot` implementation must run an multi-stage check to capture the infrastructure's true state, forcing the loop to stick around until the `AvailabilityZone` string changes.
*   **For Aurora:** The code completely drops individual instance polling and instead loops over `DescribeDBClusters` tracking the nested `DBClusterMembers` roles collection.



---

## 5. Implementation: `provider/aws/aurora.go`

| piece | what it does |
|---|---|
| `auroraProvider`, `NewAurora` | same shape as RDS: `loadConfig` + `newClient` |
| `Provision` | names the cluster after the token; the writer is `<token>-1`, plus `<token>-2` with `-ha`; on failure it cleans up, like RDS |
| `create` | `CreateDBCluster` (engine, version, credentials, subnet group, security groups, `dbtest` tag), then `CreateDBInstance` for each member; "already exists" is accepted, so retries are safe |
| `engineVersion` | `16` → Aurora's default 16.x (16.13) through `DescribeDBEngineVersions`; a full version is used as given |
| `waitForEndpoint` | polls every 15 s, up to 20 min, until the cluster and all its instances are `available`; returns the writer endpoint |
| `resolveAuroraClass` | override, otherwise db.r6g.large → xlarge → 2xlarge → 4xlarge by vCPU/memory; the starter's defaults (2 vCPU, 2 GiB) give **db.r6g.large** |
| `WaitForReady` | copy of the RDS connect loop (to be shared in PR 2) |
| `Deprovision` | deletes each member, then `DeleteDBCluster` with `SkipFinalSnapshot` and `DeleteAutomatedBackups`, retrying every 15 s while AWS says the cluster is busy; a missing cluster counts as success |
| `Supports` | restart always; failover only with `-ha` |
| `Disrupt` | restart: reboots the current writer and waits for it to leave and return to `available`. Failover: `FailoverDBCluster` targeting the reader, then waits until that reader is the writer and the cluster is `available`; it errors clearly if there's no reader |
| `waitForReboot`, `describeCluster`, `roles` | small helpers; `roles` returns the writer and the first reader from `DBClusterMembers` |
| `poll` | generic "check every interval until true or timeout", with the same timeout handling as RDS's `waitForStatus` |
| `init` | registers `provider.Aurora` |
