# Risk assessment

Reviewed yearly, and whenever the architecture changes. Likelihood and impact: Low / Medium / High.

| Risk | L | I | Treatment |
|---|---|---|---|
| A bad change breaks the site | M | H | Reviews, journey walk, canary with automatic rollback |
| Customer data lost or corrupted | L | H | Multi-region, delete protection, nightly backup and drill, restore |
| CI or supply chain compromised | L | H | Pinned actions, Harden-Runner, provenance verified before deploy, keyless and least-privilege CI, deny policy on the plan identity |
| A secret leaks | L | M | Push protection, gitleaks, Secret Manager, rotation |
| Unauthorised or unreviewed change | L | H | Branch protection, prod approvals, merge audit, ledger, out-of-band change alert |
| Owner's account compromised | L | H | Security-key 2SV; alerts on human changes |
| Unexpected charges | M | L | Cost review on every PR, $1 budget alert |

**Accepted risks** (cost against benefit for a free sandbox):
- No WAF or rate limiting at the edge (needs a ~$18/month load balancer); abuse is capped by max instances.
- Google-managed encryption keys rather than customer-managed keys.
- Up to 24 hours of logical data loss without point-in-time recovery.
- One owner: separation of duties is between automation (authors) and the owner (approves), with the
  merge audit as the compensating control for admin bypass.
- Log and ledger retention policies are unlocked (locking is permanent); lock them once agreed.
