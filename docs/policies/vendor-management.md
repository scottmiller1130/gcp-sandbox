# Vendor management policy

| Vendor | Used for | Can see | Assurance |
|---|---|---|---|
| Google Cloud | Hosting, data, logs, secrets | Everything in the projects | SOC 2 Type II, ISO 27001 |
| GitHub | Code, CI, reviews, releases | Code, workflow logs, encrypted secrets | SOC 2 Type II |
| Anthropic | AI reviews (risk, journey, cost) and the authoring App (Claude Code) | Diffs and plans (with sensitive values redacted); no customer data | SOC 2 Type II |
| Grafana k6, StepSecurity, Aqua (Trivy), Bridgecrew (checkov), OpenSSF | CI tools | Public code only | Open source; actions pinned to commits |

**Rules:** a new vendor is added through a PR that updates this table. Secrets and customer data are
never sent to an AI review: push protection keeps secrets out of the repo, and plans are redacted.
**Review:** yearly, including each vendor's current SOC 2 report.
