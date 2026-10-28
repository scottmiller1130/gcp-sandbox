# Security policy

## Reporting a vulnerability

Please **don't open a public issue**. Use GitHub's private reporting instead:
**Security → Report a vulnerability** on this repository. You'll get a reply within a few days.

In scope: the code in this repository and the live sandbox services it deploys. Please don't run
load or denial-of-service tests against the live services.

## What's here

A learning sandbox on Google Cloud. It holds no credentials: CI authenticates with keyless
Workload Identity Federation pinned to this repository's ID, secrets live in Secret Manager and
GitHub encrypted secrets, and the billing account ID is kept out of the code. See
[docs/security.md](docs/security.md) for the controls and how they're evidenced.
