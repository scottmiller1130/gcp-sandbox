package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRingKeepsNewestFirst(t *testing.T) {
	r := newRing[int](3)
	for i := 1; i <= 5; i++ {
		r.add(i)
	}
	if got := r.list(10); len(got) != 3 || got[0] != 5 || got[2] != 3 {
		t.Fatalf("got %v, want [5 4 3]", got)
	}
	if got := r.list(2); len(got) != 2 || got[0] != 5 {
		t.Fatalf("got %v", got)
	}
	if got := newRing[int](3).list(5); len(got) != 0 {
		t.Fatalf("empty ring returned %v", got)
	}
}

func statsOf(t *testing.T, h http.Handler, headers map[string]string) stats {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d", rec.Code)
	}
	var s stats
	if err := json.NewDecoder(rec.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStatsRecordsRequestsButNotPolling(t *testing.T) {
	h := testApp(map[string]string{"K_REVISION": "rev-1", "APP_ENV": "prod"})
	get(t, h, "/api/info")
	get(t, h, "/work?ms=5&secret=hunter2")
	get(t, h, "/nope")
	get(t, h, "/healthz")
	statsOf(t, h, nil)
	s := statsOf(t, h, nil)

	if s.Revision != "rev-1" || s.Environment != "prod" {
		t.Errorf("identity: %+v", s.info)
	}
	paths := []string{}
	for _, r := range s.Recent {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, ",") != "/nope,/work?ms=5,/api/info" {
		t.Errorf("feed = %v; want newest first, polling and health checks hidden, query values dropped", paths)
	}
	if s.ByStatus["4xx"] != 1 || s.ByStatus["2xx"] < 4 {
		t.Errorf("by_status = %v", s.ByStatus)
	}
	if s.InFlight != 0 {
		t.Errorf("in_flight = %d, want 0 (excluding the stats request itself)", s.InFlight)
	}
}

func TestStatsNeverLeaksClientDetails(t *testing.T) {
	h := testApp(nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("Cookie", "session=abc")
	h.ServeHTTP(httptest.NewRecorder(), req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	for _, leak := range []string{"203.0.113.7", "session=abc"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("stats leaked %q", leak)
		}
	}
}

func TestStatsIncludesTraceOfThisRequest(t *testing.T) {
	s := statsOf(t, testApp(nil), map[string]string{"X-Cloud-Trace-Context": "105445aa7843bc8bf206b12000100000/1;o=1"})
	if s.TraceID != "105445aa7843bc8bf206b12000100000" {
		t.Fatalf("trace_id = %q", s.TraceID)
	}
}

func TestStatsStaysUpWhenTheRevisionIsRiggedToFail(t *testing.T) {
	s := statsOf(t, testApp(map[string]string{"ERROR_RATE": "1"}), nil)
	if s.ErrorRate != 1 {
		t.Fatalf("error_rate = %v", s.ErrorRate)
	}
}

func TestDisplayPathIsSafe(t *testing.T) {
	cases := map[string]string{
		"/work?ms=99999":               "/work?ms=1000",
		"/work?ms=abc":                 "/work",
		"/x?token=secret":              "/x",
		"/%3Cscript%3E":                "/<script>",
		"/" + strings.Repeat("a", 100): "/" + strings.Repeat("a", maxPathLen-2) + "…",
		"/caf%C3%A9":                   "/caf??",
	}
	for in, want := range cases {
		if got := displayPath(httptest.NewRequest(http.MethodGet, in, nil)); got != want {
			t.Errorf("displayPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerTimingHeader(t *testing.T) {
	rec := get(t, testApp(nil), "/api/info")
	if !strings.HasPrefix(rec.Header().Get("Server-Timing"), "app;dur=") {
		t.Fatalf("Server-Timing = %q", rec.Header().Get("Server-Timing"))
	}
}

func TestStaticFilesAndTheme(t *testing.T) {
	h := testApp(nil)
	for path, ctype := range map[string]string{
		"/static/app.js":  "javascript",
		"/static/app.css": "text/css",
		"/theme.css":      "text/css",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), ctype) {
			t.Errorf("%s content type %q", path, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Cache-Control") == "no-store" {
			t.Errorf("%s should be cacheable", path)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s missing nosniff", path)
		}
	}
	if rec := get(t, h, "/static/"); rec.Code != http.StatusNotFound {
		t.Errorf("/static/ = %d, want 404 (no directory listing)", rec.Code)
	}
	if rec := get(t, h, "/theme.css"); !strings.Contains(rec.Body.String(), "--accent:hsl(") {
		t.Errorf("theme.css = %q", rec.Body.String())
	}
}

func TestPageHasHooksForTheScript(t *testing.T) {
	body := get(t, testApp(map[string]string{"K_REVISION": "rev-9", "APP_ENV": "dev"}), "/").Body.String()
	for _, want := range []string{`data-revision="rev-9"`, `data-env="dev"`, `src="/static/app.js?v=`, `id="fleet"`, `id="radar"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestEventLogDropsSensitiveAttributes(t *testing.T) {
	events := newRing[event](5)
	logger := slog.New(teeHandler{slog.NewJSONHandler(&bytes.Buffer{}, nil), events})
	logger.With("logging.googleapis.com/trace", "projects/p/traces/t").
		Warn("admin: bad or missing token", "remote", "203.0.113.7", "path", "/admin")
	got := events.list(1)[0]
	if got.Message != "admin: bad or missing token" || got.Level != "WARN" {
		t.Fatalf("event = %+v", got)
	}
	if _, ok := got.Attrs["remote"]; ok {
		t.Error("remote address kept")
	}
	if got.Attrs["path"] != "/admin" {
		t.Errorf("attrs = %v", got.Attrs)
	}
	for _, v := range got.Attrs {
		if strings.Contains(v, "traces") {
			t.Error("trace ID kept")
		}
	}
}
