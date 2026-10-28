# Security and SOC 2 readiness

An audit of the whole setup (October 2026), the controls in place, and what's being added.
Status: ✅ in place · 🔧 in progress · 📋 decision or document needed.

## Encryption

| | Control | Evidence |
|---|---|---|
| ✅ | **In flight:** HTTPS only. Plain HTTP gets a 302 to HTTPS, and HSTS is set for 2 years | `curl -I http://<service>` |
| ✅ | **TLS 1.2+ only:** TLS 1.0 and 1.1 are refused by Google's front end | `openssl s_client -tls1_1` fails; `-tls1_2` / `-tls1_3` succeed |
| ✅ | **Service to service:** the app's calls to Firestore, Cloud Storage and IAM Credentials are all HTTPS. GitHub reaches GCP over HTTPS with short-lived OIDC tokens | `app/gcp.go`; workflows |
| ✅ | **At rest:** Firestore, Cloud Storage, Secret Manager, Artifact Registry and Cloud Logging use Google-managed AES-256 | Google's default encryption |
| 📋 | **Customer-managed keys (CMEK)** via Cloud KMS: ~$0.06/key version/month plus operations. Not required by SOC 2; add it if a customer contract requires key control | The cost gate blocks it until it's signed off |

## Findings and fixes

| # | Severity | Finding | Fix | Status |
|---|---|---|---|---|
| 1 | High | No enforced change approval: private repo on GitHub Free (no branch protection or rulesets); every PR merged with 0 reviews | Public repo, then protected `main` with required reviews and checks, and a `prod` environment that needs approval | 🔧 |
| 2 | High | CI's plan identity (any branch) has `roles/viewer`, which includes `datastore.entities.get/list`: a PR's Terraform could read customer data | IAM deny policy on `datastore.entities.*` for `tf-plan` | 🔧 |
| 3 | High | Default Compute service account has `roles/editor` in dev and prod (unused) | Deprivilege it in Terraform | 🔧 |
| 4 | High | No alert notification channels: alerts and failed restore drills notify nobody | Email channel and log-based alerts | 🔧 |
| 5 | Medium | Data Access audit logs off; app logs kept 30 days; no immutable archive | Data Access logs on sensitive services; audit logs from all projects copied to a write-once bucket kept 400 days | 🔧 |
| 6 | Medium | Actions pinned by tag; any action allowed; no image, secret or IaC scanning; mutable image tags | Pin to SHAs, allow-list actions, add gitleaks, Trivy and checkov, immutable tags | 🔧 |
| 7 | Medium | A personal Gmail account owns all projects; no Google Cloud organization, so no org policies | Hardware-key 2SV, break-glass admin identity, quarterly access review; Cloud Identity Free + a domain for an org | 📋 |
| 8 | Medium | The AI reviews send diffs to Anthropic (a subprocessor); a leaked secret would go with them | gitleaks before the AI step; vendor register | 🔧 |
| 9 | Low | `/admin` is public with a static bearer token, rotated by hand | Quarterly rotation; alert on failed attempts | 🔧 |
| 10 | Low | No WAF or rate limiting (needs a load balancer, ~$18/month) | Accepted risk: abuse cost is capped by max instances | 📋 accepted |

## Controls already in place

| SOC 2 area | Control |
|---|---|
| CC6 Logical access | Keyless CI (Workload Identity Federation pinned to the repo ID); no service account keys anywhere; one narrow identity per job; only `main` can apply or deploy; the backup identity can add files but never change or delete them |
| CC7 Monitoring | Uptime checks, error-rate alert, SLO, dashboards; Admin Activity audit logs kept 400 days (locked); weekly drift detection |
| CC8 Change management | Every change goes through a PR with automated risk, customer-journey and cost reviews and gates; plan on PR, re-scored before apply; deploy by image digest; canary with automatic rollback; the customer journey walked on the candidate before any traffic |
| A1 Availability | Multi-region database; scale-out to 10 instances; old revision kept for instant rollback |
| C1 / backup & recovery | Delete protection; nightly backup to a write-once vault in another project; nightly restore drill; `restore` command |
| Cost | Cost gate on every PR; $1 budget alert |
| Secrets | Secret Manager; admin token generated during apply and never written to state; nothing secret in the repository (gitleaks scan of full history: clean) |

## Change management: how a change is tracked, approved and logged

1. **Proposed** as a pull request. A template records type, risk, rollback plan and linked issue.
2. **Reviewed** by machines (risk, journey, cost, tests, scanners) and **approved** by a human.
   `main` is protected: no direct pushes and no merge without approval and passing checks.
3. **Approved again for prod:** the prod deploy and prod Terraform apply wait for a reviewer on the
   `prod` environment.
4. **Recorded** in the change ledger: a record per apply and deploy, written to the write-once audit
   bucket (PR, author, approver, commit, plan hash, verdicts, image digest, revision, run link).
5. **Watched:** any change made outside the pipeline (by a human rather than `tf-apply` or a deployer)
   raises an alert, and drift detection runs daily.
6. **Emergencies:** an `emergency` label allows a fast path, recorded in the ledger and reviewed
   within 24 hours.

## Policies to write (Phase 3)

Information security, access control (including quarterly reviews), change management, incident
response, backup and recovery, vendor management (Google Cloud, GitHub, Anthropic), and risk assessment.
Each points to the evidence above.
