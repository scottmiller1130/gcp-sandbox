# Incident response policy

**Detect.** Uptime checks and alerts (site down, 5xx rate, failed backup or restore drill, failed
`/admin` logins, changes outside the pipeline, deny-policy hits) email the owner. GitHub security
alerts (code scanning, Dependabot, secret scanning) and private vulnerability reports also count.

**Respond.**
1. Open an [Incident](../../.github/ISSUE_TEMPLATE/incident.yml) issue and set a severity (SEV1 to SEV3).
2. Stop the harm first: **Rollback** workflow (no approval needed), rotate a secret (bump
   `admin_token_version`), or restore data (`server restore`, see the backup policy).
3. Keep a timeline in the issue (UTC).

**Communicate.** SEV1 with customer data exposed: tell affected customers and any relevant authority
within 72 hours.

**Learn.** Before closing: root cause, what detected it and how fast, and follow-ups as issues. A SEV1
also triggers a review of the relevant policy.
