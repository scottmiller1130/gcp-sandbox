# Terraform layout

The infrastructure is split the way most companies split theirs: **layers** that
change at different speeds and are owned by different people, **one state file
per layer and environment**, and **reusable modules** so each environment is a short file.

```
terraform/
├── bootstrap/        layer 0 · applied by a human · changes rarely
├── platform/         layer 1 · shared services · applied by CI
├── envs/
│   ├── dev/          layer 2 · one app environment per project · applied by CI
│   └── prod/
└── modules/          building blocks, no state of their own
    ├── project/            project factory: project + billing + APIs + service agents
    ├── cloud-run-service/  service + runtime identity + secret + deployer grants
    ├── revision-pruner/    Cloud Scheduler + Cloud Run Job
    └── run-observability/  uptime check, alerts, SLOs, dashboard
```

## Projects

```
 Sandbox billing account ($1 budget across all three)
 ├── gcp-sandbox-78tvq4        "shared": Terraform state, image registry, CI identities
 ├── gcp-sandbox-dev-78tvq4    hello-web (dev), max 3 instances
 └── gcp-sandbox-prod-78tvq4   hello-web (prod), max 10 instances, 5xx alert
```

A project per environment is the main isolation boundary in GCP. IAM, quotas, billing
and API settings are all per project, so a mistake in dev can't touch prod. Projects
are free. All three share the billing account's free tier.

## Layers

| Layer | State prefix | Contains | Applied by | Why it's separate |
|---|---|---|---|---|
| `bootstrap` | `bootstrap` | dev/prod projects, keyless GitHub auth, every CI identity, the state bucket, the budget | **you**, by hand | It creates CI's own permissions. If CI could apply it, CI could grant itself anything |
| `platform` | `platform` | Artifact Registry and who may push/pull | CI | Shared by every env. Changes affect all of them |
| `envs/dev`, `envs/prod` | `envs/<env>` | the app: service, secret, job, scheduler, monitoring | CI | Each env has its own blast radius and can be planned/applied on its own |

Layers read the layer below through `terraform_remote_state` (outputs only), never
by sharing state. One broken apply can't corrupt another layer's state, and a plan for
dev never locks prod.

## Who can do what

Every CI identity is keyless (Workload Identity Federation), and Google only accepts
tokens from this repo, pinned by numeric ID:

| Identity | Project | Usable from | Can |
|---|---|---|---|
| `tf-plan` | shared | any branch, incl. PRs | read all three projects and state (plans run with `-lock=false`) |
| `tf-apply` | shared | `main` only | own dev + prod, manage the registry, write state |
| `ci-builder` | shared | `main` only | push images to the registry |
| `ci-deployer` | each env | `main` only | release *its env's* `hello-web` and maintenance job |
| `hello-web-run` | each env | (Cloud Run) | read its own admin token, nothing else |
| `hello-web-maint` / `-scheduler` | each env | (Scheduler/Job) | prune revisions of that one service / start that one job |

`tf-apply` can't change the bootstrap layer, its own permissions, or anything in the
shared project except the registry.

## Day-to-day: change infrastructure through a PR

1. Branch, edit, e.g. set `max_instances = 5` in `terraform/envs/prod/main.tf`.
2. Open a PR. **Terraform** plans `platform`, `dev` and `prod` and posts **one risk review
   comment** (below). **CI** runs `fmt`, `validate`, `tflint` and the risk engine's tests.
3. Read the review. If anything is 🔴 CRITICAL and intended, add the `risk-accepted` label.
4. Merge. For each layer in order (`platform` → `dev` → `prod`), **Terraform** plans, re-scores
   that exact plan, refuses to apply a CRITICAL change without sign-off, applies the saved plan,
   then hits the live site. The first failure stops the rest, so a change that breaks dev never
   reaches prod.
5. Every Monday the drift check plans everything and **fails if the cloud doesn't match the
   code**, e.g. when someone changed something in the console.

App code goes through a separate pipeline (**Deploy**), so infrastructure and releases
don't block each other. See [cloud-run-app.md](cloud-run-app.md).

## Change safety: the risk review

Every infrastructure PR gets a comment like this, updated on each push:

> ## 🛡️ Terraform change review
> **Overall risk: 🟠 HIGH** · 3 changes (dev 1, prod 2) · AI: **⚠️ Merge with care**
>
> > Lowers prod's instance ceiling from 10 to 3 and tightens the startup probe…
> >
> > **Customer impact:** none until the next Deploy; after that, peak traffic above ~240
> > concurrent requests would queue…
>
> | Risk | Env | Change | Customer impact |
> |---|---|---|---|
> | 🟠 HIGH | prod | Change Cloud Run service `hello-web` in prod. | … |
>
> Each change then has a section with **what happens**, **customer impact**, **why this risk**,
> **rollback**, **verify after apply**, and the exact attributes changing (`old → new`).

It's built in two layers, on purpose:

| | Rules (`scripts/tf_risk.py`) | AI review (Claude Opus 5.5) |
|---|---|---|
| Decides | the risk *floor*, and **what's blocked** | explanations, customer impact, rollback, how to verify |
| Can change a risk | sets it | can **raise** it, never lower it |
| Deterministic | yes, and unit-tested (`scripts/test_tf_risk.py`) | no |
| If unavailable | always runs | the review still posts, rules only |

Rules are ordered by how directly customers feel a change:

| Category | Examples | Typical risk |
|---|---|---|
| **Customer path** | the service, public access, image pulls, the app's secret, enabled APIs, the project | delete/replace: 🔴 CRITICAL · scaling down, probes, memory, identity: 🟠 HIGH |
| **Delivery path** | keyless auth, CI identities, state access | breaking it: 🟠 HIGH, because you lose the fast way to fix customer problems |
| **Observability** | uptime check, alerts, SLOs | delete: 🟡 MEDIUM, because problems could go unnoticed |
| **Terraform state** | the state bucket | 🔴 CRITICAL, never lowered |
| **Cost / maintenance / other** | budget, pruning job, dashboards | mostly 🟢 LOW |

