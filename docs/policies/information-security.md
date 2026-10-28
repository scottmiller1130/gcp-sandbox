# Information security policy

**Scope:** the `gcp-sandbox` repository, its GitHub Actions, the Google Cloud projects
`gcp-sandbox-78tvq4` (shared), `gcp-sandbox-dev-78tvq4` and `gcp-sandbox-prod-78tvq4`, and the public
service they run.

**Roles**
- *Owner (@scottmiller1130):* accountable for security; approves every change, every prod
  deployment and every access grant; reviews this policy yearly.
- *Automation:* the `gcp-sandbox-claude` GitHub App authors changes; CI identities plan, apply and
  deploy. Neither can approve its own work.

**Data**
- Customer data is the flight log in Firestore: generated callsigns and emoji, no personal
  information. Requests are logged without query values; client IPs appear only in failed `/admin`
  attempts, kept 30 days.
- Secrets live in Google Secret Manager and GitHub encrypted secrets, never in code, state or logs.
  Push protection and gitleaks block secrets from being committed.

**Encryption**
- In transit: HTTPS only, TLS 1.2+ (TLS 1.0/1.1 refused), HSTS; all service-to-service calls over TLS.
- At rest: Google-managed AES-256 for every store. Customer-managed keys are available on request,
  at a cost.

**Secure development:** every change passes tests, CodeQL, dependency review, secret and image scans,
the Terraform risk review, the customer-journey walk and the cost review before a human approves it.
Images are built once, signed with build provenance, and verified before every deploy.
