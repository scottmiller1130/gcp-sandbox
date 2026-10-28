// Command server is a small, hardened web app for Cloud Run.
//
// It serves a public page that shows which revision and instance handled the
// request, so canary and blue/green rollouts are visible in the browser, plus a
// few endpoints used by CI and load tests.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// version is stamped at build time: -ldflags "-X main.version=<git sha>".
var version = "dev"

// maxWorkMS caps /work so a public caller can't hold requests open for long.
const maxWorkMS = 1000

type info struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Instance string `json:"instance"`
	Time     string `json:"time"`
}

type app struct {
	service   string
	revision  string
	instance  string
	errorRate float64 // fraction of requests to fail on purpose; for testing rollbacks
	style     template.CSS
	csp       string
	page      *template.Template
}

func newApp(getenv func(string) string) *app {
	a := &app{
		service:  orDefault(getenv("K_SERVICE"), "local"),
		revision: orDefault(getenv("K_REVISION"), "local"),
		instance: randomID(),
	}
	if r, err := strconv.ParseFloat(getenv("ERROR_RATE"), 64); err == nil {
		a.errorRate = min(max(r, 0), 1)
	}

	// Each version gets its own accent colour so a rollout is easy to see.
	sum := sha256.Sum256([]byte(version))
	hue := int(sum[0]) * 360 / 256
	css := fmt.Sprintf(`body{margin:0;font:16px/1.5 system-ui,sans-serif;background:#f6f7f9;color:#1b1f24}`+
		`main{max-width:40rem;margin:10vh auto;padding:2rem;background:#fff;border-radius:12px;border-top:8px solid hsl(%d 70%% 45%%)}`+
		`h1{margin-top:0}dl{display:grid;grid-template-columns:max-content 1fr;gap:.25rem 1rem}dt{color:#57606a}dd{margin:0;font-family:ui-monospace,monospace}`+
		`@media (prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}main{background:#161b22}dt{color:#8b949e}}`, hue)
	a.style = template.CSS(css)

	// Allow only this exact inline stylesheet; no scripts, frames or other origins.
	h := sha256.Sum256([]byte(css))
	a.csp = "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(h[:]) + "'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

	a.page = template.Must(template.New("page").Parse(pageHTML))
	return a
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /api/info", a.faults(a.apiInfo))
	mux.HandleFunc("GET /work", a.faults(a.work))
	mux.HandleFunc("GET /{$}", a.faults(a.index))
	return a.secureHeaders(mux)
}

func (a *app) info() info {
	return info{
		Service:  a.service,
		Version:  version,
		Revision: a.revision,
		Instance: a.instance,
		Time:     time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (a *app) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (a *app) apiInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.info())
}

// work simulates a slow request (?ms=, capped at maxWorkMS) so load tests can
// push Cloud Run into adding instances.
func (a *app) work(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.Atoi(r.URL.Query().Get("ms"))
	if err != nil {
		ms = 100
	}
	ms = min(max(ms, 0), maxWorkMS)

	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		writeJSON(w, http.StatusOK, a.info())
	case <-r.Context().Done():
	}
}

func (a *app) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.page.Execute(w, struct {
		info
		Style template.CSS
	}{a.info(), a.style}); err != nil {
		slog.Error("render page", "err", err)
	}
}

// faults fails a share of requests when ERROR_RATE is set. The error body still
// names the revision, so CI can tell which revision is failing.
func (a *app) faults(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.errorRate > 0 && mrand.Float64() < a.errorRate {
			writeJSON(w, http.StatusInternalServerError, struct {
				Error string `json:"error"`
				info
			}{"injected failure", a.info()})
			return
		}
		next(w, r)
	}
}

func (a *app) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", a.csp)
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Revision", a.revision)
		h.Set("X-Instance", a.instance)
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "err", err)
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	// Cloud Logging reads "severity" and "message" from JSON lines on stdout.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.LevelKey:
				a.Key = "severity"
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	})))

	a := newApp(os.Getenv)
	srv := &http.Server{
		Addr:              ":" + orDefault(os.Getenv("PORT"), "8080"),
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	// Cloud Run sends SIGTERM and allows ~10s before killing the instance:
	// stop accepting new connections and let in-flight requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	go func() {
		slog.Info("listening", "addr", srv.Addr, "version", version, "revision", a.revision, "instance", a.instance, "error_rate", a.errorRate)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hello from Cloud Run</title>
<style>{{.Style}}</style>
</head>
<body>
<main>
<h1>Hello from Cloud Run</h1>
<p>Reload a few times during a rollout to see traffic move between revisions.</p>
<dl>
<dt>Version</dt><dd>{{.Version}}</dd>
<dt>Revision</dt><dd>{{.Revision}}</dd>
<dt>Instance</dt><dd>{{.Instance}}</dd>
<dt>Served at</dt><dd>{{.Time}}</dd>
</dl>
</main>
</body>
</html>
`
