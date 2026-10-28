"""Tests for tf_risk.py. Run: python3 -m unittest scripts/test_tf_risk.py"""

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import tf_risk  # noqa: E402

SVC = "module.app.google_cloud_run_v2_service.this"


def rc(address, rtype, actions, before=None, after=None, **change):
    return {"address": address, "type": rtype,
            "change": {"actions": actions, "before": before, "after": after, **change}}


def service(max_instances=10, memory="256Mi", probe="/healthz", ingress="INGRESS_TRAFFIC_ALL", traffic="TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", labels=None):
    return {
        "name": "hello-web", "ingress": ingress, "labels": labels or {},
        "traffic": [{"type": traffic, "percent": 100}],
        "template": [{
            "scaling": [{"max_instance_count": max_instances, "min_instance_count": 0}],
            "containers": [{"resources": [{"limits": {"cpu": "1", "memory": memory}}],
                            "startup_probe": [{"http_get": [{"path": probe}]}]}],
        }],
    }


class Base(unittest.TestCase):
    def review(self, resource_changes, env="prod", stack="envs/prod"):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump({"resource_changes": resource_changes}, f)
        changes = tf_risk.load_plan(f.name, stack, env)
        os.unlink(f.name)
        for c in changes:
            tf_risk.apply_rules(c)
        return changes

    def one(self, *args, **kw):
        changes = self.review(*args, **kw)
        self.assertEqual(len(changes), 1, [c.address for c in changes])
        return changes[0]


class CustomerPathRules(Base):
    def test_removing_public_access_in_prod_is_critical(self):
        c = self.one([rc("module.app.google_cloud_run_v2_service_iam_member.public[0]", "google_cloud_run_v2_service_iam_member",
                         ["delete"], before={"role": "roles/run.invoker", "member": "allUsers"})])
        self.assertEqual(c.risk, "CRITICAL")
        self.assertIn("403", c.reasons[0])

    def test_same_change_in_dev_is_one_level_lower(self):
        c = self.one([rc("x", "google_cloud_run_v2_service_iam_member", ["delete"],
                         before={"role": "roles/run.invoker", "member": "allUsers"})], env="dev", stack="envs/dev")
        self.assertEqual(c.risk, "HIGH")

    def test_replacing_the_service_is_critical_and_says_why(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["delete", "create"], before=service(), after=service(),
                         replace_paths=[["location"]])])
        self.assertEqual((c.action, c.risk), ("replace", "CRITICAL"))
        self.assertTrue(any("location" in r for r in c.reasons))

    def test_lowering_max_instances_is_high(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(10), after=service(3))])
        self.assertEqual(c.risk, "HIGH")
        self.assertTrue(any("from 10 to 3" in r for r in c.reasons), c.reasons)

    def test_raising_max_instances_is_medium(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(10), after=service(15))])
        self.assertEqual(c.risk, "MEDIUM")

    def test_probe_change_is_high(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(), after=service(probe="/ready"))])
        self.assertEqual(c.risk, "HIGH")

    def test_less_memory_is_high(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(), after=service(memory="128Mi"))])
        self.assertEqual(c.risk, "HIGH")

    def test_ingress_change_is_critical(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(), after=service(ingress="INGRESS_TRAFFIC_INTERNAL_ONLY"))])
        self.assertEqual(c.risk, "CRITICAL")

    def test_labels_only_is_low(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(), after=service(labels={"team": "web"}))])
        self.assertEqual(c.risk, "LOW")

    def test_says_whether_customers_get_it_now_or_on_next_deploy(self):
        pinned = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before=service(), after=service(15))])
        self.assertTrue(any("0% traffic" in r for r in pinned.reasons))
        latest = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"],
                              before=service(traffic="TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"),
                              after=service(15, traffic="TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"))])
        self.assertTrue(any("immediately" in r for r in latest.reasons))

    def test_removing_image_pull_permission_is_critical(self):
        c = self.one([rc("google_artifact_registry_repository_iam_member.env_pull[\"prod\"]", "google_artifact_registry_repository_iam_member",
                         ["delete"], before={"role": "roles/artifactregistry.reader", "member": "serviceAccount:service-1@serverless-robot-prod.iam.gserviceaccount.com"})],
                     env="shared", stack="platform")
        self.assertEqual(c.risk, "CRITICAL")

    def test_secret_rotation_is_medium_not_critical(self):
        c = self.one([rc("module.app.google_secret_manager_secret_version.admin_token", "google_secret_manager_secret_version",
                         ["create", "delete"], before={"secret": "s"}, after={"secret": "s"})])
        self.assertEqual(c.risk, "MEDIUM")

    def test_dropping_an_api_from_terraform_without_disabling_it_is_low(self):
        c = self.one([rc("google_project_service.x", "google_project_service", ["delete"],
                         before={"service": "run.googleapis.com", "disable_on_destroy": False})])
        self.assertEqual(c.risk, "LOW")

    def test_billing_account_change_is_critical(self):
        c = self.one([rc("module.projects[\"prod\"].google_project.this", "google_project", ["update"],
                         before={"billing_account": "A", "project_id": "p"}, after={"billing_account": "", "project_id": "p"})],
                     env="shared", stack="bootstrap")
        self.assertEqual(c.risk, "CRITICAL")


