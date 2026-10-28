package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// flightApp is an app wired to the fake Google backend, with a clock the test controls.
func flightApp(t *testing.T) (*app, *fakeGoogle, *time.Time) {
	g := newFakeGoogle(t)
	env := g.env()
	env["K_REVISION"] = "hello-web-abc-1"
	a := newApp(func(k string) string { return env[k] })
	if a.flight == nil {
		t.Fatal("flight log not enabled with a project set")
	}
	now := g.clock
	a.flight.now = func() time.Time { return now }
	return a, g, &now
}

func sign(t *testing.T, h http.Handler, emoji int, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/flightlog", strings.NewReader(`{"emoji":`+itoa(emoji)+`}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func getLog(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/flightlog", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/flightlog = %d: %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFlightLogSignAndRead(t *testing.T) {
	a, g, _ := flightApp(t)
	h := a.routes()

	w := sign(t, h, 0)
	if w.Code != http.StatusCreated {
		t.Fatalf("sign = %d: %s", w.Code, w.Body)
	}
	var got struct {
		Entry           logEntry `json:"entry"`
		SignaturesToday int      `json:"signatures_today"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	e := got.Entry
	if e.Emoji != "🚀" || e.Revision != "hello-web-abc-1" || e.Callsign == "" || e.Created.IsZero() || got.SignaturesToday != 1 {
		t.Fatalf("unexpected entry %+v (today %d)", e, got.SignaturesToday)
	}
	if ids := g.entryIDs(); len(ids) != 1 || ids[0] != e.ID {
		t.Fatalf("stored %v, want [%s]", ids, e.ID)
	}
	if n := g.docs[quotaDocPrefix+"2026-10-28"].int("signatures"); n != 1 {
		t.Fatalf("quota counter = %d, want 1", n)
	}

	out := getLog(t, h)
	if out["enabled"] != true || out["total"].(float64) != 1 {
		t.Fatalf("GET: %v", out)
	}
	entries := out["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["callsign"] != e.Callsign {
		t.Fatalf("entries = %v", entries)
	}
	st := out["status"].(map[string]any)
	if st["location"] != "nam5" || st["delete_protection"] != true || st["pitr"] != false {
		t.Fatalf("status = %v", st)
	}
}

func TestFlightLogRejectsBadSignatures(t *testing.T) {
	a, g, _ := flightApp(t)
	h := a.routes()
	cases := map[string]struct {
		emoji  int
		mutate func(*http.Request)
		want   int
	}{
		"cross-site":     {0, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		"form post":      {0, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		"unknown emoji":  {len(logEmojis), func(*http.Request) {}, http.StatusBadRequest},
		"negative emoji": {-1, func(*http.Request) {}, http.StatusBadRequest},
		"empty body":     {0, func(r *http.Request) { r.Body = http.NoBody }, http.StatusBadRequest},
	}
	for name, c := range cases {
		if w := sign(t, h, c.emoji, c.mutate); w.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", name, w.Code, c.want, w.Body)
		}
	}
	if ids := g.entryIDs(); len(ids) != 0 {
		t.Fatalf("rejected requests wrote %v", ids)
	}
}

func TestFlightLogRateLimits(t *testing.T) {
	a, _, now := flightApp(t)
	h := a.routes()
	if w := sign(t, h, 1); w.Code != http.StatusCreated {
		t.Fatalf("first sign = %d", w.Code)
	}
	// Same instance, straight away: throttled for everyone.
	other := func(r *http.Request) { r.Header.Set("X-Forwarded-For", "198.51.100.1") }
	if w := sign(t, h, 1, other); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("instance throttle: %d %v", w.Code, w.Header())
	}
	// After the instance limit, another visitor may sign but the first must wait.
	*now = now.Add(signEvery + time.Millisecond)
	if w := sign(t, h, 1); w.Code != http.StatusTooManyRequests {
		t.Fatalf("visitor throttle: %d", w.Code)
	}
	if w := sign(t, h, 1, other); w.Code != http.StatusCreated {
		t.Fatalf("other visitor: %d %s", w.Code, w.Body)
	}
}

func TestFlightLogDailyCapHoldsAcrossInstances(t *testing.T) {
	a, g, _ := flightApp(t)
	g.docs[quotaDocPrefix+"2026-10-28"] = fsFields{"signatures": fsInt(dailySignCap)}
	w := sign(t, a.routes(), 0)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "free tier") {
		t.Fatalf("over the cap: %d %s", w.Code, w.Body)
	}
	if ids := g.entryIDs(); len(ids) != 0 {
		t.Fatalf("wrote %v past the daily cap", ids)
	}
}

func TestFlightLogCachesReads(t *testing.T) {
	a, g, now := flightApp(t)
	h := a.routes()
	g.addEntry("a", g.clock.Add(-time.Hour))

	getLog(t, h)
	getLog(t, h)
	if g.queries != 2 { // one list + one count
		t.Fatalf("queries after two GETs within the TTL = %d, want 2", g.queries)
	}
	*now = now.Add(listTTL)
	getLog(t, h)
	if g.queries != 4 {
		t.Fatalf("queries after the TTL = %d, want 4", g.queries)
	}

	// Out of budget: serve what's cached, never query.
	a.flight.mu.Lock()
	a.flight.reads = instanceReadCap
	a.flight.mu.Unlock()
	*now = now.Add(listTTL)
	if out := getLog(t, h); out["total"].(float64) != 1 {
		t.Fatalf("cached total lost: %v", out)
	}
	if g.queries != 4 {
		t.Fatalf("queried past the read budget: %d", g.queries)
	}
}

func TestFlightLogOffWithoutDatabase(t *testing.T) {
	a := newApp(func(string) string { return "" })
	if a.flight != nil {
		t.Fatal("flight log enabled without a project")
	}
	out := getLog(t, a.routes())
	if out["enabled"] != false || out["reason"] == "" {
		t.Fatalf("got %v", out)
	}
	if w := sign(t, a.routes(), 0); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("sign without a database = %d", w.Code)
	}
}

func TestFlightLogPollingStaysOutOfTheFeed(t *testing.T) {
	a, _, _ := flightApp(t)
	h := a.routes()
	getLog(t, h)
	sign(t, h, 0)
	recent := a.feed.list(10)
	if len(recent) != 1 || recent[0].Method != http.MethodPost {
		t.Fatalf("feed = %+v, want only the POST", recent)
	}
}
