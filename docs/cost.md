# Cost impact

The sandbox's rule is **completely free**. Every pull request gets a **💸 Cost impact** comment
(`.github/workflows/cost.yml`, `scripts/cost.py`) that answers: *does this still fit the free tier,
how much headroom is left, and what's the worst case in dollars if it doesn't?*

## What it looks at

**1. Worst-case use of each free tier, on main and on the PR.** The cost drivers are read straight
from the code, so changing any of them shows up:

| Free tier | Driven by | Free |
|---|---|---|
| Firestore reads / writes per day | prod's `max_instances`, `listTTL`, `statusTTL`, `listLimit`, `dailySignCap`, `instanceReadCap` | 50k / 20k per project |
| Cloud Scheduler jobs | every `google_cloud_scheduler_job` × the environments using it | 3 per billing account |
| Secret versions | `google_secret_manager_secret_version` × environments | 6 |
| Image storage | Artifact Registry `keep_count` × ~12 MB | 0.5 GB |
| Always-on instances | `min_instance_count` | none: billed every second |
| Free viewing hours | Mission Control's `POLL_MS` and `FLIGHT_POLL_MS`, plus CPU and memory per instance | 2M requests, 180k vCPU-s, 360k GiB-s a month |

"Worst case" means every limit is hit at once: all instances running all day, every cap used up.
Real use is a tiny fraction of that, but if the worst case fits, the bill can't surprise you.

**2. Anything billable the PR adds.** Load balancers, static IPs, Cloud SQL, AlloyDB, Spanner, Redis,
GKE nodes, VPC connectors, Cloud NAT, KMS keys, VMs beyond the free e2-micro, managed Firestore
backups, a second (named) Firestore database, point-in-time recovery, buckets outside the free
regions or the Standard class, and log retention past 30 days.

**3. What a model can't count.** Claude reads the diff and the model's numbers, looking for new API
calls, extra log volume, retry loops, egress, or anything that lets one visitor drive unbounded use.
It can raise the risk, never lower it.

## The gate

| Finding | Risk |
|---|---|
| Adds something billable, or a worst case passes its free tier | 🔴 CRITICAL: **blocks the merge** |
| A worst case reaches 80% of its free tier | 🟠 HIGH |
| A worst case grows > 25%, or free viewing hours drop > 20% | 🟡 MEDIUM |

A blocked PR passes once someone adds the **`cost-accepted`** label. That's a deliberate "yes, we're
choosing to spend money", the cost equivalent of `risk-accepted`. The $1 budget alert stays as a
backstop for anything the model misses.

## Today

Everything fits. The tightest free tier is Firestore reads: about 34k of the 50k a day *at the
absolute worst case* (prod's 10 instances all viewed non-stop for 24 hours).

## Keeping it accurate

Prices and free tiers live at the top of `scripts/cost.py`, with the date they were checked. Google
doesn't publish Firestore's `nam5` Standard rates alongside the regional ones, so they're assumed to be
2× us-central1. To track a new cost driver, read it in `load()` and add a `Metric`. Tests:
`python3 -m unittest scripts/test_cost.py`.
