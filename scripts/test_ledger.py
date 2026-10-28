"""Tests for ledger.py. Run: python3 -m unittest scripts/test_ledger.py"""

import base64
import json
import os
import sys
import unittest
import zlib

sys.path.insert(0, os.path.dirname(__file__))
import ledger  # noqa: E402

ENV = {"GITHUB_REPOSITORY": "o/r", "GITHUB_SHA": "abc123", "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
       "GITHUB_WORKFLOW": "Deploy", "GITHUB_EVENT_NAME": "push", "GITHUB_ACTOR": "alice"}
PR = {"number": 7, "author": "bot[bot]", "approvals": [{"reviewer": "alice", "at": "t", "commit": "abc123"}],
      "independently_approved": True, "checks": {}}


def record(previous=None, now="2026-10-28T12:00:00Z", **details):
    return ledger.build("release", "prod", "success", details, ENV, PR, [], previous, now)


class Records(unittest.TestCase):
    def test_record_carries_who_what_and_a_self_hash(self):
        r = record(image="img@sha256:x", revision="hello-web-1")
        self.assertEqual(r["sha256"], ledger.digest(r))
        self.assertEqual(r["pull_request"]["approvals"][0]["reviewer"], "alice")
        self.assertEqual(r["details"]["revision"], "hello-web-1")
        self.assertEqual(r["workflow"]["url"], "https://github.com/o/r/actions/runs/42")

    def test_object_names_sort_by_time(self):
        a = ledger.object_name(record(now="2026-10-28T09:00:00Z"))
        b = ledger.object_name(record(now="2026-10-28T10:00:00Z"))
        self.assertEqual(a, "ledger/2026/10/28/090000Z-run42-1-release-prod.json")
        self.assertLess(a, b)

    def test_verdicts_are_read_from_review_comments(self):
        blob = base64.b64encode(zlib.compress(json.dumps({"risk": "LOW", "blocked": False, "worst_case_usd": 0}).encode())).decode()
        got = ledger.decode_verdicts([f"## 💸 Cost impact\n<!-- cost-verdict:{blob} -->", "unrelated"])
        self.assertEqual(got, {"cost": {"risk": "LOW", "blocked": False, "worst_case_usd": 0}})


class Chain(unittest.TestCase):
    def chain(self):
        r1 = record(now="2026-10-28T09:00:00Z")
        n1 = ledger.object_name(r1)
        r2 = record(previous={"object": n1, "sha256": r1["sha256"]}, now="2026-10-28T10:00:00Z")
        return {n1: r1, ledger.object_name(r2): r2}

    def test_an_intact_chain_verifies(self):
        self.assertEqual(ledger.verify(self.chain()), [])

    def test_an_altered_record_is_caught(self):
        recs = self.chain()
        first = sorted(recs)[0]
        recs[first]["outcome"] = "failure"  # someone edits history
        problems = ledger.verify(recs)
        self.assertTrue(any("altered" in p for p in problems), problems)

    def test_a_deleted_record_is_caught(self):
        recs = self.chain()
        del recs[sorted(recs)[0]]
        self.assertTrue(any("missing" in p for p in ledger.verify(recs)))


if __name__ == "__main__":
    unittest.main()
