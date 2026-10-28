# hello-web on Cloud Run

A small public website deployed to Cloud Run through GitHub Actions, with canary,
blue/green and direct releases, automatic rollback, and a load test. It's built to
stay inside the GCP free tier.

```
 push to main / Run workflow
            │
            ▼
 ┌─ GitHub Actions ───────────────────────────────────────────────────┐
 │ test → build ONCE → push image ──► Artifact Registry (shared proj) │
 │                │                                                   │
 │                ▼  same digest, keyless auth per environment        │
 │  dev:  candidate at 0% → smoke test → 100% → check                 │
 │                │ (prod only starts if dev passed)                  │
 │                ▼                                                   │
 │  prod: candidate at 0% → smoke test → canary 10 → 50 → 100         │
 │                              any failed gate ──► back to previous  │
 └────────────────────────────────────────────────────────────────────┘

 Each environment is its own GCP project with its own hello-web service:
   https://hello-web-….run.app            ← traffic split
   https://candidate---hello-web-….run.app ← newest revision
   https://previous---hello-web-….run.app  ← rollback target
```

Infrastructure layout and the Terraform pipeline are in [terraform.md](terraform.md).

## Layout

| Path | What |
|---|---|
| `app/` | Go web server + Dockerfile (distroless, non-root, ~17 MB) |
| `terraform/` | Infrastructure in layers: see [terraform.md](terraform.md) |
| `.github/workflows/` | `ci`, `deploy`, `promote`, `rollback`, `loadtest` |
| `terraform/modules/cloud-run-service` | Service, runtime identity, Secret Manager admin token |
| `terraform/modules/run-observability` | Uptime check, alerts, SLOs, dashboard |
| `terraform/modules/revision-pruner` | Cloud Scheduler + Cloud Run Job that prunes old revisions |
| `scripts/verify.sh` | Health gate used by every release step |
| `loadtest/k6.js` | k6 load profile |

## App endpoints

| Path | Purpose |
|---|---|
| `/` | **Mission Control**: a live view of the service's internals (below) |
| `/api/stats` | What this instance is doing: identity, uptime, requests, in-flight, status counts, latency, recent requests and log lines. Powers Mission Control |
| `/api/info` | Version, revision and instance as JSON. CI uses it to see which revision answered |
| `/work?ms=200` | Waits N ms (max 1000) before answering, so load tests can force scaling |
| `/healthz` | Startup and liveness probe |
| `/admin` | Instance internals. Needs the bearer token from Secret Manager |
| `/static/*`, `/theme.css` | Mission Control's script and styles, plus this version's accent colour |

## Mission Control

The homepage shows what's happening inside the service, live:

- **Your request's journey**: a waterfall of your page load (DNS, TLS, Google's front end, the app's
  own code, download). It comes from the browser's Navigation Timing plus the `Server-Timing: app;dur=…`
  header every response carries. It also shows the request's Cloud Trace ID, with a link for project owners.
- **Rollout radar**: which revision answered your last 40 polls. Watch it move 10% → 50% → 100% during a canary.
- **Instance fleet**: one card per instance you've reached, with a friendly name, uptime, requests,
  in-flight load (out of 80), latency, status counts and memory. Cards go to sleep when Cloud Run scales them in.
- **Summon the swarm**: up to 100 parallel `/work` requests from your browser, with the result broken
  down by the instances that answered.
- **Live request feed** and **instance logs**: each instance keeps its last 60 requests and 40 log lines
  in memory.
- **Achievements**: for meeting two instances, catching a canary, seeing a cold start, and so on.

**Privacy and safety:** the feed shows method, path and status only. Query values are dropped (except
`/work?ms=`), and non-ASCII characters become `?`. Log lines keep an allow-list of attributes: no IPs,
trace IDs or headers. The page renders everything with `textContent`, and the CSP allows scripts and
styles only from the app's own files (`script-src 'self'`, no inline code).

**Cost:** the page polls `/api/stats` every 2 s and stops when the tab is hidden. It uses no long-lived
connections, which would keep an instance billed. One viewer for an hour is about 1,800 tiny requests, and
Cloud Run's free tier is 2 million a month.

Every response carries `X-Revision` and `X-Instance` headers.

## Releasing

Pushing a change under `app/` to `main` builds the image once, releases it to
**dev** (direct, smoke-tested), and then to **prod** with a canary. Prod never runs an
image that didn't pass in dev. To choose how it goes, open **Actions → Deploy → Run workflow**:

