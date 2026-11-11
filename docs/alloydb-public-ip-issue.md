# AlloyDB HA: the public IP stops working after a second failover

## What happens

- On an HA AlloyDB instance, the first failover works normally. After the
  second one (the primary moves back to its original zone, onto a rebuilt
  node), the instance's public IP stops answering: connections time out on
  port 5432 and on the Auth Proxy port, 5433.
- The database itself is healthy: AlloyDB reports `READY`, and clients on the
  private IP connect normally.
- The public IP, the network settings and the allow list don't change, so
  AlloyDB shows everything as correctly configured.
- Reproduced in 6 of 6 runs, including a crash run.

## What we tried

- Re-applying the allow list (as project owner): the update took 12 minutes
  and didn't help.
- Turning public IP off and on: two 3-minute updates. AlloyDB released the old
  address and assigned a new public IP, which worked immediately. This was
  tested once.

## Impact on dbtest

- The probe and bench run inside the cloud over the private IP, so outage
  measurements aren't affected.
- The readiness check (`WaitForReady`) connected from the laptop over the
  public IP, so AlloyDB HA runs failed after the second switch.

## Fix (follow-up PR, not in this one)

- `WaitForReady` now checks readiness from the cloud's runner over the private
  IP: Cloud Run on GCP, Fargate on AWS, Docker locally.
- After that change, all 7 runs on AWS and GCP completed, including the first
  full AlloyDB HA runs. Each check adds about 25–40 seconds, outside the
  measured outage.
- The off/on workaround isn't suitable for dbtest: it takes about 6 minutes and
  changes the public IP.

## Open question

- It looks like an AlloyDB bug. Is it worth a Google support case? The evidence
  is ready: cluster IDs, timings, public versus private results, and the off/on
  result.
