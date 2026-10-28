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
2. Open a PR. **Terraform** plans `platform`, `dev` and `prod` and posts each plan as a
   PR comment. **CI** runs `fmt`, `validate` and `tflint`.
3. Review the plan, then merge. **Terraform** applies `platform` → `dev` → `prod` in order,
   stopping at the first failure, so a change that breaks dev never reaches prod.
4. Every Monday the drift check plans everything and **fails if the cloud doesn't match the
   code**, e.g. when someone changed something in the console.

App code goes through a separate pipeline (**Deploy**), so infrastructure and releases
don't block each other. See [cloud-run-app.md](cloud-run-app.md).

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

Projects are created with `deletion_policy = "PREVENT"`, so `terraform destroy` can't
delete an environment by accident. To remove one on purpose, set it to `"DELETE"` in
`terraform/modules/project/main.tf` and apply. Then destroy in reverse order: envs → platform → bootstrap.
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