- **target**: `prod` (dev, then prod) or `dev` (stop after dev, using your strategy there)
- **strategy**: used in the last environment:
  - **canary**: shifts traffic through `canary_steps` (default `10 50 100`). At each
    step it sends 300 requests and fails if more than 5% of the *new revision's*
    responses are errors, then waits 30 s. A failure sends 100% back to the previous revision.
  - **blue-green**: the new revision ("green") gets a URL of its own and 0% of traffic.
    Click around it, load test it (`target: candidate`), then run **Promote**. Promote
    checks green, switches 100% to it, and switches back if the check fails.
  - **direct**: goes straight to 100% after the smoke test.

**Rollback** (Actions → Rollback, pick the environment) sends 100% back to the revision
tagged `previous`. Nothing gets rebuilt, so it takes seconds.

### Practise failures

Deploy has an `error_rate` input that makes the new revision fail that fraction of
requests, **in the last environment only**, so dev stays healthy on the way to prod (health checks keep passing):

| `error_rate` | What you'll see |
|---|---|
| `1` | Smoke test on the candidate URL fails. Users never see it |
| `0.1` | Smoke test passes (≤20%), the canary's 10% step catches it (>5%) and rolls back |
| `0` | Normal release. Also clears a previous injected rate |

Watch it live from a terminal while a deploy runs:

```bash
while true; do curl -s https://<service-url>/api/info | jq -r .revision; sleep 0.2; done
```

## Load testing

**Actions → Load test → Run workflow** (pick dev or prod). It runs k6 from a GitHub runner, then reads
Cloud Monitoring to report the peak instance count in the run summary.

- With the defaults (50 users, `/work?ms=200`, concurrency 80) one or two instances can
  handle the load.
- To watch it scale out, set `concurrency = 5` on the `app` module in
  `terraform/envs/prod/main.tf` and merge the change (the Terraform workflow applies it). 50 users then need ~10 instances, which hits `max_instances`, and latency
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
curl -s -H "Authorization: Bearer $(gcloud secrets versions access latest --project <env project> --secret hello-web-admin-token)" \
  https://<service-url>/admin | jq
```

**Rotate it:** bump `admin_token_version` in `terraform/envs/<env>/main.tf`, merge (Terraform applies it;
a new version is created *before* the old one is destroyed), then run **Deploy**
(`direct`) so new instances start with the new token. Old instances keep the old token
until Cloud Run replaces them.

Free tier: 6 active versions and 10k accesses per month. Rotation replaces the version
rather than adding one, and each instance start counts as one access.

## Observability

| What | Where |
|---|---|
| **Dashboard** with requests, 5xx and p95 latency *by revision*, instances, uptime, maintenance runs | `terraform -chdir=terraform/envs/prod output dashboard_url` |
| **Logs** as structured JSON, each line linked to its request's trace | Logging → Logs Explorer, `resource.labels.service_name="hello-web"` |
| **Traces**: Cloud Run traces incoming requests automatically | Trace Explorer (open a trace to jump to its logs) |
| **Error Reporting**: injected failures are reported grouped by *version* | Error Reporting |
| **Uptime check** on `/` every 5 min from Oregon, Iowa and Virginia | Monitoring → Uptime checks |
| **Alerts**: site down (2+ locations failing) and 5xx rate above 5% | Monitoring → Alerting |
| **SLOs**: 99% availability and 95% of requests under 1s, both over a rolling 7 days | Monitoring → Services → hello-web |

Alerts open incidents in the console. To get emailed too, set the `alert_email`
variable's default in `terraform/envs/<env>/main.tf`. Dev has no 5xx alert, because releases there
are smoke-tested before prod and alerts in dev would be noise.

Try it: run **Deploy** with `error_rate: 0.1` and `strategy: blue-green`, then load test
`target: candidate`. The dashboard shows the 5xx line for the green revision only,
Error Reporting groups the failures under green's version, and the 5xx alert fires.
Prod's SLO isn't affected because green receives no user traffic.

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
export CLOUDSDK_CORE_PROJECT=<env project>                                     # dev or prod
gcloud run jobs execute hello-web-maintenance --region us-central1 --wait      # run now
gcloud run jobs execute hello-web-maintenance --region us-central1 \
  --update-env-vars DRY_RUN=true --wait                                         # preview only
gcloud scheduler jobs run hello-web-maintenance-daily --location us-central1   # test the trigger
```

Free tier: Scheduler gives 3 jobs per billing account (dev + prod use 2). The job runs for
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

## First-time setup

See [terraform.md](terraform.md#first-time-setup).
