// Command server is a small, hardened web app for Cloud Run.
//
// Its homepage is "Mission Control": a live view of the app's own internals -
// which instance and revision answered, what each instance is doing, how a
// request travels, and how traffic moves during a rollout.
//
// It also keeps a "flight log" in Firestore that visitors can sign: the one
// piece of state that outlives instances, deploys and regions (flightlog.go).
//
// Run as "server maintenance" it is instead a one-shot Cloud Run Job that
// backs up the flight log and prunes old revisions (maintenance.go, backup.go);
// "server restore" replays the backups into Firestore.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// version is stamped at build time: -ldflags "-X main.version=<git sha>".
var version = "dev"

// maxWorkMS caps /work so a public caller can't hold requests open for long.
const maxWorkMS = 1000

// Logs with this @type show up in Error Reporting, grouped by service and version.
const errorReportingType = "type.googleapis.com/google.devtools.clouderrorreporting.v1beta1.ReportedErrorEvent"

// Scripts and styles come only from these files - no inline code - so the
// Content-Security-Policy can stay strict.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

//go:embed static
var staticFiles embed.FS

type info struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Instance string `json:"instance"`
	Time     string `json:"time"`
}

type app struct {
	service     string
	revision    string
	instance    string
	environment string  // dev / prod, set by CI at deploy time
	project     string  // for linking logs to Cloud Trace; empty when running locally
	errorRate   float64 // fraction of requests to fail on purpose; for testing rollbacks
	adminToken  string  // from Secret Manager; empty disables /admin
	hue         int     // accent colour, derived from the version

	started      time.Time
	readyAfter   time.Duration
	firstRequest sync.Once

	requests  atomic.Int64
	inFlight  atomic.Int64
	injected  atomic.Int64
	status2xx atomic.Int64
	status4xx atomic.Int64
	status5xx atomic.Int64
	feed      *ring[requestRecord]
	events    *ring[event]
	flight    *flightLog // nil when no database is connected (running locally)

	page *template.Template
}

func newApp(getenv func(string) string) *app {
	a := &app{
		service:     orDefault(getenv("K_SERVICE"), "local"),
		revision:    orDefault(getenv("K_REVISION"), "local"),
		instance:    randomID(),
		environment: orDefault(getenv("APP_ENV"), "local"),
		project:     getenv("GOOGLE_CLOUD_PROJECT"),
		adminToken:  strings.TrimSpace(getenv("ADMIN_TOKEN")),
		started:     time.Now(),
		feed:        newRing[requestRecord](feedSize),
	}
	if a.project != "" && getenv("FLIGHTLOG") != "off" {
		hc := &http.Client{Timeout: 5 * time.Second}
		a.flight = newFlightLog(&firestore{hc: hc, base: orDefault(getenv("FIRESTORE_API"), firestoreAPI), project: a.project,
			token: metadataToken(hc, orDefault(getenv("METADATA_TOKEN_URL"), metadataURL))})
	}
	if r, err := strconv.ParseFloat(getenv("ERROR_RATE"), 64); err == nil {
		a.errorRate = min(max(r, 0), 1)
	}
	// Each version gets its own accent colour so a rollout is easy to see.
	sum := sha256.Sum256([]byte(version))
	a.hue = int(sum[0]) * 360 / 256
	a.page = template.Must(template.New("page").Parse(pageHTML))
	return a
}

func (a *app) routes() http.Handler {
	static, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /admin", a.admin)
	mux.HandleFunc("GET /api/stats", a.stats)
	mux.HandleFunc("GET /theme.css", a.theme)
	mux.Handle("GET /static/", noListing(cacheable(http.StripPrefix("/static/", http.FileServerFS(static)))))
	mux.HandleFunc("GET /api/flightlog", a.flightLogGet)
	mux.HandleFunc("POST /api/flightlog", a.flightLogSign)
	mux.HandleFunc("GET /api/info", a.faults(a.apiInfo))
	mux.HandleFunc("GET /work", a.faults(a.work))
	mux.HandleFunc("GET /{$}", a.faults(a.index))
	return a.observe(a.secureHeaders(mux))
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

// log returns a logger whose lines are linked to the request's trace, so the
// Logs Explorer and Cloud Trace can jump between them.
func (a *app) log(r *http.Request) *slog.Logger {
	traceID, spanID := parseTraceContext(r)
	if a.project == "" || traceID == "" {
		return slog.Default()
	}
	return slog.With(
		"logging.googleapis.com/trace", "projects/"+a.project+"/traces/"+traceID,
		"logging.googleapis.com/spanId", spanID,
	)
}

// parseTraceContext reads the trace Cloud Run attaches to each request, from
// the W3C traceparent header or Google's older X-Cloud-Trace-Context.
func parseTraceContext(r *http.Request) (traceID, spanID string) {
	// These headers come from the client as well as from Cloud Run, and end up
	// in logs and on the page: accept only well-formed hex IDs.
	// traceparent: 00-<32 hex trace id>-<16 hex span id>-<flags>
	if p := strings.Split(r.Header.Get("traceparent"), "-"); len(p) == 4 && isHex(p[1], 32) && isHex(p[2], 16) {
		return p[1], p[2]
	}
	// X-Cloud-Trace-Context: <32 hex trace id>/<decimal span id>;o=1
	if v := r.Header.Get("X-Cloud-Trace-Context"); v != "" {
		traceID, rest, _ := strings.Cut(v, "/")
		if !isHex(traceID, 32) {
			return "", ""
		}
		span, _, _ := strings.Cut(rest, ";")
		if n, err := strconv.ParseUint(span, 10, 64); err == nil {
			return traceID, fmt.Sprintf("%016x", n)
		}
		return traceID, ""
	}
	return "", ""
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// clientIP is the caller's address as Cloud Run's front end saw it: the last
// X-Forwarded-For entry (earlier ones are whatever the client sent). Only IP
// characters are kept, so it's safe to log.
func clientIP(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	last := strings.TrimSpace(xff[strings.LastIndex(xff, ",")+1:])
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("0123456789abcdefABCDEF.:", c) {
			return c
		}
		return -1
	}, last[:min(len(last), 45)])
}

