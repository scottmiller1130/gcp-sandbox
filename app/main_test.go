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

func TestAdmin(t *testing.T) {
	h := testApp(map[string]string{"ADMIN_TOKEN": "s3cret\n"}) // trailing newline, as secrets often have
	for _, tc := range []struct {
		name, auth string
		want       int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"not bearer", "s3cret", http.StatusUnauthorized},
		{"right token", "Bearer s3cret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestAdminHiddenWithoutToken(t *testing.T) {
	if rec := get(t, testApp(nil), "/admin"); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestAdminNotAffectedByFaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	testApp(map[string]string{"ADMIN_TOKEN": "t", "ERROR_RATE": "1"}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestParseTraceContext(t *testing.T) {
	for _, tc := range []struct {
		header, value, trace, span string
	}{
		{"traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"},
		{"X-Cloud-Trace-Context", "105445aa7843bc8bf206b12000100000/1;o=1", "105445aa7843bc8bf206b12000100000", "0000000000000001"},
		{"X-Cloud-Trace-Context", "105445aa7843bc8bf206b12000100000", "105445aa7843bc8bf206b12000100000", ""},
		{"traceparent", "garbage", "", ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(tc.header, tc.value)
		if trace, span := parseTraceContext(r); trace != tc.trace || span != tc.span {
			t.Errorf("%s=%q: got (%q,%q), want (%q,%q)", tc.header, tc.value, trace, span, tc.trace, tc.span)
		}
	}
}
