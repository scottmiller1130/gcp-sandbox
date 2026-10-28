"""Tests for cost.py. Run: python3 -m unittest scripts/test_cost.py

Each test copies this repo's real Terraform and app code, changes it the way a
costly PR would, and checks the model catches it."""

import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import cost  # noqa: E402

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.base = os.path.join(self.tmp, "base")
        self.head = os.path.join(self.tmp, "head")
        for d in (self.base, self.head):
            shutil.copytree(os.path.join(REPO, "terraform"), os.path.join(d, "terraform"),
                            ignore=shutil.ignore_patterns(".terraform", "*.tfstate*", ".terraform.lock.hcl"))
            shutil.copytree(os.path.join(REPO, "app"), os.path.join(d, "app"))

    def change(self, rel, old, new):
        p = os.path.join(self.head, rel)
        with open(p) as f:
            text = f.read()
        self.assertIn(old, text, f"{rel} no longer contains {old!r}: update this test")
        with open(p, "w") as f:
            f.write(text.replace(old, new, 1))

    def append(self, rel, text):
        with open(os.path.join(self.head, rel), "a") as f:
            f.write(text)

    def assess(self):
        b, h = cost.load(self.base), cost.load(self.head)
        ms = cost.metrics(b, h)
        billable = cost.billable_findings(b, h)
        risk, reasons = cost.rule_risk(ms, billable)
        return {m.id: m for m in ms}, billable, risk, reasons


class TodaysRepo(Base):
    def test_the_sandbox_is_free_today(self):
        ms, billable, risk, _ = self.assess()
        self.assertEqual((billable, risk), ([], "LOW"))
        self.assertTrue(all(m.head_usd == 0 for m in ms.values()))
        self.assertEqual(ms["scheduler"].head, 2)  # dev + prod maintenance
        self.assertLess(ms["fs_reads"].head, cost.FREE["fs_reads"])

    def test_resource_copies_follow_the_environments(self):
        m = cost.load(self.head)
        self.assertEqual(m.count("google_firestore_database"), 2)
        self.assertEqual(sum(r.copies for r in m.res if r.type == "google_service_account" and r.name == "backup_writer"), 2)
        self.assertEqual(m.max_instances, {"dev": 3, "prod": 10})


class UsageModel(Base):
    def test_shorter_cache_blows_the_firestore_free_tier(self):
        self.change("app/flightlog.go", "listTTL         = 5 * time.Minute", "listTTL         = 10 * time.Second")
        self.change("app/flightlog.go", "instanceReadCap = 3000", "instanceReadCap = 30000")
        ms, _, risk, reasons = self.assess()
        self.assertGreater(ms["fs_reads"].head, cost.FREE["fs_reads"])
        self.assertGreater(ms["fs_reads"].head_usd, 0)
        self.assertEqual(risk, "CRITICAL")
        self.assertIn("Firestore reads", reasons[0])

    def test_the_per_instance_cap_still_bounds_reads(self):
        self.change("app/flightlog.go", "listTTL         = 5 * time.Minute", "listTTL         = 10 * time.Second")
        ms, _, _, _ = self.assess()  # cap of 3000/instance unchanged
        self.assertLessEqual(ms["fs_reads"].head, 10 * 3000 + 2 * 2000 + 2)

    def test_raising_max_instances_shows_shrinking_headroom(self):
        self.change("terraform/envs/prod/main.tf", "max_instances       = 10", "max_instances       = 14")
        ms, _, risk, _ = self.assess()
        self.assertGreater(ms["fs_reads"].head, ms["fs_reads"].base)
        self.assertIn(risk, ("MEDIUM", "HIGH"))

    def test_always_on_instances_cost_money(self):
        self.change("terraform/modules/cloud-run-service/main.tf", "min_instance_count = 0", "min_instance_count = 1")
        ms, _, risk, _ = self.assess()
        self.assertAlmostEqual(ms["always_on"].head_usd, 2 * 30 * 86400 * (1 * 0.0000025 + 0.25 * 0.0000025), places=2)
        self.assertEqual(risk, "CRITICAL")

    def test_faster_polling_cuts_free_viewing_hours(self):
        self.change("app/static/app.js", "const POLL_MS = 2000;", "const POLL_MS = 500;")
        ms, _, risk, reasons = self.assess()
        self.assertLess(ms["viewer_hours"].head, ms["viewer_hours"].base * 0.5)
        self.assertEqual(risk, "MEDIUM")
        self.assertIn("viewing hours", " ".join(reasons))

    def test_a_fourth_scheduler_job_is_billed(self):
        self.append("terraform/modules/revision-pruner/main.tf", '\nresource "google_cloud_scheduler_job" "extra" {\n  name = "x"\n}\n')
        ms, _, risk, _ = self.assess()
        self.assertEqual(ms["scheduler"].head, 4)
        self.assertAlmostEqual(ms["scheduler"].head_usd, 0.10)
        self.assertEqual(risk, "CRITICAL")


