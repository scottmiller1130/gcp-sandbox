package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The flight log: visitors "sign" it and the signature is stored in Firestore,
// so unlike everything else on Mission Control it survives scale-to-zero,
// deploys and the loss of a whole region (the database is multi-region).
//
// Staying inside Firestore's free tier (50k reads, 20k writes a day) is a hard
// requirement, so every path has a ceiling:
//
//	signing    a transaction on a per-day counter: at most dailySignCap a day
//	           across all instances (1 read + 2 writes each)
//	listing    cached per instance for listTTL, so reads are bounded by
//	           max instances x (1 day / listTTL) x (listLimit + 2)
//	everything a per-instance read budget, as a backstop
//
// Visitors choose only an emoji from a fixed set; the callsign is generated
// here. There's no free text, so there's nothing to moderate.

const (
	logCollection  = "flightlog"
	backupStateDoc = "meta/backup"
	quotaDocPrefix = "meta/quota-" // + UTC date

	listLimit       = 10
	listTTL         = 5 * time.Minute
	statusTTL       = 10 * time.Minute
	dailySignCap    = 2000
	instanceReadCap = 3000
	signEvery       = 2 * time.Second  // per instance
	clientEvery     = 15 * time.Second // per visitor
)

var (
	logEmojis   = []string{"🚀", "🛰️", "🌍", "⭐", "🔥", "🧪", "🛸", "🐙"}
	callsignAdj = []string{"Brave", "Calm", "Swift", "Lucky", "Sunny", "Witty", "Noble", "Zesty", "Cosmic", "Plucky",
		"Gentle", "Spry", "Bold", "Quiet", "Jolly", "Keen", "Nimble", "Proud", "Rapid", "Snappy"}
	callsignNoun = []string{"Otter", "Falcon", "Panda", "Lynx", "Badger", "Heron", "Koala", "Marten", "Ibex", "Gecko",
		"Puffin", "Walrus", "Bison", "Orca", "Lemur", "Raven", "Tapir", "Yak", "Quokka", "Narwhal"}

	errLogFull = errors.New("the flight log is full for today")
)

type logEntry struct {
	ID       string    `json:"id"`
	Callsign string    `json:"callsign"`
	Emoji    string    `json:"emoji"`
	Revision string    `json:"revision"`
	Version  string    `json:"version"`
	Instance string    `json:"instance"`
	Created  time.Time `json:"created"`
}

func (e logEntry) fields() fsFields {
	return fsFields{
		"callsign": fsString(e.Callsign), "emoji": fsString(e.Emoji), "revision": fsString(e.Revision),
		"version": fsString(e.Version), "instance": fsString(e.Instance),
	}
}

func entryFromDoc(d fsDocument) logEntry {
	f := d.Fields
	return logEntry{ID: d.ID(), Callsign: f.str("callsign"), Emoji: f.str("emoji"), Revision: f.str("revision"),
		Version: f.str("version"), Instance: f.str("instance"), Created: f.time("created")}
}

func newCallsign() string {
	return callsignAdj[mrand.IntN(len(callsignAdj))] + " " + callsignNoun[mrand.IntN(len(callsignNoun))] + " " +
		strconv.Itoa(10+mrand.IntN(90))
}

// --- Queries (shared by the web app and the backup job) -------------------

func latestEntries(ctx context.Context, db *firestore, n int) ([]logEntry, error) {
	docs, err := db.query(ctx, fsQuery{Coll: logCollection, OrderBy: []fsOrder{{fsFieldRef{"created"}, "DESCENDING"}}, Limit: n})
	if err != nil {
		return nil, err
	}
	out := make([]logEntry, 0, len(docs))
	for _, d := range docs {
		out = append(out, entryFromDoc(d))
	}
	return out, nil
}

// countEntries counts entries created at or before through (all, if zero).
func countEntries(ctx context.Context, db *firestore, through time.Time) (int, error) {
	q := fsQuery{Coll: logCollection}
	if !through.IsZero() {
		w := fieldFilter("created", "LESS_THAN_OR_EQUAL", fsTime(through))
		q.Where = &w
	}
	return db.count(ctx, q)
}

