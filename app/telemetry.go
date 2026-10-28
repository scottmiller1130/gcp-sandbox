package main

import (
	"context"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What this instance has been doing, kept in memory for the Mission Control
// page. Only what's safe to show the public: no IPs, headers or query values.

const (
	feedSize   = 60
	eventsSize = 40
	maxPathLen = 48
)

type ring[T any] struct {
	mu   sync.Mutex
	buf  []T
	next int
	full bool
}

func newRing[T any](n int) *ring[T] { return &ring[T]{buf: make([]T, n)} }

func (r *ring[T]) add(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// list returns up to n items, newest first.
func (r *ring[T]) list(n int) []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.next
	if r.full {
		size = len(r.buf)
	}
	out := make([]T, 0, min(n, size))
	for i := 1; i <= size && len(out) < n; i++ {
		out = append(out, r.buf[(r.next-i+len(r.buf))%len(r.buf)])
	}
	return out
}

type requestRecord struct {
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	Millis float64   `json:"ms"`
}

type event struct {
	At      time.Time         `json:"at"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// quiet paths are counted but kept out of the feed: the page itself polls them.
func quiet(path string) bool {
	return path == "/api/stats" || path == "/healthz" || path == "/theme.css" || strings.HasPrefix(path, "/static/")
}

// displayPath makes a request path safe and short enough to show publicly.
// Scanners send all sorts of junk; keep printable ASCII only.
func displayPath(r *http.Request) string {
	p := r.URL.Path
	if p == "/work" {
		if ms, err := strconv.Atoi(r.URL.Query().Get("ms")); err == nil {
			p += "?ms=" + strconv.Itoa(min(max(ms, 0), maxWorkMS))
		}
	}
	b := []byte(p)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	if len(b) > maxPathLen {
		return string(b[:maxPathLen-1]) + "…"
	}
	return string(b)
}

type statusWriter struct {
	http.ResponseWriter
	start  time.Time
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		// Lets the browser split "time in the app" from "time on the network".
		w.Header().Set("Server-Timing", "app;dur="+strconv.FormatFloat(float64(time.Since(w.start).Microseconds())/1000, 'f', 2, 64))
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (a *app) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		a.inFlight.Add(1)
		sw := &statusWriter{ResponseWriter: w, start: start}
		defer func() {
			a.inFlight.Add(-1)
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			a.countStatus(sw.status)
			if quiet(r.URL.Path) {
				return
			}
			ms := float64(time.Since(start).Microseconds()) / 1000
			a.feed.add(requestRecord{At: start.UTC(), Method: r.Method, Path: displayPath(r), Status: sw.status, Millis: ms})
			a.firstRequest.Do(func() {
				slog.Info("first request served: this instance started cold", "after_ms", time.Since(a.started).Milliseconds())
			})
		}()
		next.ServeHTTP(sw, r)
	})
}

func (a *app) countStatus(code int) {
	switch {
	case code >= 500:
		a.status5xx.Add(1)
	case code >= 400:
		a.status4xx.Add(1)
	default:
		a.status2xx.Add(1)
	}
}

// --- Events: a copy of this instance's own log lines ----------------------

// safeAttrs are the log attributes shown publicly; everything else (trace IDs,
// remote addresses, Error Reporting payloads) only goes to Cloud Logging.
var safeAttrs = map[string]bool{
	"addr": true, "version": true, "revision": true, "instance": true, "error_rate": true,
	"admin_enabled": true, "after_ms": true, "err": true, "path": true,
}

type teeHandler struct {
	slog.Handler
	events *ring[event]
}

func (h teeHandler) Handle(ctx context.Context, r slog.Record) error {
	e := event{At: r.Time.UTC(), Level: r.Level.String(), Message: r.Message}
	r.Attrs(func(attr slog.Attr) bool {
		if safeAttrs[attr.Key] {
			if e.Attrs == nil {
				e.Attrs = map[string]string{}
			}
			e.Attrs[attr.Key] = attr.Value.String()
		}
		return true
	})
	h.events.add(e)
	return h.Handler.Handle(ctx, r)
}

// Attributes added with With() are request-scoped (trace IDs): not shown.
func (h teeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return teeHandler{h.Handler.WithAttrs(as), h.events}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{h.Handler.WithGroup(name), h.events}
}

// --- /api/stats -----------------------------------------------------------

type stats struct {
	info
	Environment string             `json:"environment"`
	Started     time.Time          `json:"started"`
	UptimeSec   float64            `json:"uptime_s"`
	ReadyMs     float64            `json:"ready_ms"`
	Requests    int64              `json:"requests"`
	InFlight    int64              `json:"in_flight"`
	ByStatus    map[string]int     `json:"by_status"`
	Latency     map[string]float64 `json:"latency_ms"`
	ErrorRate   float64            `json:"error_rate"`
	Recent      []requestRecord    `json:"recent"`
	Events      []event            `json:"events"`
	Runtime     map[string]any     `json:"runtime"`
	TraceID     string             `json:"trace_id,omitempty"`
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(int(p*float64(len(sorted))), len(sorted)-1)]
}

func (a *app) stats(w http.ResponseWriter, r *http.Request) {
	recent := a.feed.list(feedSize)
	lat := make([]float64, 0, len(recent))
	for _, rr := range recent {
		lat = append(lat, rr.Millis)
	}
	sort.Float64s(lat)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	var events []event
	if a.events != nil {
		events = a.events.list(20)
	}
	traceID, _ := parseTraceContext(r)

	writeJSON(w, http.StatusOK, stats{
		info:        a.info(),
		Environment: a.environment,
		Started:     a.started.UTC(),
		UptimeSec:   time.Since(a.started).Seconds(),
		ReadyMs:     float64(a.readyAfter.Microseconds()) / 1000,
		Requests:    a.requests.Load(),
		InFlight:    a.inFlight.Load() - 1, // not counting this request
		ByStatus: map[string]int{
			"2xx": int(a.status2xx.Load()), "4xx": int(a.status4xx.Load()), "5xx": int(a.status5xx.Load()),
		},
		Latency: map[string]float64{
			"p50": percentile(lat, 0.5), "p95": percentile(lat, 0.95), "max": percentile(lat, 1),
		},
		ErrorRate: a.errorRate,
		Recent:    slices.Clip(recent[:min(20, len(recent))]),
		Events:    events,
		Runtime: map[string]any{
			"go": runtime.Version(), "goroutines": runtime.NumGoroutine(),
			"heap_mib": float64(mem.HeapAlloc) / (1 << 20), "cpus": runtime.NumCPU(),
			"gc_runs": mem.NumGC,
		},
		TraceID: traceID,
	})
}