class OtherRules(Base):
    def test_breaking_ci_auth_is_high(self):
        c = self.one([rc("google_iam_workload_identity_pool_provider.github", "google_iam_workload_identity_pool_provider", ["update"],
                         before={"attribute_condition": "a"}, after={"attribute_condition": "b"})], env="shared", stack="bootstrap")
        self.assertEqual(c.risk, "HIGH")

    def test_deleting_an_alert_is_medium(self):
        c = self.one([rc("module.observability.google_monitoring_alert_policy.uptime", "google_monitoring_alert_policy", ["delete"],
                         before={"display_name": "down"})])
        self.assertEqual(c.risk, "MEDIUM")

    def test_broad_role_grant_is_high_even_when_new(self):
        c = self.one([rc("google_project_iam_member.x", "google_project_iam_member", ["create"],
                         after={"role": "roles/editor", "member": "user:someone@example.com"})])
        self.assertEqual(c.risk, "HIGH")

    def test_state_bucket_is_never_lowered(self):
        c = self.one([rc("google_storage_bucket.tfstate", "google_storage_bucket", ["delete"],
                         before={"name": "gcp-sandbox-78tvq4-tfstate"})], env="dev")
        self.assertEqual(c.risk, "CRITICAL")

    def test_no_op_and_read_are_ignored(self):
        self.assertEqual(self.review([rc("a", "google_project", ["no-op"]), rc("b", "google_project", ["read"])]), [])

    def test_computed_only_update_is_low(self):
        c = self.one([rc(SVC, "google_cloud_run_v2_service", ["update"], before={"etag": "1"}, after={"etag": "2"})])
        self.assertEqual(c.risk, "LOW")


class Redaction(Base):
    def test_sensitive_values_never_reach_the_output(self):
        c = self.one([rc("google_secret_manager_secret_version.v", "google_secret_manager_secret_version", ["update"],
                         before={"secret_data": "hunter2", "enabled": True}, after={"secret_data": "hunter3", "enabled": True},
                         before_sensitive={"secret_data": True}, after_sensitive={"secret_data": True})])
        dump = json.dumps({"attrs": c.attrs, "before": c.before, "after": c.after, "ai": tf_risk._ai_payload([c])})
        self.assertNotIn("hunter", dump)


class AiMerging(Base):
    def test_ai_can_raise_but_never_lower(self):
        raise_me, lower_me = self.review([
            rc("a", "google_monitoring_dashboard", ["update"], before={"dashboard_json": "1"}, after={"dashboard_json": "2"}),
            rc("b", "google_cloud_run_v2_service_iam_member", ["delete"], before={"role": "roles/run.invoker", "member": "allUsers"}),
        ])
        item = {"plain_english": "", "customer_impact": "", "why": "", "rollback": "", "verify": ""}
        tf_risk.merge_ai([raise_me, lower_me], {"changes": [
            {**item, "id": raise_me.key, "risk": "HIGH"},
            {**item, "id": lower_me.key, "risk": "LOW"},
        ]})
        self.assertEqual(raise_me.risk, "HIGH")
        self.assertEqual(lower_me.risk, "CRITICAL")

    def test_unknown_ids_and_bad_levels_are_ignored(self):
        (c,) = self.review([rc("a", "google_monitoring_dashboard", ["update"], before={"x": 1}, after={"x": 2})])
        tf_risk.merge_ai([c], {"changes": [{"id": "nope", "risk": "CRITICAL"}, {"id": c.key, "risk": "SEVERE"}]})
        self.assertEqual(c.risk, "LOW")
        self.assertIsNone(c.ai)


