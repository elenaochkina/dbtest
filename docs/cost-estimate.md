# How the cost estimate works

How the console estimates what an AWS recovery run costs, from the form's confirm step to
the line on screen.

## Diagram

```
cmd/dbtest/main.go
│  prices := pricing.New(...)                    ── one Price List client per session
│  console.New(serviceLauncher, logs, prices)    ── hands it to the console
▼
┌──────────────────────── internal/console ─────────────────────────┐
│                                                                    │
│  keys.go                                                           │
│    form arrives at "confirm" → m.fetchPricesFor(form.cfg)          │
│        │                                                           │
│        ▼                                                           │
│  prices.go                                                         │
│    fetchPricesFor(cfg):                                            │
│      docker?                       → nothing                       │
│      key = "aurora db.r6g.large"                                   │
│      m.prices[key] fetched/running → nothing                       │
│      otherwise                     → background fetch ──────┐      │
│                                                             │      │
│    setPrices(pricesMsg)  ← result arrives ◄─────────────────┤      │
│      m.prices[key] = prices (or the error)                  │      │
│                                                             │      │
│    estimateLine():                                          │      │
│      m.prices[key] → HourlyCost × estimatedMinutes → "~$0.06"      │
│        │                                                    │      │
│        ▼                                                    │      │
│  recovery_form.go  lines(estimate) → shown under the summary│      │
└─────────────────────────────────────────────────────────────┼──────┘
                                                              │
┌──────────────────────── internal/pricing ───────────────────┼──────┐
│  pricing.go   Client.Fetch(provider, class) ◄───────────────┘      │
│                 Price List API (us-east-1) for AWS_REGION prices   │
│                 → Prices{"InstanceUsage:db.r6g.large": 0.26, ...}  │
│                                                                    │
│  cost.go      InstanceClass(provider, request) ── asks provider/aws│
│               HourlyCost(prices, provider, request)                │
│               Cost(dollarsPerHour, minutes)                        │
└────────────────────────────────────────────────────────────────────┘
```

## Each file's job

| File | Package | Job |
|---|---|---|
| `main.go` | main | Creates one pricing client and gives it to the console. |
| `pricing.go` | pricing | `Client.Fetch`: searches the Price List API for one provider and instance class, and returns a `Prices` map. |
| `cost.go` | pricing | `InstanceClass`, `HourlyCost`, `Cost`: the math. No API calls. |
| `rds.go`, `aurora.go` | provider/aws | `RDSInstanceClass`, `AuroraInstanceClass`, `RDSStorageGiB`: the size Provision creates for a request. |
| `prices.go` | console | The in-memory map: fetch when needed, store the result, build the estimate line. |
| `keys.go` | console | Starts the fetch when the form arrives at confirm. |
| `recovery_form.go` | console | Shows the estimate line under the confirm summary. |

## Step by step

1. **Request to class.** The form holds a size (vCPU, memory), but AWS prices are per instance
   class. `pricing.InstanceClass` asks `provider/aws`, which picks the smallest class that fits,
   or the `AWS_*_INSTANCE_CLASS` override. Provision uses the same tables, so the estimate
   prices the class that is actually created.
2. **Fetch.** `Client.Fetch` runs two or three exact searches: the instance class, RDS storage
   (RDS only), and ARM Fargate. Each returns 2–7 products. The API runs only in a few regions,
   so the client calls us-east-1 and filters by `AWS_REGION`.
3. **Parse.** Each product is a JSON document. `product` keeps only the usage type and the
   on-demand USD price, and `withoutRegion` drops the region code: `USW2-InstanceUsage:db.r6g.large`
   becomes `InstanceUsage:db.r6g.large`.
4. **Cache.** The console stores each result in `m.prices` under `"<provider> <class>"`. The
   same choice again uses the map with no API call. A failed fetch is tried again on the next
   confirm. The map lives until the console quits.
5. **Price per hour.** `HourlyCost` adds:
   - the instance: `InstanceUsage:<class>`, or `Multi-AZUsage:<class>` for RDS with HA, and two
     instances for Aurora with HA
   - RDS storage: GiB × GB-month price ÷ 730
   - Fargate: 1.25 vCPU and 2.5 GB (probe and bench, from `terraform/aws/taskdefs.tf`)
6. **Duration.** `estimatedMinutes` = 5 (provision) + settle + repetitions × (1 + settle) + 1
   (deprovision). The defaults give about 11 minutes.
7. **Cost.** `Cost` multiplies the price per hour by the minutes, with RDS and Aurora's
   10-minute minimum.

## Timing

| Fetch | Time |
|---|---|
| First in a session | ~1.2 s, mostly the SSO login and role switch |
| A new class later | ~0.1–0.4 s, the client keeps its credentials |
| A class already in the map | 0 s, no API call |

## What the confirm line shows

| State | Line |
|---|---|
| fetch running | `estimated cost     checking…` |
| done | `estimated cost     ~$0.06  (db.r6g.large, ~11 min)` |
| fetch failed | `estimated cost     unavailable (see log)` |
| no pricing client | `estimated cost     unavailable` |
| docker | no line |

## Permission

`Client.Fetch` needs `pricing:GetProducts`. The `ReadPrices` statement in
`terraform/bootstrap/dbtest-run.json` gives it to the `dbtest` role.

## Limits

- List prices only: no free tier, credits or discounts.
- Leaves out Aurora storage and I/O, CloudWatch logs, data transfer and public IPv4 addresses.
- Counts both Fargate tasks for the whole run; the bench task runs only briefly.
- The duration is a guess. The real bill appears in Cost Explorer about 24 hours later.