Dev changes are one level lower (it's pre-production), except state and CI plumbing. Broad role
grants (`owner`, `editor`) are at least HIGH anywhere. Cloud Run changes also say **when
customers get them**: right away (ingress, IAM, or traffic still on "latest"), or on the next
Deploy through the canary gates (template changes once CI pins traffic).

**What blocks:** any 🔴 CRITICAL change. The PR check fails and the apply refuses to run until
the `risk-accepted` label is on the PR. GitHub Free can't *require* a passing check before
merging a private repo, so the real enforcement is at apply time. The apply re-scores the plan
it's about to run, carries over the AI's escalations from the PR review, and treats anything
that wasn't in the reviewed plan as at least HIGH.

**Hard stops that no label overrides:** the Cloud Run service and the image registry have
`prevent_destroy`, so Terraform errors out on any plan that would delete or replace them.
Removing that line is itself a reviewed change.

**After every apply:** the live site in that environment gets 30 requests. More than 5% failing
fails the job, so later layers (prod) aren't applied.

### Setup

```bash
gh label create risk-accepted --color B60205 \
  --description "Reviewed the CRITICAL changes in the Terraform risk review; apply anyway"
gh secret set ANTHROPIC_API_KEY          # paste a key from console.anthropic.com
```

Without the secret, everything still works with rules only, and the comment says the AI review
was skipped.

**Cost:** the AI review runs once per push to a PR that changes infrastructure, and only when
the plan has changes. Adding or removing the label reuses the existing review. A typical PR with a
handful of changes is a few cents on Claude Opus 5.5 ($4 / $20 per million input/output tokens).
The very first plan of everything (~100 resources) is closer to $0.50. This is billed by
Anthropic, separately from the GCP free tier.

**Data sent to the API:** the resource addresses, types, changed attributes and the Terraform diff
of the PR. Values Terraform marks sensitive are replaced with `(sensitive)` before anything
leaves the runner, and the admin token never appears in plans at all (write-only attribute).

**Prompt injection:** PR content could try to talk the model into a low rating. It can't
work, because the AI can only *raise* a risk above the rules' floor, and only the rules decide what
blocks.

## Adding things

- **Another environment** (e.g. `stage`): add it to `environments` in
  `terraform/bootstrap/variables.tf` and apply bootstrap. Copy `envs/dev` to `envs/stage`
  and change `env`/`prefix`. Add `stage` to the workflow matrices. Watch the Scheduler
  free tier: 3 jobs per billing account, and dev + prod already use 2.
- **Another app**: call the three app modules again in each env file with a new `name`.
- **A new module input**: add it to the module's `variables.tf` with a default, so existing
  callers keep working.

## First-time setup

Done once, in this order. Steps 1–2 are already done for this sandbox.

```bash
# 1. State bucket (Terraform needs it before it can store anything)
gcloud storage buckets create gs://gcp-sandbox-78tvq4-tfstate --project gcp-sandbox-78tvq4 \
  --location us-central1 --uniform-bucket-level-access --public-access-prevention
gcloud storage buckets update gs://gcp-sandbox-78tvq4-tfstate --versioning

# 2. APIs Terraform itself needs before it can read the project
gcloud services enable cloudresourcemanager.googleapis.com cloudbilling.googleapis.com \
  --project gcp-sandbox-78tvq4

# 3. Bootstrap: projects, identities. Also imports the hand-made bucket and budget.
terraform -chdir=terraform/bootstrap init
terraform -chdir=terraform/bootstrap apply

# 4. Hand the outputs to GitHub Actions
scripts/set-github-variables.sh

# 5. First apply of the CI-managed layers (CI takes over after this)
terraform -chdir=terraform/platform init && terraform -chdir=terraform/platform apply
terraform -chdir=terraform/envs/dev init && terraform -chdir=terraform/envs/dev apply
terraform -chdir=terraform/envs/prod init && terraform -chdir=terraform/envs/prod apply
```

Then push. **Deploy** builds the app and releases it to dev and then prod.

## Tearing down

Projects are created with `deletion_policy = "PREVENT"`, and the Cloud Run service and image
registry have `prevent_destroy`, so `terraform destroy` can't delete an environment by accident.
To remove one on purpose, in a reviewed PR first set `deletion_policy` to `"DELETE"` in
`terraform/modules/project/main.tf` and delete the `prevent_destroy` lines, and apply that. Then
destroy in reverse order: envs → platform → bootstrap.
Deleted projects can be restored for 30 days.

## What a real enterprise adds on top

This layout uses the same patterns. These pieces are left out because they need things
a personal sandbox doesn't have:

| Missing piece | Needs | What it gives you |
|---|---|---|
| **Organization + folders** (e.g. `org / sandbox / {dev,prod}`) | a domain + Cloud Identity Free | inherited IAM and policies, central audit logs, no "orphan" projects |
| **Org policies** such as allowed regions, no public IPs, no service-account keys, domain-restricted sharing | an organization | guardrails no project owner can bypass |
| **Policy as code** in PRs (OPA/Conftest, Checkov) | rules tuned to your standards | blocks e.g. public buckets before they're planned |
| **Approval gates** before prod applies and deploys | GitHub Pro/Team (environment protection rules on private repos) | a human clicks "approve" for prod |
| **Shared VPC, private ingress, Cloud Armor, load balancer** | budget (LB ~$18/month) | private networking, WAF, multi-region |
| **Separate state buckets/projects per env** | more projects | prod state readable only by prod's identities |
