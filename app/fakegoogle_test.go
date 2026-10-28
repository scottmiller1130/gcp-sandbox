package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGoogle is just enough of Firestore, Cloud Storage, IAM Credentials and
// the metadata server for these tests. It checks who is calling: Firestore
// only accepts the job/instance token, the vault only the borrowed writer token.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	clock   time.Time
	docs    map[string]fsFields // "flightlog/abc" -> fields
	objects map[string][]byte   // "bucket/name" -> data
	queries int                 // runQuery + runAggregationQuery calls
	dbInfo  databaseInfo
	commits int
}

const fakeProject = "p"

func newFakeGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{t: t, clock: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), docs: map[string]fsFields{}, objects: map[string][]byte{},
		dbInfo: databaseInfo{LocationID: "nam5", DeleteProtectionState: "DELETE_PROTECTION_ENABLED", PointInTimeRecovery: "POINT_IN_TIME_RECOVERY_DISABLED"}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) env() map[string]string {
	return map[string]string{
		"GOOGLE_CLOUD_PROJECT": fakeProject,
		"FIRESTORE_API":        g.srv.URL,
		"STORAGE_API":          g.srv.URL,
		"IAM_CREDENTIALS_API":  g.srv.URL,
		"METADATA_TOKEN_URL":   g.srv.URL + "/token",
		"BACKUP_BUCKET":        "vault",
		"BACKUP_PREFIX":        "prod",
		"BACKUP_WRITER":        "backup-writer@p.iam.gserviceaccount.com",
	}
}

func (g *fakeGoogle) addEntry(id string, created time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := logEntry{ID: id, Callsign: "Test " + id, Emoji: "🚀", Revision: "r1", Version: "v1", Instance: "i1"}.fields()
	f["created"] = fsTime(created)
	g.docs[logCollection+"/"+id] = f
}

func (g *fakeGoogle) entryIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var ids []string
	for p := range g.docs {
		if id, ok := strings.CutPrefix(p, logCollection+"/"); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	auth := r.Header.Get("Authorization")
	path := r.URL.Path
	dbPrefix := "/v1/projects/" + fakeProject + "/databases/(default)"
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func(code int, msg string) { http.Error(w, msg, code) }

	switch {
	case path == "/token":
		reply(map[string]any{"access_token": "job", "expires_in": 3600})

	case strings.HasSuffix(path, ":generateAccessToken"):
		if auth != "Bearer job" {
			fail(http.StatusForbidden, "only the job may borrow the writer identity")
			return
		}
		reply(map[string]any{"accessToken": "writer", "expireTime": g.clock.Add(time.Hour)})

	case strings.HasPrefix(path, dbPrefix):
		if auth != "Bearer job" {
			fail(http.StatusUnauthorized, "firestore: bad token "+auth)
			return
		}
		g.firestore(w, r, strings.TrimPrefix(path, dbPrefix), reply, fail)

	case strings.HasPrefix(path, "/upload/storage/v1/b/") || strings.HasPrefix(path, "/storage/v1/b/"):
		if auth != "Bearer writer" {
			fail(http.StatusForbidden, "vault: only the backup writer identity may use it")
			return
		}
		g.storage(w, r, reply, fail)

	default:
		fail(http.StatusNotFound, "fake: no route for "+r.Method+" "+path)
	}
}

func (g *fakeGoogle) firestore(w http.ResponseWriter, r *http.Request, path string, reply func(any), fail func(int, string)) {
	switch {
	case path == "" && r.Method == http.MethodGet:
		reply(g.dbInfo)
	case path == "/documents:beginTransaction":
		reply(map[string]string{"transaction": "txn-1"})
	case path == "/documents:rollback":
		reply(map[string]any{})
	case path == "/documents:runQuery":
		var body struct {
			Q fakeQuery `json:"structuredQuery"`
		}
		g.decode(r, &body)
		g.queries++
		docs := g.run(body.Q)
		if len(docs) == 0 {
			reply([]map[string]string{{"readTime": g.clock.Format(time.RFC3339)}})
			return
		}
		var rows []map[string]any
		for _, d := range docs {
			rows = append(rows, map[string]any{"document": d})
		}
		reply(rows)
	case path == "/documents:runAggregationQuery":
		var body struct {
			A struct {
				Q fakeQuery `json:"structuredQuery"`
			} `json:"structuredAggregationQuery"`
		}
		g.decode(r, &body)
		g.queries++
		body.A.Q.Limit = 0
		reply([]map[string]any{{"result": map[string]any{"aggregateFields": fsFields{"n": fsInt(len(g.run(body.A.Q)))}}}})
	case path == "/documents:commit":
		var body struct {
			Writes []fsWrite `json:"writes"`
		}
		g.decode(r, &body)
		g.commit(body.Writes, reply, fail)
	case strings.HasPrefix(path, "/documents/"):
		doc := strings.TrimPrefix(path, "/documents/")
		switch r.Method {
		case http.MethodGet:
			f, ok := g.docs[doc]
			if !ok {
				fail(http.StatusNotFound, "not found")
				return
			}
			reply(fsDocument{Name: "projects/p/databases/(default)/documents/" + doc, Fields: f})
		case http.MethodPatch:
			var d fsDocument
			g.decode(r, &d)
			g.docs[doc] = d.Fields
			reply(d)
		}
	default:
		fail(http.StatusNotFound, "fake firestore: "+path)
	}
}

