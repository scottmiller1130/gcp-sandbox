package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testApp(env map[string]string) http.Handler {
	return newApp(func(k string) string { return env[k] }).routes()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, testApp(nil), "/healthz")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestInfoReportsRevision(t *testing.T) {
	rec := get(t, testApp(map[string]string{"K_SERVICE": "hello-web", "K_REVISION": "hello-web-abc"}), "/api/info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got info
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Service != "hello-web" || got.Revision != "hello-web-abc" || got.Version != version || got.Instance == "" {
		t.Fatalf("unexpected info: %+v", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := get(t, testApp(nil), "/")
	for _, h := range []string{"Content-Security-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

// The CSP allows the inline stylesheet by hash, so the hash must match the
// bytes actually rendered or the page loses its styling.
func TestCSPMatchesRenderedStyle(t *testing.T) {
	rec := get(t, testApp(nil), "/")
	m := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no <style> in page")
	}
	sum := sha256.Sum256([]byte(m[1]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want) {
		t.Fatalf("CSP %q does not allow rendered style %s", csp, want)
	}
}

func TestUnknownPathAndMethod(t *testing.T) {
	h := testApp(nil)
	if rec := get(t, h, "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: got %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/info", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: got %d", rec.Code)
	}
}

func TestWorkIsCapped(t *testing.T) {
	start := time.Now()
	rec := get(t, testApp(nil), "/work?ms=999999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if d := time.Since(start); d > maxWorkMS*time.Millisecond+500*time.Millisecond {
		t.Fatalf("took %v, cap not applied", d)
	}
}

func TestErrorInjection(t *testing.T) {
	h := testApp(map[string]string{"ERROR_RATE": "1", "K_REVISION": "bad-rev"})
	rec := get(t, h, "/api/info")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
	var body info
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Revision != "bad-rev" {
		t.Fatalf("error body should name the revision: %+v %v", body, err)
	}
	// Health checks must keep passing, or Cloud Run would restart instances
	// instead of letting the canary analysis catch the bad revision.
	if rec := get(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz got %d", rec.Code)
	}
}

func TestErrorRateClamped(t *testing.T) {
	a := newApp(func(k string) string {
		if k == "ERROR_RATE" {
			return "7"
		}
		return ""
	})
	if a.errorRate != 1 {
		t.Fatalf("errorRate = %v, want 1", a.errorRate)
	}
}
