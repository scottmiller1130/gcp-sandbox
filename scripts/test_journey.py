"""Tests for journey.py. Run: python3 -m unittest scripts/test_journey.py"""

import json
import os
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(__file__))
import journey  # noqa: E402

GOOD = {
    ("GET", "/healthz"): (200, "text/plain", b"ok\n"),
    ("GET", "/"): (200, "text/html", b'<main id="app"></main>'),
    ("GET", "/static/app.js"): (200, "text/javascript", b"1"),
    ("GET", "/static/app.css"): (200, "text/css", b"a{}"),
    ("GET", "/theme.css"): (200, "text/css", b":root{--accent:red}"),
    ("GET", "/api/stats"): (200, "application/json", b'{"instance":"i","version":"v","recent":[],"events":[]}'),
    ("GET", "/work"): (200, "application/json", b'{"instance":"i"}'),
    ("GET", "/api/flightlog"): (200, "application/json", b'{"enabled":false}'),
    ("POST", "/api/flightlog"): (403, "application/json", b'{"error":"cross-site request"}'),
}


def serve(routes):
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def handle_one(self):
            if self.command == "POST":
                self.rfile.read(int(self.headers.get("Content-Length", 0)))
            status, ctype, body = routes.get((self.command, self.path.split("?")[0]), (404, "text/plain", b"nope"))
            self.send_response(status)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Security-Policy", "default-src 'none'")
            self.end_headers()
            self.wfile.write(body)

        do_GET = do_POST = handle_one

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_port}"


class Walk(unittest.TestCase):
    def test_a_healthy_server_passes_every_probe(self):
        srv, url = serve(GOOD)
        self.addCleanup(srv.server_close)
        self.addCleanup(srv.shutdown)
        results = journey.walk(url)
        self.assertTrue(all(r["ok"] for r in results), [r for r in results if not r["ok"]])
        self.assertEqual({r["step"] for r in results}, {"reach", "load", "watch", "swarm", "sign"})

    def test_missing_csp_and_a_down_server_fail(self):
        routes = dict(GOOD)
        srv, url = serve(routes)
        self.addCleanup(srv.server_close)
        self.addCleanup(srv.shutdown)
        routes[("POST", "/api/flightlog")] = (201, "application/json", b"{}")  # guard removed
        by_name = {r["name"]: r for r in journey.walk(url)}
        self.assertFalse(by_name["Cross-site signing is refused"]["ok"])
        self.assertIn("403", by_name["Cross-site signing is refused"]["problem"])
        down = journey.walk("http://127.0.0.1:9", timeout=0.5)
        self.assertTrue(all(not r["ok"] and r["status"] == 0 for r in down))


DIFF = """diff --git a/app/main.go b/app/main.go
--- a/app/main.go
+++ b/app/main.go
@@ -180,7 +180,7 @@ func (a *app) work(w http.ResponseWriter, r *http.Request) {
-	ms = min(max(ms, 0), maxWorkMS)
+	ms = min(max(ms, 0), maxWorkMS*2)
diff --git a/app/backup.go b/app/backup.go
--- a/app/backup.go
+++ b/app/backup.go
@@ -1,3 +1,4 @@ package main
+func helperNobodyMapped() {}
"""


class Map(unittest.TestCase):
    def test_functions_pick_the_step_not_the_whole_file(self):
        by_step, quiet, unmapped = journey.map_changes(["app/main.go"], DIFF)
        self.assertEqual(by_step, {"swarm": ["app/main.go"]})

    def test_files_fall_back_to_their_steps(self):
        by_step, _, _ = journey.map_changes(["app/backup.go", "app/page.go"], DIFF)
        self.assertEqual(by_step["keep"], ["app/backup.go"])
        self.assertEqual(by_step["load"], ["app/page.go"])

    def test_docs_and_tests_are_not_customer_facing_and_unknowns_are_flagged(self):
        by_step, quiet, unmapped = journey.map_changes(["README.md", "app/main_test.go", "Makefile"], "")
        self.assertEqual(by_step, {})
        self.assertEqual(quiet, ["README.md", "app/main_test.go"])
        self.assertEqual(unmapped, ["Makefile"])


def probe(step, name, ok=True, ms=5.0, status=200):
    return {"step": step, "name": name, "request": "GET /x", "status": status, "ok": ok, "problem": "" if ok else "boom", "ms": ms}


