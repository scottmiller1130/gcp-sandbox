package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRevisionsToDelete(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var revs []revision
	for i := 1; i <= 8; i++ { // r1 oldest ... r8 newest
		revs = append(revs, revision{Name: fmt.Sprintf("projects/p/locations/l/services/s/revisions/r%d", i), CreateTime: base.Add(time.Duration(i) * time.Hour)})
	}
	got := revisionsToDelete(revs, map[string]bool{"r2": true}, 3)
	// keep r8 r7 r6 (newest 3) and r2 (protected)
	if want := []string{"r5", "r4", "r3", "r1"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRunMaintenance(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing header", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok"}`)
	})
	svc := "/projects/p/locations/l/services/s"
	mux.HandleFunc("GET "+svc, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"latestReadyRevision":"projects/p/locations/l/services/s/revisions/r6",
			"trafficStatuses":[{"revision":"r1","percent":100},{"revision":"r2","tag":"previous"},{"revision":"r3","percent":0}]}`)
	})
	mux.HandleFunc("GET "+svc+"/revisions", func(w http.ResponseWriter, r *http.Request) {
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		page := r.URL.Query().Get("pageToken")
		var revs []revision
		next := ""
		lo, hi := 1, 3
		if page == "p2" {
			lo, hi = 4, 6
		} else {
			next = "p2"
		}
		for i := lo; i <= hi; i++ {
			revs = append(revs, revision{Name: fmt.Sprintf("projects/p/locations/l/services/s/revisions/r%d", i), CreateTime: base.Add(time.Duration(i) * time.Hour)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revisions": revs, "nextPageToken": next})
	})
	mux.HandleFunc("DELETE "+svc+"/revisions/{name}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = append(deleted, r.PathValue("name"))
		mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	env := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "p", "REGION": "l", "SERVICE": "s", "KEEP": "1",
		"RUN_API": srv.URL, "METADATA_TOKEN_URL": srv.URL + "/token",
	}
	if err := runMaintenance(context.Background(), func(k string) string { return env[k] }, srv.Client()); err != nil {
		t.Fatal(err)
	}
	// r6 newest (kept, also latest ready), r1 serving, r2 tagged -> delete r5 r4 r3
	// (r3 has a 0% traffic entry with no tag, so it isn't protected)
	if want := []string{"r5", "r4", "r3"}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}

	deleted = nil
	env["DRY_RUN"] = "true"
	if err := runMaintenance(context.Background(), func(k string) string { return env[k] }, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("dry run deleted %v", deleted)
	}
}

func TestRunMaintenanceRequiresConfig(t *testing.T) {
	err := runMaintenance(context.Background(), func(string) string { return "" }, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "must be set") {
		t.Fatalf("got %v", err)
	}
}
