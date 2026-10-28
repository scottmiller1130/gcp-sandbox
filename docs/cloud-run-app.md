# hello-web on Cloud Run

A small public website deployed to Cloud Run through GitHub Actions, with canary,
blue/green and direct releases, automatic rollback, and a load test. It's built to
stay inside the GCP free tier.

```
 push to main / Run workflow
            │
            ▼
 ┌─ GitHub Actions ─────────────────────────────────────────────┐
 │ test → build → push image ──► Artifact Registry (apps/)      │
 │        │                                                     │
 │        ▼  (keyless auth: Workload Identity Federation)       │
 │ deploy revision with 0% traffic, tag "candidate"             │
 │ smoke test candidate URL ── fail ──► stop, prod untouched    │
 │        │                                                     │
 │        ├─ canary:     10% → check → 50% → check → 100%       │
 │        │                 └── fail ──► 100% back to previous  │
 │        ├─ blue-green: green ready on its own URL → Promote   │
 │        └─ direct:     100% → check                           │
 └──────────────────────────────────────────────────────────────┘
            │
            ▼
 Cloud Run service hello-web (us-central1, 0-10 instances)
   https://hello-web-….run.app            ← production traffic split
   https://candidate---hello-web-….run.app ← newest revision
   https://previous---hello-web-….run.app  ← rollback target
```

## Layout

| Path | What |
|---|---|
| `app/` | Go web server + Dockerfile (distroless, non-root, ~17 MB) |
| `infra/` | Terraform: Cloud Run, Artifact Registry, service accounts, GitHub trust |
| `.github/workflows/` | `ci`, `deploy`, `promote`, `rollback`, `loadtest` |
| `infra/secrets.tf` | Secret Manager: the `/admin` token |
| `infra/observability.tf` | Uptime check, alerts, SLOs, dashboard |
| `infra/scheduler.tf` | Cloud Scheduler + Cloud Run Job that prunes old revisions |
| `scripts/verify.sh` | Health gate used by every release step |
| `loadtest/k6.js` | k6 load profile |

## App endpoints

| Path | Purpose |
|---|---|
| `/` | The web page. Shows version, revision and instance; each version has its own accent colour |
| `/api/info` | Same as JSON. CI uses it to see which revision answered |
| `/work?ms=200` | Waits N ms (max 1000) before answering, so load tests can force scaling |
| `/healthz` | Startup and liveness probe |
| `/admin` | Instance internals (uptime, request count, memory). Needs the bearer token from Secret Manager |

Every response carries `X-Revision` and `X-Instance` headers.

## Releasing

Pushing a change under `app/` to `main` runs **Deploy** with a canary. To pick a
strategy, go to **Actions → Deploy → Run workflow**:

- **canary**: shifts traffic through `canary_steps` (default `10 50 100`). At each
  step it sends 300 requests and fails if more than 5% of the *new revision's*
  responses are errors, then waits 30 s. A failure sends 100% back to the previous revision.
- **blue-green**: the new revision ("green") gets a URL of its own and 0% of traffic.
  Click around it, load test it (`target: candidate`), then run **Promote**. Promote
  checks green, switches 100% to it, and switches back if the check fails.
- **direct**: goes straight to 100% after the smoke test.

**Rollback** (Actions → Rollback) sends 100% back to the revision tagged `previous`.
Nothing gets rebuilt, so it takes seconds.

### Practise failures

Deploy has an `error_rate` input that makes the new revision fail that fraction of
requests (health checks keep passing):

| `error_rate` | What you'll see |
|---|---|
| `1` | Smoke test on the candidate URL fails. Production never sees it |
| `0.1` | Smoke test passes (≤20%), the canary's 10% step catches it (>5%) and rolls back |
| `0` | Normal release. Also clears a previous injected rate |

Watch it live from a terminal while a deploy runs:

```bash
while true; do curl -s https://<service-url>/api/info | jq -r .revision; sleep 0.2; done
```

## Load testing

**Actions → Load test → Run workflow.** It runs k6 from a GitHub runner, then reads
Cloud Monitoring to report the peak instance count in the run summary.

- With the defaults (50 users, `/work?ms=200`, concurrency 80) one or two instances can
  handle the load.
- To watch it scale out, lower `concurrency` in `infra/variables.tf` to `5` and
  apply. 50 users then need ~10 instances, which hits `max_instances`, and latency
  rises because requests queue.
- Try `path: /` to see how much a single instance handles when requests are cheap.

Inputs are capped (200 users, 10 minutes, `max_instances` ≤ 20). A default run is
~25k requests, about 1% of Cloud Run's monthly free requests.

## Security

- **No keys.** GitHub Actions gets short-lived tokens through Workload Identity
  Federation. Trust is pinned to this repo's numeric ID and the `main` branch, so
  workflows on PRs, forks and other branches can't authenticate at all.
- **Least privilege.** The deployer can push images, read Cloud Run, and change
  *only* the `hello-web` service and its maintenance job. The app's runtime identity can
  read only its own secret.
- **Hardened image.** Distroless (no shell or package manager), non-root, static
  binary, deployed by digest.