// logSafe strips line breaks so a request value can't fake a log line.
func logSafe(s string) string {
	return strings.NewReplacer("\n", "", "\r", "").Replace(s)
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

// theme is a tiny stylesheet carrying this version's accent colour, so the
// colour can change per release without inline styles.
func (a *app) theme(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprintf(w, ":root{--accent-h:%d;--accent:hsl(%d 75%% 58%%)}\n", a.hue, a.hue)
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	traceID, _ := parseTraceContext(r)
	now := time.Now().UTC()
	if err := a.page.Execute(w, struct {
		info
		Environment string
		TraceID     string
		Project     string
		Hue         int
		ServedAt    string
		ServedDate  string
	}{a.info(), a.environment, traceID, a.project, a.hue, now.Format("15:04:05.000") + " UTC", now.Format("Mon 2 Jan 2006")}); err != nil {
		a.log(r).Error("render page", "err", err)
	}
}

// admin shows this instance's internals. It needs the bearer token stored in
// Secret Manager, and is hidden (404) when no token is configured.
func (a *app) admin(w http.ResponseWriter, r *http.Request) {
	if a.adminToken == "" {
		http.NotFound(w, r)
		return
	}
	got, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Compare hashes so the check takes the same time whatever the input length.
	gotSum, wantSum := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(a.adminToken))
	if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 || !isBearer {
		a.log(r).Warn("admin: bad or missing token", "remote", logSafe(clientIP(r)))
		w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	writeJSON(w, http.StatusOK, struct {
		info
		Uptime         string  `json:"uptime"`
		Requests       int64   `json:"requests"`
		InjectedErrors int64   `json:"injected_errors"`
		ErrorRate      float64 `json:"error_rate"`
		GoVersion      string  `json:"go_version"`
		Goroutines     int     `json:"goroutines"`
		HeapMiB        float64 `json:"heap_mib"`
	}{
		info:           a.info(),
		Uptime:         time.Since(a.started).Round(time.Second).String(),
		Requests:       a.requests.Load(),
		InjectedErrors: a.injected.Load(),
		ErrorRate:      a.errorRate,
		GoVersion:      runtime.Version(),
		Goroutines:     runtime.NumGoroutine(),
		HeapMiB:        float64(mem.HeapAlloc) / (1 << 20),
	})
}

// faults fails a share of requests when ERROR_RATE is set. The error body still
// names the revision, so CI can tell which revision is failing.
func (a *app) faults(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.errorRate > 0 && mrand.Float64() < a.errorRate {
			a.injected.Add(1)
			path := logSafe(displayPath(r))
			a.log(r).Error("injected failure on "+path,
				"path", path,
				"@type", errorReportingType,
				"serviceContext", map[string]string{"service": a.service, "version": version},
				"context", map[string]any{"httpRequest": map[string]any{"method": r.Method, "url": path, "responseStatusCode": 500}},
			)
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
		a.requests.Add(1)
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
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

// noListing serves files but never a directory index.
func noListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cacheable lets browsers keep static files briefly. URLs carry the version
// (?v=...), so a new release is fetched straight away.
func cacheable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
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
	// A copy of each line also goes to the in-memory event log Mission Control shows.
	events := newRing[event](eventsSize)
	slog.SetDefault(slog.New(teeHandler{slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.LevelKey:
				a.Key = "severity"
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	}), events}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	if len(os.Args) > 1 {
		run := map[string]func(context.Context, func(string) string, *http.Client) error{
			"maintenance": runMaintenance,
			"restore":     runRestore,
		}[os.Args[1]]
		if run == nil {
			slog.Error("unknown command", "command", os.Args[1])
			os.Exit(2)
		}
		if err := run(ctx, os.Getenv, &http.Client{Timeout: time.Minute}); err != nil {
			slog.Error(os.Args[1]+" failed", "err", err)
			os.Exit(1)
		}
		return
	}

	a := newApp(os.Getenv)
	a.events = events
	srv := &http.Server{
		Addr:              ":" + orDefault(os.Getenv("PORT"), "8080"),
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	a.readyAfter = time.Since(a.started)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "version", version, "revision", a.revision,
			"instance", a.instance, "error_rate", a.errorRate, "admin_enabled", a.adminToken != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Cloud Run sends SIGTERM and allows ~10s before killing the instance:
	// stop accepting new connections and let in-flight requests finish.
	<-ctx.Done()
	slog.Info("shutting down: Cloud Run is scaling this instance in")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