class PriorVerdict(Base):
    def test_apply_time_change_missing_from_the_pr_review_is_at_least_high(self):
        reviewed, extra = self.review([
            rc("a", "google_cloud_run_v2_service", ["update"], before=service(), after=service(15)),
            rc("b", "google_cloud_run_v2_service", ["update"], before=service(), after=service(20)),
        ])
        prior = {"changes": [{"key": reviewed.key, "risk": "MEDIUM", "ai_risk": "MEDIUM", "ai": None}]}
        tf_risk.merge_prior([reviewed, extra], prior)
        self.assertEqual(reviewed.risk, "MEDIUM")
        self.assertEqual(extra.risk, "HIGH")
        self.assertTrue(extra.unreviewed)

    def test_ai_escalation_from_the_pr_carries_into_the_apply(self):
        (c,) = self.review([rc("a", "google_monitoring_dashboard", ["update"], before={"x": 1}, after={"x": 2})])
        tf_risk.merge_prior([c], {"changes": [{"key": c.key, "risk": "CRITICAL", "ai_risk": "CRITICAL", "ai": None}]})
        self.assertEqual(c.risk, "CRITICAL")


class EndToEnd(Base):
    def run_cli(self, plan, labels="", extra=()):
        d = tempfile.mkdtemp()
        path = os.path.join(d, "plan.json")
        with open(path, "w") as f:
            json.dump({"resource_changes": plan}, f)
        md, v = os.path.join(d, "c.md"), os.path.join(d, "v.json")
        tf_risk.main(["review", "--plan", f"envs/prod:prod:{path}", "--no-ai", "--md", md, "--verdict", v, *extra])
        return tf_risk.main(["gate", "--verdict", md, "--labels", labels]), tf_risk._read(md)

    def test_critical_blocks_until_risk_accepted(self):
        plan = [rc("x", "google_cloud_run_v2_service_iam_member", ["delete"], before={"role": "roles/run.invoker", "member": "allUsers"})]
        code, md = self.run_cli(plan)
        self.assertEqual(code, 2)
        self.assertIn("need sign-off", md)
        code, _ = self.run_cli(plan, labels="something, risk-accepted")
        self.assertEqual(code, 0)

    def test_no_changes(self):
        code, md = self.run_cli([])
        self.assertEqual(code, 0)
        self.assertIn("No infrastructure changes", md)

    def test_gate_fails_closed_without_a_verdict(self):
        self.assertEqual(tf_risk.main(["gate", "--verdict", "/nonexistent"]), 2)

    def test_ai_skipped_without_key(self):
        os.environ.pop("ANTHROPIC_API_KEY", None)
        (c,) = self.review([rc("a", "google_monitoring_dashboard", ["update"], before={"x": 1}, after={"x": 2})])
        review, status = tf_risk.ai_review([c], "")
        self.assertIsNone(review)
        self.assertIn("skipped", status)


class ReviewReuse(Base):
    """A review of the same commit is reused only if its AI part succeeded."""

    def run_review(self, prior):
        d = tempfile.mkdtemp()
        plan, prior_path = os.path.join(d, "plan.json"), os.path.join(d, "prior.json")
        with open(plan, "w") as f:
            json.dump({"resource_changes": [rc("a", "google_monitoring_dashboard", ["update"], before={"x": 1}, after={"x": 2})]}, f)
        with open(prior_path, "w") as f:
            json.dump(prior, f)
        calls = []
        orig = tf_risk.ai_review
        tf_risk.ai_review = lambda changes, diff: (calls.append(1), (None, "error (stub)"))[1]
        try:
            tf_risk.main(["review", "--plan", f"envs/prod:prod:{plan}", "--prior", prior_path, "--head-sha", "abc",
                          "--md", os.path.join(d, "c.md"), "--verdict", os.path.join(d, "v.json")])
        finally:
            tf_risk.ai_review = orig
        return len(calls)

    def test_reuses_a_successful_review_of_the_same_commit(self):
        self.assertEqual(self.run_review({"head_sha": "abc", "ai_status": "ok (claude-opus-5-5)", "changes": []}), 0)

    def test_retries_when_the_earlier_ai_review_failed(self):
        self.assertEqual(self.run_review({"head_sha": "abc", "ai_status": "error (API 401)", "changes": []}), 1)

    def test_new_commit_gets_a_new_review(self):
        self.assertEqual(self.run_review({"head_sha": "old", "ai_status": "ok (claude-opus-5-5)", "changes": []}), 1)