type fakeQuery struct {
	From    []struct{ CollectionID string } `json:"from"`
	Where   *fsFilter                       `json:"where"`
	OrderBy []fsOrder                       `json:"orderBy"`
	StartAt *fsCursor                       `json:"startAt"`
	Limit   int                             `json:"limit"`
}

func (g *fakeGoogle) run(q fakeQuery) []fsDocument {
	coll := q.From[0].CollectionID
	var docs []fsDocument
	for p, f := range g.docs {
		if id, ok := strings.CutPrefix(p, coll+"/"); ok && !strings.Contains(id, "/") && matches(q.Where, f) {
			docs = append(docs, fsDocument{Name: "projects/p/databases/(default)/documents/" + p, Fields: f})
		}
	}
	desc := len(q.OrderBy) > 0 && q.OrderBy[0].Direction == "DESCENDING"
	slices.SortFunc(docs, func(a, b fsDocument) int {
		c := a.Fields.time("created").Compare(b.Fields.time("created"))
		if c == 0 {
			c = strings.Compare(a.Name, b.Name)
		}
		if desc {
			return -c
		}
		return c
	})
	if q.StartAt != nil {
		at, _ := time.Parse(time.RFC3339Nano, *q.StartAt.Values[0].TimestampValue)
		name := *q.StartAt.Values[1].ReferenceValue
		docs = slices.DeleteFunc(docs, func(d fsDocument) bool {
			c := d.Fields.time("created").Compare(at)
			return c < 0 || c == 0 && d.Name <= name
		})
	}
	if q.Limit > 0 && len(docs) > q.Limit {
		docs = docs[:q.Limit]
	}
	return docs
}

func matches(f *fsFilter, fields fsFields) bool {
	if f == nil {
		return true
	}
	if f.CompositeFilter != nil {
		for _, sub := range f.CompositeFilter.Filters {
			if !matches(&sub, fields) {
				return false
			}
		}
		return true
	}
	ff := f.FieldFilter
	want, _ := time.Parse(time.RFC3339Nano, *ff.Value.TimestampValue)
	c := fields.time(ff.Field.FieldPath).Compare(want)
	switch ff.Op {
	case "GREATER_THAN":
		return c > 0
	case "LESS_THAN_OR_EQUAL":
		return c <= 0
	}
	panic("fake: unsupported op " + ff.Op)
}

func (g *fakeGoogle) commit(writes []fsWrite, reply func(any), fail func(int, string)) {
	for _, wr := range writes {
		doc := strings.SplitN(wr.Update.Name, "/documents/", 2)[1]
		if wr.CurrentDocument != nil && !wr.CurrentDocument.Exists {
			if _, ok := g.docs[doc]; ok {
				fail(http.StatusConflict, "ALREADY_EXISTS")
				return
			}
		}
	}
	g.commits++
	g.clock = g.clock.Add(time.Second)
	var results []map[string]any
	for _, wr := range writes {
		doc := strings.SplitN(wr.Update.Name, "/documents/", 2)[1]
		f := fsFields{}
		if wr.UpdateMask != nil {
			for k, v := range g.docs[doc] {
				f[k] = v
			}
			for _, k := range wr.UpdateMask.FieldPaths {
				f[k] = wr.Update.Fields[k]
			}
		} else {
			for k, v := range wr.Update.Fields {
				f[k] = v
			}
		}
		var tr []fsValue
		for _, t := range wr.UpdateTransforms {
			switch {
			case t.SetToServerValue == "REQUEST_TIME":
				f[t.FieldPath] = fsTime(g.clock)
			case t.Increment != nil:
				f[t.FieldPath] = fsInt(f.int(t.FieldPath) + fsFields{"i": *t.Increment}.int("i"))
			}
			tr = append(tr, f[t.FieldPath])
		}
		g.docs[doc] = f
		results = append(results, map[string]any{"transformResults": tr})
	}
	reply(map[string]any{"writeResults": results, "commitTime": g.clock})
}

func (g *fakeGoogle) storage(w http.ResponseWriter, r *http.Request, reply func(any), fail func(int, string)) {
	q := r.URL.Query()
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/"):
		bucket := strings.Split(strings.TrimPrefix(r.URL.Path, "/upload/storage/v1/b/"), "/")[0]
		key := bucket + "/" + q.Get("name")
		if q.Get("ifGenerationMatch") != "0" {
			fail(http.StatusBadRequest, "uploads must be create-only")
			return
		}
		if _, ok := g.objects[key]; ok {
			fail(http.StatusPreconditionFailed, "object already exists")
			return
		}
		g.objects[key], _ = io.ReadAll(r.Body)
		reply(map[string]string{"name": q.Get("name")})
	case q.Get("alt") == "media":
		rest := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/")
		bucket, name, _ := strings.Cut(rest, "/o/")
		data, ok := g.objects[bucket+"/"+name]
		if !ok {
			fail(http.StatusNotFound, "no such object")
			return
		}
		_, _ = w.Write(data)
	default:
		bucket := strings.Split(strings.TrimPrefix(r.URL.Path, "/storage/v1/b/"), "/")[0]
		var items []map[string]string
		for key := range g.objects {
			if name, ok := strings.CutPrefix(key, bucket+"/"); ok && strings.HasPrefix(name, q.Get("prefix")) {
				items = append(items, map[string]string{"name": name})
			}
		}
		reply(map[string]any{"items": items})
	}
}

func (g *fakeGoogle) decode(r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		panic(fmt.Sprintf("fake: bad request body for %s: %v", r.URL.Path, err))
	}
}
