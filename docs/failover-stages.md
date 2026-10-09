# AWS Database Failover Recovery Stages and Metrics

This document outlines the detailed recovery lifecycles for both **AWS RDS Multi-AZ** and **Amazon Aurora** during a forced failover or node disruption. Use these behavioral profiles and metrics to configure your automated database probe containers.

---

## Part 1: Standard RDS Multi-AZ Failover Lifecycle

The recovery of an AWS RDS Multi-AZ instance after a primary node disruption happens in **four distinct stages**. Because the duration of the background synchronization depends entirely on the size of the data gap, these stages must be tracked and measured dynamically.

⚡ [Disruption] ── Stage 1: Outage ──▶ 👍 [Traffic Restored] ── Stage 2: Degraded ──▶ ⏳ [The Switch] ── Stage 3: Write Blip ──▶ ✅ [Fully Synced] ── Stage 4: Stable
### Stage 1: The Initial Outage (Failover in Flight)
*   **What is happening:** The primary node (**Node A**) has failed or been cut off. AWS is updating its internal DNS and routing tables to promote the standby node (**Node D**) to primary.
*   **Probe signature:** 100% failure rate for both reads and writes. Errors returned include connection refused, connection reset, or timeout.
*   **Typical duration:** 10 to 60 seconds.

### Stage 2: Degraded Primary (The Asynchronous Catch-Up)
*   **What is happening:** Node D is now active and accepting application traffic as primary, but it is running alone without an active standby. In the background, the recovered node (Node A) reconnects and begins streaming the missing database transaction logs.
*   **Probe signature:** 100% success rate with low latency. Both reads and writes succeed normally because Node D does not wait for Node A to acknowledge writes during this asynchronous catch-up phase.
*   **AWS API status:** DB instance status reports as `"available"`, but the `AvailabilityZone` field still reflects the old primary zone.
*   **Duration:** Variable, depending on the volume of un-replicated write activity executed during the outage.

### Stage 3: The Handshake Handover (The Write Blip)
*   **What is happening:** Node A has finished replaying the missing transaction logs. AWS activates synchronous replication mode, forcing Node D to momentarily stall incoming writes while Node A completes the final lock-step handshake and commits the remaining transactions.
*   **Probe signature:** Write-only failures or severe write latency spikes exceeding the probe timeout threshold. Reads continue to succeed normally because local read operations do not require standby acknowledgment.
*   **Duration:** Very short, typically 1 to 5 seconds.

### Stage 4: Fully Synced and High-Availability Restored
*   **What is happening:** The replication chain is fully synchronous again, and the RDS cluster is protected against subsequent hardware failures.
*   **Probe signature:** 100% success rate across both reads and writes. Write latency returns to normal baseline levels (with standard synchronous overhead).
*   **AWS API status:** DB instance status is `"available"`, and the `AvailabilityZone` field finally updates to reflect the new primary zone.

### RDS Recovery Metrics Calculation Formulas
*   **Total Application Downtime** = Timestamp when Stage 2 begins minus Timestamp when Stage 1 begins
*   **Replication Sync Catch-Up Duration** = Timestamp when Stage 3 begins minus Timestamp when Stage 2 begins
*   **Synchronous Transition Cost (Blip Length)** = Timestamp when Stage 3 ends minus Timestamp when Stage 3 begins
*   **Total Time to Full Recovery** = Timestamp when Stage 4 API swap is observed minus Timestamp when Stage 1 begins

---

## Part 2: Amazon Aurora Failover Lifecycle

Because Amazon Aurora utilizes a distributed, cloud-native **shared storage architecture**, it decouples compute from disk. Data is continuously written 6-ways across 3 AZs at the storage tier, eliminating the need for an instance-to-instance transaction log catch-up phase. The recovery happens in **three highly compressed stages**.

[Disruption] ── Stage 1: Outage ──▶ 🔄 Stage 2: Storage Replay ──▶ ✅ [Fully Restored] ── Stage 3: Stable

### Stage 1: The Outage (Detection & Promotion)
*   **What is happening:** The primary Writer instance crashes or drops. Aurora’s cluster manager detects the loss of heartbeat and instantly reconfigured the cluster network endpoint (CNAME) to target a designated Read Replica.
*   **Probe signature:** 100% failure rate for writes. Reads sent to the main cluster endpoint drop briefly, though reads targeting explicit, surviving reader endpoints continue functioning.
*   **Typical duration:** 10 to 30 seconds (can drop under 10 seconds if using RDS Proxy).

### Stage 2: Storage Replay (Crash Recovery)
*   **What is happening:** The newly promoted primary Writer node opens its session to the shared storage layer. The database engine executes a quick, highly parallel log replay directly from the storage nodes to resolve any uncommitted, in-flight operations.
*   **Probe signature:** New connection attempts are refused or time out while the database engine initializes its runtime environment.
*   **Typical duration:** 1 to 5 seconds.

