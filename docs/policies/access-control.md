# Access control policy

**Principles:** least privilege, no shared accounts, no long-lived keys, every grant approved and
recorded.

| Access | How it's granted | Evidence |
|---|---|---|
| GitHub write | Owner only; automation through the GitHub App (1-hour tokens, this repo only) | Repo settings, App installation |
| Merge to `main` | Branch protection: PR, required checks, approval by a code owner who isn't the last pusher | PR reviews, merge audit workflow |
| Deploy / apply to prod | `prod` environment: approval by the owner | Deployment approvals, change ledger |
| Google Cloud, CI | Keyless Workload Identity Federation pinned to this repo's ID; apply and deploy only from `main` | `terraform/bootstrap/identity.tf` |
| Google Cloud, humans | Owner's Google account with 2-step verification (security key) | Google account settings |
| Customer data | The app's runtime identity and the backup job only; the plan identity is denied by an IAM deny policy | `terraform/bootstrap/audit.tf` |

**Requests:** open an [access request](../../.github/ISSUE_TEMPLATE/access-request.yml) issue. The owner
approves by changing the Terraform in a PR (bootstrap for identities), so the grant is reviewed,
recorded and reversible.

**Quarterly review:** the owner lists who and what has access (GitHub collaborators and App
installations, `gcloud projects get-iam-policy` for each project, service account keys = none),
removes anything unneeded, and records the review as a closed issue labelled `access-review`.

**Removal:** access is removed the same day it's no longer needed, through a PR.