class AiCall(Base):
    """ai_review against a stand-in for the Anthropic client."""

    def setUp(self):
        import types
        try:
            import anthropic
        except ImportError:
            self.skipTest("anthropic not installed")
        self.anthropic = anthropic
        self.calls = []
        test = self

        class Stream:
            def __init__(self, kwargs):
                self.kwargs = kwargs

            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

            def get_final_message(self):
                return test.respond(self.kwargs)

        class Messages:
            def stream(self, **kwargs):
                test.calls.append(kwargs)
                return Stream(kwargs)

        fake = types.SimpleNamespace(beta=types.SimpleNamespace(messages=Messages()))
        self._orig = anthropic.Anthropic
        anthropic.Anthropic = lambda *a, **k: fake
        os.environ["ANTHROPIC_API_KEY"] = "test"
        (self.change,) = self.review([rc("a", "google_cloud_run_v2_service_iam_member", ["delete"],
                                         before={"role": "roles/run.invoker", "member": "allUsers"})])

    def tearDown(self):
        self.anthropic.Anthropic = self._orig
        os.environ.pop("ANTHROPIC_API_KEY", None)

    def message(self, text, stop="end_turn"):
        import types
        return types.SimpleNamespace(stop_reason=stop, model=tf_risk.MODEL,
                                     content=[types.SimpleNamespace(type="text", text=text)])

    def good(self):
        return json.dumps({"overall": {"risk": "LOW", "recommendation": "safe_to_merge", "summary": "s",
                                       "customer_impact": "c", "watch_after_merge": []},
                           "changes": [{"id": self.change.key, "risk": "LOW", "plain_english": "p", "customer_impact": "c",
                                        "why": "w", "rollback": "r", "verify": "v"}]})

    def test_request_shape_and_parse(self):
        self.respond = lambda kw: self.message(self.good())
        review, status = tf_risk.ai_review([self.change], "diff")
        self.assertTrue(status.startswith("ok"), status)
        kw = self.calls[0]
        self.assertEqual(kw["model"], "claude-opus-5-5")
        self.assertEqual(kw["thinking"], {"type": "adaptive"})
        self.assertEqual(kw["output_config"]["format"]["type"], "json_schema")
        self.assertEqual(kw["fallbacks"], "default")
        self.assertIn("server-side-fallback-2026-07-01", kw["betas"])
        self.assertIn(self.change.key, kw["messages"][0]["content"])
        tf_risk.merge_ai([self.change], review)
        self.assertEqual(self.change.risk, "CRITICAL")  # the AI said LOW; the floor holds

    def test_retries_without_fallbacks_if_rejected(self):
        import httpx2 as httpx  # the HTTP client anthropic 2.x is built on

        def respond(kw):
            if "fallbacks" in kw:
                req = httpx.Request("POST", "https://api.anthropic.com/v1/messages")
                raise self.anthropic.BadRequestError("nope", response=httpx.Response(400, request=req), body=None)
            return self.message(self.good())
        self.respond = respond
        review, status = tf_risk.ai_review([self.change], "")
        self.assertTrue(status.startswith("ok"), status)
        self.assertEqual(len(self.calls), 2)
        self.assertNotIn("fallbacks", self.calls[1])

    def test_refusal_and_garbage_degrade_to_rules_only(self):
        self.respond = lambda kw: self.message("", stop="refusal")
        self.assertEqual(tf_risk.ai_review([self.change], "")[0], None)
        self.respond = lambda kw: self.message("not json")
        review, status = tf_risk.ai_review([self.change], "")
        self.assertIsNone(review)
        self.assertIn("unparseable", status)


if __name__ == "__main__":
    unittest.main()
