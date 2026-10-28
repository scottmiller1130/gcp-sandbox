#!/usr/bin/env python3
"""The change ledger: one tamper-evident record per production-affecting change.

After every Terraform apply and every release, CI writes a JSON record to the
write-once ledger bucket (400-day retention; the writers can add records but
never change or delete them). Each record answers the auditor's questions:

  what      kind (terraform-apply / release), environment, commit, details
            (plan hash, image digest, revision), outcome
  who       the PR's author, who approved it and when, who merged it, who
            approved the `prod` deployment, which workflow run did it
  checks    the risk, customer-journey and cost verdicts from the PR
  chain     the SHA-256 of the previous record, so a missing or altered
            record breaks the chain

Usage:
  ledger.py record --kind release --environment prod --outcome success \\
                   [--detail image=...] [--detail revision=...] [--dry-run]
  ledger.py verify [--bucket NAME]        # check every record and the chain

Needs GH_TOKEN (pull-requests: read, actions: read), LEDGER_BUCKET, and gcloud
authenticated as an identity that may write to the bucket.
"""

from __future__ import annotations

import argparse
import base64
import datetime
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.request
import zlib

PREFIX = "ledger/"
VERDICTS = {  # PR comment marker -> verdict key in the record
    "tf-risk-verdict": "terraform_risk",
    "journey-verdict": "customer_journey",
    "cost-verdict": "cost",
}

# ---------------------------------------------------------------------------
# GitHub
# ---------------------------------------------------------------------------


def gh(path: str):
    api = os.environ.get("GITHUB_API_URL", "https://api.github.com")
    req = urllib.request.Request(f"{api}{path}", headers={
        "Authorization": f"Bearer {os.environ['GH_TOKEN']}",
        "Accept": "application/vnd.github+json",
    })
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.load(r)


def decode_verdicts(comments: list[str]) -> dict:
    out = {}
    for body in comments:
        for marker, key in VERDICTS.items():
            m = re.search(rf"<!-- {marker}:([A-Za-z0-9+/=]+) -->", body)
            if not m:
                continue
            try:
                v = json.loads(zlib.decompress(base64.b64decode(m.group(1))))
            except (ValueError, zlib.error):
                continue
            out[key] = {k: v.get(k) for k in ("overall_risk", "risk", "blocked", "worst_case_usd", "ai_status", "head_sha") if k in v}
    return out


def pull_request(repo: str, sha: str) -> dict | None:
    prs = [p for p in gh(f"/repos/{repo}/commits/{sha}/pulls") if p.get("merged_at")]
    if not prs:
        return None
    pr = prs[0]
    n = pr["number"]
    reviews = gh(f"/repos/{repo}/pulls/{n}/reviews?per_page=100")
    approvals = [{"reviewer": r["user"]["login"], "at": r["submitted_at"], "commit": r["commit_id"]}
                 for r in reviews if r["state"] == "APPROVED"]
    author = pr["user"]["login"]
    comments = [c["body"] for c in gh(f"/repos/{repo}/issues/{n}/comments?per_page=100")]
    return {
        "number": n,
        "title": pr["title"],
        "url": pr["html_url"],
        "author": author,
        "merged_by": (pr.get("merged_by") or {}).get("login"),
        "merged_at": pr["merged_at"],
        "labels": [l["name"] for l in pr.get("labels", [])],
        "approvals": approvals,
        # Separation of duties: approved by someone other than the author.
        "independently_approved": any(a["reviewer"] != author for a in approvals),
        "checks": decode_verdicts(comments),
    }


def environment_approvals(repo: str, run_id: str) -> list[dict]:
    try:
        rows = gh(f"/repos/{repo}/actions/runs/{run_id}/approvals")
    except Exception:  # noqa: BLE001 - evidence is best-effort, the record still goes in
        return []
    return [{"reviewer": r["user"]["login"], "state": r["state"], "comment": r.get("comment") or "",
             "environments": [e["name"] for e in r.get("environments", [])]} for r in rows]


# ---------------------------------------------------------------------------
# Records and the chain
# ---------------------------------------------------------------------------