### Stage 3: Fully Restored (High-Availability Maintained)
*   **What is happening:** The new Writer is accepting traffic. The old writer node boots back up, automatically connects to the same shared storage layer, and registers itself as a safe Read Replica. **There is no data gap to close, and no delayed write-stalling blip occurs.**
*   **Probe signature:** 100% success rate across both reads and writes. Performance immediately hits baseline speeds.
*   **AWS API status:** The DB Cluster status reports as `"available"`, and the `DBClusterMembers` collection reflects the updated roles (the promoted instance is flagged as `IsClusterWriter = true`).

### Aurora Recovery Metrics Calculation Formulas
*   **Total Application Downtime** = Timestamp when Stage 3 begins minus Timestamp when Stage 1 begins
*   **Total Time to Full Recovery** = Timestamp when AWS API Cluster Writer Role Swap is observed minus Timestamp when Stage 1 begins





# Revised RDS Multi-AZ Failover Measurement Specification

This document details the refined measurement model for tracking Multi-AZ RDS failovers. Instead of expecting a single clean outage block, the system now embraces multi-outage "blips" by capturing **one disruption window** per test, recording two discrete recovery metrics, an interruption counter, and tail latency overhead.

---

## 1. The Core Metrics Model
Every disruption window now writes **one single row** to your results dataset, tracking three dimensions of the event:

### Metric 1: Recovery Time (`writable_downtime_ms`)
*   **What it measures:** The time it takes for the standby node to safely assume the primary role and start answering application queries. 
*   **How it is calculated:** The duration of the **first recorded outage** within the disruption window.
*   **Why this matters:** This keeps the metric perfectly backward-compatible with Docker restarts and single-AZ reboots, allowing direct baseline comparisons.

### Metric 2: Full Recovery Time (`full_recovery_ms`)
*   **What it measures:** The complete chronological span from the initial hard failure until the database cluster is 100% fully synchronized, stable, and high availability (HA) is restored.
*   **How it is calculated:** 
    \[\text{Full Recovery} = \text{Last Outage's FirstOKAfter} - \text{First Outage's LastOK}\]
*   **Why this matters:** This dynamically absorbs the hidden background replication catch-up phases, the synchronous write blips, and any healthy gaps between them.

### Metric 3: Interruption Count (`interruptions`)
*   **What it measures:** The absolute number of discrete outage events observed inside the single disruption window.
*   **Expected values:** For a clean Docker restart or simple reboot, this will equal `1`. For an RDS Multi-AZ failover, this will gracefully capture numbers > 1 (e.g., `3` due to secondary write blips) rather than causing a fatal test failure.

### Optional Extension: Extra Downtime (`extra_downtime_ms`)
*   **What it measures:** The cumulative sum of all secondary outages occurring *after* the initial main traffic block clears.
*   **Why this matters:** It distinguishes a provider that has a long, slow recovery tail from a provider that experiences several brief, sharp micro-blips.

---

## 2. Division of Labor: Control Plane vs. Data Plane

This model resolves the confusion between API tracking and probe tracking by defining strict boundaries:

*   **The Control Plane (Orchestration Driver):** Uses the `DescribeDBInstances` loop waiting for the `AvailabilityZone` string to change **strictly as a deadline/pacing mechanism**. It keeps the script alive long enough to ensure the test does not exit while the infrastructure is still shifting.
*   **The Data Plane (Probe Container):** Owns 100% of the recorded metrics. Because client-experienced latency is provider-agnostic, using the data plane makes your metrics completely portable to alternative platforms (e.g., CloudSQL, Neon) that do not expose an explicit "AZ Swap" API.

*Note: The probe-measured full recovery time (5m 38s) is far more precise than the API-measured swap time (~6m 32s), which carries a 1-minute overhead just for AWS internal metadata reflection.*

---

## 3. Assertion Changes & Guardrails

To support this new paradigm, your test evaluation logic must change:

### The Assertion Relaxed
```go
// OLD ASSERTION (Fails if blips occur)
if got := len(result.Writable.Outages); got != cfg.Repetitions

// NEW ASSERTION (Allows complex outages)
// Every disruption window must contain >= 1 outage. 
// A value of 0 means the disruption failed to trigger or was too fast to detect.
```

### The Settle Guardrail
Any outage that begins *before* the disruption window starts must cause a **fatal run failure**. This ensures that the baseline state was perfectly clean and stable before the failure injection occurred, preventing lingering environmental pollution from corrupting repetition results.

---

## 4. Expected Output Database Row Schema

When a Multi-AZ failover is executed, the probe container will now output a single, dense row matching this schema:

```json
{
  "repetition": 1,
  "disruption": "failover",
  "readable_downtime_ms": 32775.283,
  "writable_downtime_ms": 37034.418,
  "full_recovery_ms": 337983.0,
  "interruptions": 3,
  "extra_downtime_ms": 2547.0,
  "lost_commits": 0,
  "probe_failures": 142,
  "probe_errors": {
    "57P03": 65,
    "timeout": 77
  }
}
```
