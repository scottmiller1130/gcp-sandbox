# Backup and recovery policy

| | |
|---|---|
| **What** | The flight log (Firestore `(default)`, `nam5` multi-region) |
| **Replication** | Synchronous across US regions: region loss loses no confirmed write |
| **Backups** | Nightly, incremental, to a vault bucket in the shared project; write-only identity, 90-day retention |
| **Restore drill** | Nightly, automatic: read back, checksum, and reconcile the database count against all backups. Failure alerts |
| **RPO** | 0 for infrastructure failure; up to 24 hours for logical loss (bad code or deletion), since point-in-time recovery is off (billed) |
| **RTO** | Minutes: `gcloud run jobs execute hello-web-maintenance --args restore --wait` (idempotent) |
| **Protection** | Delete protection; Terraform `prevent_destroy`; the risk review blocks any destroy |

**Evidence:** the drill result in `meta/backup` (shown on Mission Control), the job's logs, and the vault's
files. **Test:** at least yearly, restore into dev from the vault and record it as a closed issue.