// entriesBetween returns entries created in (after, through], oldest first.
func entriesBetween(ctx context.Context, db *firestore, after, through time.Time) ([]logEntry, error) {
	where := and(fieldFilter("created", "GREATER_THAN", fsTime(after)), fieldFilter("created", "LESS_THAN_OR_EQUAL", fsTime(through)))
	q := fsQuery{Coll: logCollection, Where: &where, Limit: 500,
		OrderBy: []fsOrder{{fsFieldRef{"created"}, "ASCENDING"}, {fsFieldRef{"__name__"}, "ASCENDING"}}}
	var out []logEntry
	for {
		docs, err := db.query(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			out = append(out, entryFromDoc(d))
		}
		if len(docs) < q.Limit {
			return out, nil
		}
		last := docs[len(docs)-1]
		ref := last.Name
		q.StartAt = &fsCursor{Values: []fsValue{last.Fields["created"], {ReferenceValue: &ref}}}
	}
}

// signLog stores e and counts it against today's cap, in one transaction:
// the cap holds however many instances are running.
func signLog(ctx context.Context, db *firestore, e logEntry, day string) (logEntry, int, error) {
	txn, err := db.begin(ctx)
	if err != nil {
		return e, 0, err
	}
	quotaPath := quotaDocPrefix + day
	q, _, err := db.get(ctx, quotaPath, txn)
	if err != nil {
		return e, 0, err
	}
	used := q.Fields.int("signatures")
	if used >= dailySignCap {
		_ = callJSON(ctx, db.hc, db.token, http.MethodPost, db.url("/documents:rollback"), map[string]string{"transaction": txn}, nil)
		return e, used, errLogFull
	}
	one := fsInt(1)
	writes := []fsWrite{
		mustNotExist(fsWrite{
			Update:           &fsDocument{Name: db.docName(logCollection + "/" + e.ID), Fields: e.fields()},
			UpdateTransforms: []fsTransform{{FieldPath: "created", SetToServerValue: "REQUEST_TIME"}},
		}),
		{
			Update:           &fsDocument{Name: db.docName(quotaPath), Fields: fsFields{"day": fsString(day)}},
			UpdateMask:       &fsMask{[]string{"day"}},
			UpdateTransforms: []fsTransform{{FieldPath: "signatures", Increment: &one}},
		},
	}
	res, err := db.commit(ctx, txn, writes)
	if err != nil {
		return e, used, err
	}
	e.Created = res.CommitTime
	if len(res.WriteResults) > 0 && len(res.WriteResults[0].TransformResults) > 0 {
		e.Created = fsFields{"t": res.WriteResults[0].TransformResults[0]}.time("t")
	}
	return e, used + 1, nil
}

// --- The web side ---------------------------------------------------------

type flightStatus struct {
	Location         string       `json:"location"`
	DeleteProtection bool         `json:"delete_protection"`
	PITR             bool         `json:"pitr"`
	Backup           *backupState `json:"backup,omitempty"`
	SignaturesToday  int          `json:"signatures_today"`
	Error            string       `json:"error,omitempty"`
}

type flightLog struct {
	db  *firestore
	now func() time.Time

	refresh sync.Mutex // one refresh at a time per instance

	mu       sync.Mutex
	day      string
	reads    int // estimated document reads today, by this instance
	entries  []logEntry
	total    int
	listAt   time.Time
	listErr  string
	status   flightStatus
	statusAt time.Time
	lastSign time.Time
	clients  map[string]time.Time
}

func newFlightLog(db *firestore) *flightLog {
	return &flightLog{db: db, now: time.Now, clients: map[string]time.Time{}}
}

// spend reserves n reads from today's budget; false means "use the cache".
func (l *flightLog) spend(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if day := l.now().UTC().Format(time.DateOnly); day != l.day {
		l.day, l.reads = day, 0
	}
	if l.reads+n > instanceReadCap {
		return false
	}
	l.reads += n
	return true
}