- **Hardened responses.** Strict CSP (no scripts, only the page's own stylesheet by hash),
  HSTS, nosniff, no framing, no referrer. Unknown paths return 404 and non-GET methods return 405.
- **CI gates.** gofmt, vet, race-enabled tests, `govulncheck`, shellcheck, actionlint,
  terraform validate. Dependabot keeps actions, Go, base images and providers current.

## Secret Manager

The `/admin` endpoint is protected by a bearer token stored in Secret Manager as
`hello-web-admin-token`.

- **Terraform generates the token** with an *ephemeral* resource and writes it with a
  *write-only* attribute, so it never appears in the Terraform state or plan.
- **Only the app's runtime identity can read it**, and that's the only permission the runtime identity has.
- **Cloud Run injects it** as `ADMIN_TOKEN` when each instance starts, using the `latest` version.

```bash
curl -s -H "Authorization: Bearer $(gcloud secrets versions access latest --secret hello-web-admin-token)" \
  https://<service-url>/admin | jq
```

**Rotate it:** bump `admin_token_version` in `infra/variables.tf`, `terraform apply`
(a new version is created *before* the old one is destroyed), then run **Deploy**
(`direct`) so new instances start with the new token. Old instances keep the old token
until Cloud Run replaces them.

Free tier: 6 active versions and 10k accesses per month. Rotation replaces the version
rather than adding one, and each instance start counts as one access.

## Observability

| What | Where |
|---|---|
| **Dashboard** with requests, 5xx and p95 latency *by revision*, instances, uptime, maintenance runs | `terraform -chdir=infra output dashboard_url` |
| **Logs** as structured JSON, each line linked to its request's trace | Logging → Logs Explorer, `resource.labels.service_name="hello-web"` |
| **Traces**: Cloud Run traces incoming requests automatically | Trace Explorer (open a trace to jump to its logs) |
| **Error Reporting**: injected failures are reported grouped by *version* | Error Reporting |
| **Uptime check** on `/` every 5 min from Oregon, Iowa and Virginia | Monitoring → Uptime checks |
| **Alerts**: site down (2+ locations failing) and 5xx rate above 5% | Monitoring → Alerting |
| **SLOs**: 99% availability and 95% of requests under 1s, both over a rolling 7 days | Monitoring → Services → hello-web |

Alerts open incidents in the console. To get emailed too, set `alert_email` (e.g.
`terraform -chdir=infra apply -var alert_email=you@example.com`).

Try it: run **Deploy** with `error_rate: 0.1` and `strategy: blue-green`, then load test
`target: candidate`. The dashboard shows the 5xx line for the green revision only,
Error Reporting groups the failures under green's version, and the 5xx alert fires.
Production's SLO isn't affected because green receives no production traffic.

Cost: uptime checks are free up to 1M runs/month (this one uses ~26k). Alerting is free
until at least Sept 2027. After that the uptime alert stays free and the 5xx alert would be
~$0.35/month (`enable_error_rate_alert = false` turns it off). Dashboards, SLOs, Trace and
Error Reporting are free at this scale.

## Scheduled maintenance (Cloud Scheduler + Cloud Run Jobs)

Every deploy leaves a revision behind. At 04:00 (America/Denver) each day, Cloud Scheduler
starts the `hello-web-maintenance` **Cloud Run Job**. It runs the same image as
`server maintenance` and deletes old revisions. It always keeps:

- the newest `revisions_to_keep` (5), and
- any revision serving traffic, any tagged revision (`previous`, `candidate`, `green`),
  and the latest ready revision, so a rollback target is never deleted.

Identities: the **scheduler** account can only start this job. The **job** account can
only manage revisions of `hello-web`. CI updates the job's image after every release.

```bash
gcloud run jobs execute hello-web-maintenance --region us-central1 --wait      # run now
gcloud run jobs execute hello-web-maintenance --region us-central1 \
  --update-env-vars DRY_RUN=true --wait                                         # preview only
gcloud scheduler jobs run hello-web-maintenance-daily --location us-central1   # test the trigger
```

Free tier: Scheduler gives 3 jobs per billing account (this uses 1). The job runs for
a few seconds a day.

## How infrastructure changes roll out

Terraform owns the service's *shape*: scaling, probes, identity and secrets. CI owns
the image, env vars and traffic split. A Terraform change to the service creates a new
revision that gets **0% traffic**, because CI pins traffic to specific revisions. The next
**Deploy** builds on the latest config, so your change rolls out through the same
canary gates as code changes.

## Resilience

- Cloud Run spreads instances across zones in the region automatically.
- A revision only gets traffic after `/healthz` passes (startup probe). Instances that
  stop answering get replaced (liveness probe).
- Graceful shutdown on SIGTERM: in-flight requests finish before the instance stops.
- Releases are gated and reversible, and the old revision stays deployed.
- Server timeouts and a 10 s request timeout keep slow clients from tying up instances.

**Not covered, because it isn't free:** surviving the loss of a whole region needs Cloud Run in two
regions behind a global external Application Load Balancer (~$18/month). `min_instances = 0`
also means the first request after an idle period waits for a cold start of a few hundred ms.
Setting it to 1 removes that, but you pay for an idle instance.

## Cost guardrails

- `max_instances = 10`, CPU only billed during requests, 256 MiB memory
- Artifact Registry keeps the 5 newest images and deletes the rest after 7 days (free tier is 0.5 GB)
- Terraform state bucket keeps 3 old versions
- $1 budget alert on the project

## First-time setup (already done for this sandbox)

```bash
# State bucket
gcloud storage buckets create gs://gcp-sandbox-78tvq4-tfstate --location us-central1 \
  --uniform-bucket-level-access --public-access-prevention
gcloud storage buckets update gs://gcp-sandbox-78tvq4-tfstate --versioning

# Infrastructure
terraform -chdir=infra init
terraform -chdir=infra apply

# Hand the outputs to GitHub Actions
terraform -chdir=infra output -json github_variables \
  | jq -r 'to_entries[] | "\(.key) \(.value)"' \
  | while read -r k v; do gh variable set "$k" --body "$v"; done
```

Then run **Deploy** once to replace the placeholder image with the real app.