class Compare(unittest.TestCase):
    def results(self, base, head, files=()):
        r = {s.id: journey.StepResult(s.id) for s in journey.JOURNEY}
        for p in base:
            r[p["step"]].base.append(p)
        for p in head:
            r[p["step"]].head.append(p)
        for step, f in files:
            r[step].files.append(f)
        journey.compare(r)
        return r

    def test_a_step_that_breaks_is_critical(self):
        r = self.results([probe("load", "Homepage")], [probe("load", "Homepage", ok=False, status=500)])
        self.assertEqual((r["load"].impact, r["load"].risk), ("broken", "CRITICAL"))

    def test_a_much_slower_step_is_flagged(self):
        r = self.results([probe("watch", "Live stats", ms=10)], [probe("watch", "Live stats", ms=80)])
        self.assertEqual((r["watch"].impact, r["watch"].risk), ("slower", "MEDIUM"))

    def test_jitter_is_not_slower(self):
        r = self.results([probe("watch", "Live stats", ms=2)], [probe("watch", "Live stats", ms=9)])
        self.assertEqual(r["watch"].impact, "none")

    def test_a_step_that_starts_working_is_new(self):
        r = self.results([probe("sign", "Read the flight log", ok=False, status=404)], [probe("sign", "Read the flight log")])
        self.assertEqual(r["sign"].impact, "new")
        self.assertEqual(r["sign"].risk, "LOW")

    def test_data_steps_are_never_below_medium_when_touched(self):
        r = self.results([], [], files=[("keep", "app/backup.go")])
        self.assertEqual((r["keep"].impact, r["keep"].risk), ("changed", "MEDIUM"))

    def test_ai_can_raise_but_never_lower(self):
        r = self.results([probe("load", "Homepage")], [probe("load", "Homepage", ok=False)], files=[("swarm", "app/main.go")])
        journey.merge_ai(r, {"steps": [
            {"id": "load", "impact": "none", "risk": "LOW", "customer_notices": "fine", "why": "w", "verify": "v"},
            {"id": "swarm", "impact": "degraded", "risk": "HIGH", "customer_notices": "slow swarms", "why": "w", "verify": "v"},
            {"id": "nope", "impact": "none", "risk": "LOW", "customer_notices": "", "why": "", "verify": ""},
        ]})
        self.assertEqual((r["load"].impact, r["load"].risk), ("broken", "CRITICAL"))
        self.assertEqual((r["swarm"].impact, r["swarm"].risk), ("degraded", "HIGH"))


class EndToEnd(unittest.TestCase):
    def run_review(self, head_ok=True, labels="", files="app/main.go\n"):
        os.environ.pop("ANTHROPIC_API_KEY", None)
        d = tempfile.mkdtemp()
        p = lambda n: os.path.join(d, n)
        base = [probe("load", "Homepage")]
        head = [probe("load", "Homepage", ok=head_ok, status=200 if head_ok else 500)]
        for name, v in (("base.json", base), ("head.json", head)):
            with open(p(name), "w") as f:
                json.dump(v, f)
        with open(p("files.txt"), "w") as f:
            f.write(files)
        with open(p("pr.diff"), "w") as f:
            f.write(DIFF)
        journey.main(["review", "--files", p("files.txt"), "--diff", p("pr.diff"), "--base", p("base.json"), "--head", p("head.json"),
                      "--labels", labels, "--head-sha", "abc", "--md", p("c.md"), "--verdict", p("v.json")])
        with open(p("c.md")) as f:
            md = f.read()
        gate = journey.main(["gate", "--verdict", p("c.md"), "--labels", labels])
        return md, gate

    def test_comment_shows_the_journey_and_the_walk(self):
        md, gate = self.run_review()
        self.assertTrue(md.startswith(journey.MARKER))
        for s in journey.JOURNEY:
            self.assertIn(s.name, md)
        self.assertIn("main vs this PR", md)
        self.assertIn("Summon a swarm** | ✏️ changed", md)
        self.assertEqual(gate, 0)

    def test_a_broken_journey_blocks_until_signed_off(self):
        md, gate = self.run_review(head_ok=False)
        self.assertIn("would break for customers", md)
        self.assertEqual(gate, 2)
        _, gate = self.run_review(head_ok=False, labels="risk-accepted")
        self.assertEqual(gate, 0)

    def test_docs_only_pr_skips_the_ai(self):
        md, gate = self.run_review(files="README.md\n")
        self.assertIn("not needed", md)
        self.assertEqual(gate, 0)

    def test_gate_fails_closed_without_a_verdict(self):
        self.assertEqual(journey.main(["gate", "--verdict", "/nonexistent"]), 2)


if __name__ == "__main__":
    unittest.main()