func (l *flightLog) refreshIfStale(ctx context.Context) {
	l.refresh.Lock()
	defer l.refresh.Unlock()
	now := l.now()

	l.mu.Lock()
	listDue, statusDue, total := now.Sub(l.listAt) >= listTTL, now.Sub(l.statusAt) >= statusTTL, l.total
	l.mu.Unlock()

	// A list refresh reads up to listLimit entries, plus 1 per 1000 for the count.
	if listDue && l.spend(listLimit+1+total/1000) {
		entries, err := latestEntries(ctx, l.db, listLimit)
		n := 0
		if err == nil {
			n, err = countEntries(ctx, l.db, time.Time{})
		}
		l.mu.Lock()
		l.listAt = now
		if err != nil {
			l.listErr = "couldn't reach the database"
			l.listAt = now.Add(30*time.Second - listTTL) // retry in 30s, not 5 min
			slog.Error("flight log: list", "err", err)
		} else {
			l.entries, l.total, l.listErr = entries, n, ""
		}
		l.mu.Unlock()
	}

	// Database settings are free to read; the backup and quota docs are 1 read each.
	if statusDue && l.spend(2) {
		st := flightStatus{}
		info, err := l.db.database(ctx)
		if err == nil {
			st.Location = info.LocationID
			st.DeleteProtection = info.DeleteProtectionState == "DELETE_PROTECTION_ENABLED"
			st.PITR = info.PointInTimeRecovery == "POINT_IN_TIME_RECOVERY_ENABLED"
			var doc fsDocument
			var found bool
			if doc, found, err = l.db.get(ctx, backupStateDoc, ""); err == nil && found {
				b := backupStateFrom(doc.Fields)
				st.Backup = &b
			}
		}
		if err == nil {
			var q fsDocument
			if q, _, err = l.db.get(ctx, quotaDocPrefix+now.UTC().Format(time.DateOnly), ""); err == nil {
				st.SignaturesToday = q.Fields.int("signatures")
			}
		}
		if err != nil {
			st.Error = "couldn't read the database's status"
			slog.Error("flight log: status", "err", err)
		}
		l.mu.Lock()
		l.status, l.statusAt = st, now
		l.mu.Unlock()
	}
}

func (l *flightLog) snapshot() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{
		"enabled":     true,
		"entries":     l.entries,
		"total":       l.total,
		"list_age_s":  int(l.now().Sub(l.listAt).Seconds()),
		"list_error":  l.listErr,
		"cache_ttl_s": int(listTTL.Seconds()),
		"status":      l.status,
		"emojis":      logEmojis,
		"budget": map[string]int{
			"reads": l.reads, "reads_cap": instanceReadCap, "sign_cap": dailySignCap,
		},
	}
}

func (a *app) flightLogGet(w http.ResponseWriter, r *http.Request) {
	if a.flight == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "emojis": logEmojis,
			"reason": "No database here: the flight log runs on Cloud Run, where Firestore is connected."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	a.flight.refreshIfStale(ctx)
	writeJSON(w, http.StatusOK, a.flight.snapshot())
}

func (a *app) flightLogSign(w http.ResponseWriter, r *http.Request) {
	// Only this page may sign: browsers send Sec-Fetch-Site, and a JSON body
	// can't be posted cross-site without a CORS preflight we never answer.
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send JSON"})
		return
	}
	l := a.flight
	if l == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database connected"})
		return
	}
	var body struct {
		Emoji int `json:"emoji"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256)).Decode(&body); err != nil || body.Emoji < 0 || body.Emoji >= len(logEmojis) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick one of the emojis"})
		return
	}

	now := l.now()
	sum := sha256.Sum256([]byte(r.Header.Get("X-Forwarded-For")))
	client := hex.EncodeToString(sum[:8]) // only a hash, kept in memory
	l.mu.Lock()
	wait := max(signEvery-now.Sub(l.lastSign), clientEvery-now.Sub(l.clients[client]))
	if wait <= 0 {
		l.lastSign = now
		if len(l.clients) > 1000 {
			clear(l.clients)
		}
		l.clients[client] = now
	}
	l.mu.Unlock()
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "easy, pilot: one signature every few seconds", "retry_after_s": int(wait.Seconds()) + 1})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	e := logEntry{ID: randomID() + randomID(), Callsign: newCallsign(), Emoji: logEmojis[body.Emoji],
		Revision: a.revision, Version: version, Instance: a.instance}
	e, used, err := signLog(ctx, l.db, e, now.UTC().Format(time.DateOnly))
	switch {
	case errors.Is(err, errLogFull):
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "the log is full for today: it stays inside the free tier", "signatures_today": used})
		return
	case err != nil:
		a.log(r).Error("flight log: sign", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "couldn't write to the database, try again"})
		return
	}

	l.mu.Lock()
	l.entries = append([]logEntry{e}, l.entries[:min(len(l.entries), listLimit-1)]...)
	l.total++
	l.status.SignaturesToday = used
	l.mu.Unlock()
	a.log(r).Info("flight log signed", "callsign", e.Callsign)
	writeJSON(w, http.StatusCreated, map[string]any{"entry": e, "signatures_today": used})
}
