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
| 1 | High | No enforced change approval: private repo on GitHub Free (no branch protection or rulesets); every PR merged with 0 reviews | Repo public. `main` protected: PR + 6 required checks, 1 code-owner approval, stale approvals dismissed, the last push must be approved by someone else, no force-push. PRs are authored by the `gcp-sandbox-claude` GitHub App and approved by a human. `prod` environment gates deploys, applies and promotes | ✅ |
| 2 | High | CI's plan identity (any branch) has `roles/viewer`, which includes `datastore.entities.get/list`: a PR's Terraform could read customer data | In dev and prod, `roles/viewer` is replaced by 8 narrow read roles (IAM policies, Run, Monitoring, secret *metadata*, Scheduler, service accounts, APIs, project) plus a custom `firestoreMetadataReader` (database settings only). None can read documents. (A deny policy would be simpler, but needs a Google Cloud organization.) | 🔧 apply bootstrap |
| 3 | High | Default Compute service account has `roles/editor` in dev and prod (unused) | `google_project_default_service_accounts` with `DEPRIVILEGE` in the project module | 🔧 apply bootstrap |
| 4 | High | No alert notification channels: alerts and failed restore drills notify nobody | Email channel from the `ALERT_EMAIL` secret (a sensitive variable, redacted from logs, plans and reviews). Log-based alerts per environment: **backup or restore drill failed/skipped**, **failed `/admin` login**, **change made outside the pipeline** (any human write in the audit log). Drift check daily | 🔧 set `ALERT_EMAIL` |
| 5 | Medium | Data Access audit logs off; app logs kept 30 days; no immutable archive | Data Access logs: secret access, impersonation and token exchange, database writes and settings reads, and every read and write on the state, vault and ledger buckets. All audit logs from all 3 projects are routed to `audit-archive` (400 days) in the shared project. Plus a write-once **change ledger** bucket (400-day retention). ~1¢/month | 🔧 apply bootstrap |
| 6 | Medium | Actions pinned by tag; any action allowed; no image, secret or IaC scanning; mutable image tags | Every action pinned to a commit SHA (Dependabot keeps them current); Harden-Runner on every job; gitleaks (blocking), Trivy on the image (blocking on fixable HIGH/CRITICAL), checkov (reported), dependency review on PRs, CodeQL, OpenSSF Scorecard; SLSA build provenance signed at build and **verified before every deploy**. Allow-list and immutable tags follow once these merge | 🔧 |
| 7 | Medium | A personal Gmail account owns all projects; no Google Cloud organization, so no org policies | Hardware-key 2SV, break-glass admin identity, quarterly access review; Cloud Identity Free + a domain for an org | 📋 |
| 8 | Medium | The AI reviews send diffs to Anthropic (a subprocessor); a leaked secret would go with them | Secret scanning push protection stops a secret before it reaches GitHub (so before any review sees it); gitleaks re-checks every commit. Vendor register in the policies | 🔧 |
| 9 | Low | `/admin` is public with a static bearer token, rotated by hand | Quarterly rotation; alert on failed attempts | 🔧 |
| 10 | Low | No WAF or rate limiting (needs a load balancer, ~$18/month) | Accepted risk: abuse cost is capped by max instances | 📋 accepted |

## Found by the new scanners

| Scanner | Finding | Fix |
|---|---|---|
| Trivy | The app was built on **Go 1.25, which is out of support**: 1 CRITICAL and 4 HIGH stdlib vulnerabilities (HTTP/2 and Range-header denial of service, crypto/tls) that 1.25 will never get fixes for | Go 1.27.3; govulncheck and Trivy both clean |
| CodeQL | 6 × log injection: trace headers, request paths and `X-Forwarded-For` written to logs as sent | Trace IDs must be well-formed hex; the client IP is the front end's entry, IP characters only; paths sanitised |
| CodeQL | 17 × actions pinned by tag | Pinned to commit SHAs |

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

## Who does what

| Actor | Can | Can't |
|---|---|---|
| `gcp-sandbox-claude-78tvq4[bot]` (GitHub App; Claude Code) | push branches, open PRs (1-hour tokens, this repo only) | approve, merge, change settings, reach other repos |
| Code owner (@scottmiller1130) | approve and merge PRs, approve `prod` | push to `main` directly (protected) |
| CI identities | plan (read-only), apply and deploy from `main` only | act from a branch, change the bootstrap layer |

Rollback is a **pre-approved standard change**: it doesn't wait for the `prod` reviewer, so an incident is never blocked on a click.

## Change management: how a change is tracked, approved and logged

1. **Proposed** as a pull request. A template records type, risk, rollback plan and linked issue.
2. **Reviewed** by machines (risk, journey, cost, tests, scanners) and **approved** by a human.
   `main` is protected: no direct pushes and no merge without approval and passing checks.
3. **Approved again for prod:** the prod deploy and prod Terraform apply wait for a reviewer on the
   `prod` environment.
4. **Recorded** in the change ledger (`scripts/ledger.py`): after every apply and release, a record
   (PR, author, approvals, deployment approval, verdicts, plan hash or image digest, revision, outcome,
   run link) goes to the write-once ledger bucket, chained by SHA-256 to the previous record.
   `python3 scripts/ledger.py verify` checks every record and the chain. Every prod release is also a
   GitHub Release.
5. **Watched:** any change made outside the pipeline (by a human rather than `tf-apply` or a deployer)
   raises an alert, and drift detection runs daily.
6. **Emergencies:** label the PR `emergency`; the owner may merge ahead of review (an admin bypass,
   recorded by GitHub). The merge audit workflow opens a post-merge review issue, due in 24 hours. It
   also catches any merge without an independent approval.

## Policies and SOC 2 mapping

- [Policies](policies/README.md): information security, access control, change management, incident
  response, backup and recovery, vendor management, risk assessment.
- [SOC 2 control matrix](soc2-controls.md): each Trust Services Criterion, its control, and where the
  evidence is.