def digest(record: dict) -> str:
    body = {k: v for k, v in record.items() if k != "sha256"}
    return hashlib.sha256(json.dumps(body, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def object_name(record: dict) -> str:
    t = datetime.datetime.fromisoformat(record["recorded_at"].replace("Z", "+00:00"))
    run = record["workflow"]
    return (f"{PREFIX}{t:%Y/%m/%d/%H%M%SZ}-run{run['run_id']}-{run['attempt']}-"
            f"{record['kind']}-{record['environment']}.json")


def build(kind: str, environment: str, outcome: str, details: dict, env: dict, pr: dict | None,
          env_approvals: list[dict], previous: dict | None, now: str) -> dict:
    server, repo = env.get("GITHUB_SERVER_URL", "https://github.com"), env["GITHUB_REPOSITORY"]
    record = {
        "format": "change-ledger/v1",
        "recorded_at": now,
        "kind": kind,
        "environment": environment,
        "outcome": outcome,
        "repository": repo,
        "commit": env["GITHUB_SHA"],
        "details": details,
        "workflow": {
            "name": env.get("GITHUB_WORKFLOW", ""),
            "run_id": env["GITHUB_RUN_ID"],
            "attempt": env.get("GITHUB_RUN_ATTEMPT", "1"),
            "url": f"{server}/{repo}/actions/runs/{env['GITHUB_RUN_ID']}",
            "event": env.get("GITHUB_EVENT_NAME", ""),
            "actor": env.get("GITHUB_ACTOR", ""),
            "triggering_actor": env.get("GITHUB_TRIGGERING_ACTOR", ""),
        },
        "pull_request": pr,
        "deployment_approvals": env_approvals,
        "previous": previous,
    }
    record["sha256"] = digest(record)
    return record


def gcloud(*args: str, input: str | None = None) -> str:
    return subprocess.run(["gcloud", "storage", *args], check=True, capture_output=True, text=True, input=input).stdout


def latest(bucket: str) -> dict | None:
    out = gcloud("ls", f"gs://{bucket}/{PREFIX}**")
    names = sorted(l.strip() for l in out.splitlines() if l.strip().endswith(".json"))
    if not names:
        return None
    rec = json.loads(gcloud("cat", names[-1]))
    return {"object": names[-1].removeprefix(f"gs://{bucket}/"), "sha256": rec.get("sha256")}


def cmd_record(args) -> int:
    env = os.environ
    repo, sha = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    details = dict(d.split("=", 1) for d in args.detail)
    pr = pull_request(repo, sha)
    approvals = environment_approvals(repo, env["GITHUB_RUN_ID"])
    bucket = args.bucket or env.get("LEDGER_BUCKET", "")
    previous = None
    if bucket and not args.dry_run:
        try:
            previous = latest(bucket)
        except subprocess.CalledProcessError:
            previous = None  # empty ledger, or no read access: record anyway
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    record = build(args.kind, args.environment, args.outcome, details, env, pr, approvals, previous, now)
    if pr is None:
        print("::warning::No merged pull request for this commit: the change wasn't reviewed through a PR.")
    elif not pr["independently_approved"]:
        print(f"::warning::PR #{pr['number']} was merged without an approval from someone other than its author.")
    text = json.dumps(record, indent=1, sort_keys=True)
    if args.dry_run or not bucket:
        if not bucket:
            print("::warning::LEDGER_BUCKET isn't set; printing the record instead of storing it.")
        print(text)
        return 0
    name = object_name(record)
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        f.write(text)
    # Write-once: fails rather than replace an existing record.
    subprocess.run(["gcloud", "storage", "cp", "--if-generation-match=0", f.name, f"gs://{bucket}/{name}"], check=True)
    os.unlink(f.name)
    print(f"Recorded gs://{bucket}/{name} (sha256 {record['sha256'][:12]}…)")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as s:
            s.write(f"\n**Change ledger:** `gs://{bucket}/{name}`\n")
    return 0


def verify(records: dict[str, dict]) -> list[str]:
    """Problems with a set of records (object name -> record); empty = intact."""
    problems = []
    by_sha = {r.get("sha256"): name for name, r in records.items()}
    for name, r in sorted(records.items()):
        if digest(r) != r.get("sha256"):
            problems.append(f"{name}: contents don't match its sha256 (altered)")
        prev = r.get("previous")
        if prev and prev.get("sha256") not in by_sha:
            problems.append(f"{name}: previous record {prev.get('object')} is missing or was altered")
    return problems


def cmd_verify(args) -> int:
    bucket = args.bucket or os.environ.get("LEDGER_BUCKET", "")
    out = gcloud("ls", f"gs://{bucket}/{PREFIX}**")
    names = sorted(l.strip() for l in out.splitlines() if l.strip().endswith(".json"))
    records = {n: json.loads(gcloud("cat", n)) for n in names}
    problems = verify(records)
    for p in problems:
        print(f"✗ {p}")
    unreviewed = [n for n, r in records.items() if not (r.get("pull_request") or {}).get("independently_approved")]
    print(f"{len(records)} records, chain {'intact' if not problems else 'BROKEN'}, "
          f"{len(unreviewed)} without an independent approval")
    return 1 if problems else 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("record")
    r.add_argument("--kind", required=True, choices=["terraform-apply", "release"])
    r.add_argument("--environment", required=True)
    r.add_argument("--outcome", required=True)
    r.add_argument("--detail", action="append", default=[], help="key=value")
    r.add_argument("--bucket")
    r.add_argument("--dry-run", action="store_true")
    v = sub.add_parser("verify")
    v.add_argument("--bucket")
    args = p.parse_args(argv)
    return cmd_record(args) if args.cmd == "record" else cmd_verify(args)


if __name__ == "__main__":
    sys.exit(main())
