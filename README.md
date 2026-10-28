# gcp-sandbox

A playground for learning Google Cloud by building things with Terraform.
Each folder under `labs/` is self-contained: create it, poke at it, destroy it.

> For the production-style GKE platform, see [scottmiller1130/GCP](https://github.com/scottmiller1130/GCP).
> Nothing here touches that.

## Apps

- **[hello-web](docs/cloud-run-app.md)**: a public site on Cloud Run with CI/CD (canary, blue/green, rollback)
  and load testing, released dev → prod. Code in `app/`, pipelines in `.github/workflows/`.
- **[Terraform layout](docs/terraform.md)**: bootstrap, platform and per-environment layers, reusable
  modules, and plan-on-PR / apply-on-merge.

## One-time setup

1. **Install tools**
   ```bash
   brew install --cask google-cloud-sdk
   brew install terraform   # already installed if `terraform version` works
   ```
2. **Log in** (opens a browser)
   ```bash
   gcloud auth login
   gcloud auth application-default login   # lets Terraform use your login
   ```
3. **Create a sandbox project** (keep it separate from anything real)
   ```bash
   export PROJECT_ID=sandbox-$RANDOM        # must be globally unique
   gcloud projects create $PROJECT_ID
   gcloud billing accounts list
   gcloud billing projects link $PROJECT_ID --billing-account=XXXXXX-XXXXXX-XXXXXX
   gcloud config set project $PROJECT_ID
   ```
4. **Set a budget alert** so a forgotten VM can't surprise you:
   Already done for `gcp-sandbox-78tvq4`: a $1 budget (see **Staying free** below).

## Staying free

Projects: **`gcp-sandbox-78tvq4`** (shared, also used by the labs), plus `gcp-sandbox-dev-78tvq4` and
`gcp-sandbox-prod-78tvq4` for the app. All are on the **Sandbox** billing account, with one $1 budget that
emails at 10¢, 50¢, $1 and if spend is *forecast* to pass 10¢.

The labs are built to fit the [always-free tier](https://cloud.google.com/free/docs/free-cloud-features):

- Regions are locked to `us-central1`, `us-east1`, `us-west1` (Terraform rejects others).
- **One** e2-micro VM at a time, with a `pd-standard` disk up to 30 GB. A second VM, a bigger
  machine type or an SSD/balanced disk costs money.
- No public IPs on VMs - an in-use external IPv4 address is billed. SSH goes through IAP (free).
- Cloud Storage: up to 5 GB of Standard storage. Cloud Run: scales to zero, well under 2M requests/month.

Things that cost money, so avoid them here unless you accept a small bill: Cloud NAT, load
balancers, Cloud SQL, reserved static IPs, egress beyond 1 GB/month, and GKE. The free tier
waives the management fee for one GKE cluster, but its nodes (or Autopilot pods) are still billed.

A budget only *alerts* - it does not stop spending. **Always `terraform destroy` when you finish a lab.**

## Running a lab

```bash
cd labs/01-storage-bucket
cp terraform.tfvars.example terraform.tfvars   # already set to the sandbox project
terraform init
terraform plan      # see what will be created
terraform apply     # create it
# ...explore in the console / CLI...
terraform destroy   # tear it down when done
```

## Labs

| Lab | What you build | Rough cost |
|---|---|---|
| `01-storage-bucket` | Cloud Storage bucket + an uploaded file | ~free |
| `02-compute-vm` | e2-micro VM with SSH via IAP (no public IP) | free tier in us-central1 |
| `03-cloud-run` | Public "hello" container on Cloud Run | ~free (scales to zero) |

All resources are labeled `env = sandbox`, so you can find strays with:
```bash
gcloud asset search-all-resources --scope=projects/$PROJECT_ID --query="labels.env=sandbox"
```

## Nuke everything

Deleting the project removes every resource in it (recoverable for 30 days):
```bash
gcloud projects delete $PROJECT_ID
```
