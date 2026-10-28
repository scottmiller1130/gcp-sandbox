# gcp-sandbox

A playground for learning Google Cloud by building things with Terraform.
Each folder under `labs/` is self-contained: create it, poke at it, destroy it.

> For the production-style GKE platform, see [scottmiller1130/GCP](https://github.com/scottmiller1130/GCP).
> Nothing here touches that.

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
   Console → Billing → Budgets & alerts → create a $10 budget on this project.

## Running a lab

```bash
cd labs/01-storage-bucket
cp terraform.tfvars.example terraform.tfvars   # put your project ID in it
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
