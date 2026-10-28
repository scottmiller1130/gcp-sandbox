# Customer journey impact

Every pull request gets a **🧭 Customer journey impact** comment answering one question: *what will a
visitor notice if this merges?* It's posted by `.github/workflows/journey.yml` and updated on each push.

## The journey

Defined in `scripts/journey.py`, in the order a visitor experiences it:

| Step | The promise |
|---|---|
| 1. Reach the site | The URL answers quickly over HTTPS, even after hours idle |
| 2. Load the page | Mission Control appears complete, styled and safe (strict CSP) |
| 3. Watch it live | Instances, the request feed and logs update every 2 s |
| 4. Summon a swarm | A burst of 100 requests is answered; Cloud Run scales out |
| 5. Sign the flight log | A signature is accepted fast and shows up in the log |
| 6. Their signature is kept | Nothing a visitor writes is ever lost |
| 7. Releases arrive unnoticed | Canary rollouts, automatic rollback, no downtime |
| 8. Problems are fixed fast | Alerts fire; rollback is one click |

## How each PR is assessed

1. **Walk** (measured). CI builds **main** and **the PR**, starts both, and walks each through the
   journey like a visitor: the service answering, homepage (and its CSP), script, styles, live stats, a slow
   request, reading the flight log, and checking a cross-site signature is refused. Median of 3 runs
   each. This job runs the PR's code, so it gets **no secrets and no cloud access**.
2. **Map** (deterministic). Changed files, and for Go the changed *functions*, are mapped onto the
   steps they can affect. Docs and tests are listed as not customer-facing, and unknown files are flagged
   so the map stays complete.
3. **AI review** (Claude, advisory). Explains each step in a customer's words, says what to verify after
   deploy, and may **raise** a step's risk (never lower it).

| Finding | Impact | Risk |
|---|---|---|
| Works on main, fails on the PR | 💥 broken | 🔴 CRITICAL: **blocks the merge** |
| ≥ 1.5× and ≥ 25 ms slower | 🐢 slower | 🟡 MEDIUM |
| Fails on main, works on the PR | ✨ new | |
| Code on the step changed, walk passes | ✏️ changed | 🟢 LOW (🟡 for the data steps 5–6) |

A broken step fails the check until someone adds the **`risk-accepted`** label, the same sign-off
the Terraform review uses. Terraform PRs get both comments: the journey view here, and the
per-resource view in the **Terraform change review**.

## The same walk guards every release

`_release.yml` walks the journey again on the **live candidate revision** (database attached, 0%
traffic) right after the smoke test. Any failing step stops the release before a single customer is
sent to it. Run it yourself against any URL:

```bash
python3 scripts/journey.py check --url https://hello-web-txl6im2auq-uc.a.run.app --out /tmp/walk.json
```

## Extending it

Add a step to `JOURNEY` (with its file patterns and Go functions) and, if a visitor can exercise
it over HTTP, a `Probe`. Tests: `python3 -m unittest scripts/test_journey.py`.