class BillableResources(Base):
    def test_cloud_sql_is_flagged(self):
        self.append("terraform/envs/prod/main.tf", '\nresource "google_sql_database_instance" "db" {\n  name = "db"\n}\n')
        _, billable, risk, _ = self.assess()
        self.assertEqual(len(billable), 1)
        self.assertIn("google_sql_database_instance", billable[0]["what"])
        self.assertEqual(risk, "CRITICAL")

    def test_pitr_and_a_named_database_are_flagged(self):
        self.change("terraform/modules/firestore/main.tf", '"POINT_IN_TIME_RECOVERY_DISABLED"', '"POINT_IN_TIME_RECOVERY_ENABLED"')
        self.change("terraform/modules/firestore/main.tf", 'name        = "(default)"', 'name        = "orders"')
        _, billable, _, _ = self.assess()
        whys = " ".join(b["why"] for b in billable)
        self.assertIn("point-in-time recovery", whys)
        self.assertIn("named", whys)

    def test_a_bucket_outside_the_free_regions_is_flagged(self):
        self.change("terraform/bootstrap/backups.tf", "location                    = upper(var.region)", 'location                    = "EUROPE-WEST1"')
        _, billable, _, _ = self.assess()
        self.assertIn("EUROPE-WEST1", billable[0]["why"])

    def test_removing_a_billable_thing_is_not_flagged(self):
        with open(os.path.join(self.base, "terraform", "envs", "prod", "main.tf"), "a") as f:
            f.write('\nresource "google_compute_global_address" "ip" {\n  name = "ip"\n}\n')
        _, billable, risk, _ = self.assess()
        self.assertEqual((billable, risk), ([], "LOW"))


class Report(Base):
    def run_review(self, labels=""):
        os.environ.pop("ANTHROPIC_API_KEY", None)
        md, v = os.path.join(self.tmp, "c.md"), os.path.join(self.tmp, "v.json")
        cost.main(["review", "--base", self.base, "--head", self.head, "--labels", labels, "--md", md, "--verdict", v])
        with open(md) as f:
            text = f.read()
        return text, cost.main(["gate", "--verdict", md, "--labels", labels])

    def test_free_pr_passes(self):
        md, gate = self.run_review()
        self.assertTrue(md.startswith(cost.MARKER))
        self.assertIn("Stays free", md)
        self.assertEqual(gate, 0)

    def test_billable_pr_blocks_until_accepted(self):
        self.append("terraform/envs/prod/main.tf", '\nresource "google_redis_instance" "cache" {\n  name = "c"\n}\n')
        md, gate = self.run_review()
        self.assertIn("Leaves the free tier", md)
        self.assertIn("google_redis_instance", md)
        self.assertEqual(gate, 2)
        _, gate = self.run_review(labels="cost-accepted")
        self.assertEqual(gate, 0)

    def test_gate_fails_closed_without_a_verdict(self):
        self.assertEqual(cost.main(["gate", "--verdict", "/nonexistent"]), 2)


if __name__ == "__main__":
    unittest.main()
