# Change management policy

Every change to code, infrastructure or configuration goes through a pull request. There are no
manual changes to production, and nothing is pushed directly to `main`.

1. **Propose.** A PR using the template: what and why, change type, risk and rollback.
2. **Review automatically.** CI tests; CodeQL; dependency review; secret, image and IaC scans; the
   Terraform risk review (per-resource customer impact; CRITICAL blocks); the customer-journey walk
   (main vs PR; a broken step blocks); the cost review (new charges block).
3. **Approve.** A code owner who didn't author the change, after reading the reviews. Approvals reset
   on new commits. Blocking findings need an explicit label (`risk-accepted`, `cost-accepted`).
4. **Release.** Merge builds one image (with signed provenance), deploys it to dev, then waits for
   the `prod` approval before a canary rollout with automatic rollback. Infrastructure applies wait
   for the same approval in prod.
5. **Record.** Each apply and release writes a change-ledger record: PR, author, approvals,
   deployment approval, verdicts, plan hash or image digest, outcome, chained to the previous one.
   `python3 scripts/ledger.py verify` checks the whole ledger.
6. **Detect drift.** A daily plan fails if the cloud no longer matches the code. A log-based alert
   fires when a person changes a project directly.

**Standard pre-approved changes:** rollback to the previous revision (the Rollback workflow) needs no
approval, so an incident is never blocked.

**Emergency changes:** label the PR `emergency`. The owner may merge ahead of review (an admin
bypass, recorded by GitHub). The merge audit workflow opens a post-merge review issue that must be
closed within 24 hours.
