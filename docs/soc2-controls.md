# SOC 2 control matrix

Each Trust Services Criterion this platform addresses, the control that meets it, and **where an
auditor finds the evidence**. Policies: [docs/policies](policies/README.md). Audit and findings:
[docs/security.md](security.md).

## Common criteria (Security)

| TSC | Control | Evidence |
|---|---|---|
| CC1.1–1.5 Control environment | Written policies with an owner, reviewed yearly | `docs/policies/` and its git history |
| CC2.1 Information for controls | Every change carries machine-generated risk, journey and cost reviews | PR comments: Terraform change review, Customer journey impact, Cost impact |
| CC3.1–3.4 Risk assessment | Risk register with treatments and accepted risks | `docs/policies/risk-assessment.md` |
| CC4.1 Monitoring of controls | OpenSSF Scorecard weekly; daily drift check; restore drill nightly | Scorecard badge and Security tab; Terraform workflow runs; `meta/backup` |
| CC5.2 Technology controls | Required checks on `main`: tests, CodeQL, scanners, risk / journey / cost gates | Branch protection settings; check runs on each PR |
| CC6.1 Logical access | Keyless CI pinned to the repo ID; one identity per job; the plan identity can't read customer data (narrow read roles, no `roles/viewer` in dev/prod); no service account keys | `terraform/bootstrap/identity.tf`, `audit.tf`; `gcloud iam service-accounts keys list` (empty) |
| CC6.2 Access provisioning | Access requests via issue, granted through a reviewed PR | `access-request` issues; bootstrap PRs |
| CC6.3 Access removal and review | Quarterly access review | Closed `access-review` issues |
| CC6.6 Boundary protection | HTTPS only, TLS 1.2+, HSTS, strict CSP; ingress only via Google's front end | `curl`/`openssl` checks in `docs/security.md`; `app/main.go` headers |
| CC6.7 Data in transit | TLS for every hop; secrets only in Secret Manager / GitHub secrets | `app/gcp.go`; push protection, gitleaks |
| CC6.8 Malicious software | Distroless non-root image, Trivy scan, build provenance verified before deploy, pinned actions, Harden-Runner | CI and Deploy runs; `gh attestation verify` |
| CC7.1 Vulnerability detection | CodeQL, Dependabot, Trivy, checkov, govulncheck, Scorecard | Security tab; Dependabot PRs |
| CC7.2 Anomaly monitoring | Uptime and error alerts; alerts for failed backups, failed admin logins, human changes; Data Access audit logs | Alert policies; `audit-archive` log bucket (400 days) |
| CC7.3–7.5 Incident response | Incident issues with severity, timeline, root cause; rollback without approval | `incident` issues; Rollback runs; `docs/policies/incident-response.md` |
| CC8.1 Change management | PR + approval by a non-author code owner; `prod` approvals; emergency path with 24-hour post-merge review; change ledger | PR reviews; deployment approvals; `post-merge-review` issues; `gs://…-ledger` + `scripts/ledger.py verify`; GitHub Releases |
| CC9.2 Vendor risk | Vendor register with what each can see | `docs/policies/vendor-management.md` |

## Availability

| TSC | Control | Evidence |
|---|---|---|
| A1.1 Capacity | Autoscaling 0–10 instances; load tests; cost-modelled headroom | Load test runs; cost review on each PR |
| A1.2 Environmental protections, backups | Multi-region database; nightly backup to a separate project; delete protection | `modules/firestore`, `bootstrap/backups.tf`; vault contents |
| A1.3 Recovery testing | Nightly automated restore drill; yearly restore into dev | `meta/backup` drill result; closed restore-test issue |

## Confidentiality

| TSC | Control | Evidence |
|---|---|---|
| C1.1 Identify and protect confidential information | No personal data in the flight log; logs keep no query values; sensitive Terraform values redacted from plans, artifacts and AI reviews | `docs/policies/information-security.md`; `tf_risk.py redact` |
| C1.2 Disposal | Retention: logs 30 days (audit 400), backups 90 days, ledger 400 days | Bucket and log bucket retention settings |

## What an auditor would still ask for

- **A period of operation.** SOC 2 Type II covers 3–12 months of these controls running. The ledger,
  audit archive, PRs and issues are that history; keep them.
- **A second person.** Separation of duties here is automation versus the owner. A second human
  reviewer strengthens CC8.1 and CC6.3.
- **Lock retention** on the audit archive, vault and ledger once agreed (permanent, so deliberate).
