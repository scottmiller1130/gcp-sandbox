package main

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func testVault(t *testing.T, g *fakeGoogle) *vault {
	t.Helper()
	env := g.env()
	getenv := func(k string) string { return env[k] }
	hc := g.srv.Client()
	v, err := vaultFromEnv(getenv, hc, metadataToken(hc, env["METADATA_TOKEN_URL"]))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (g *fakeGoogle) objectNames() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var names []string
	for k := range g.objects {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func TestBackupIsIncrementalAndDrilled(t *testing.T) {
	g := newFakeGoogle(t)
	v := testVault(t, g)
	ctx := context.Background()
	day1 := g.clock
	for _, id := range []string{"a", "b", "c"} {
		g.addEntry(id, day1.Add(-time.Hour))
	}

	st, err := v.backup(ctx, day1)
	if err != nil {
		t.Fatal(err)
	}
	if !st.DrillOK || st.Total != 3 || st.LastCount != 3 || !strings.Contains(st.DrillMessage, "both hold 3") {
		t.Fatalf("first backup: %+v", st)
	}
	if objs := g.objectNames(); len(objs) != 1 || !strings.HasPrefix(objs[0], "vault/prod/flightlog/") {
		t.Fatalf("vault holds %v", objs)
	}

	// Next night: only the new entries are exported.
	day2 := day1.Add(24 * time.Hour)
	g.addEntry("d", day2.Add(-2*time.Hour))
	g.addEntry("e", day2.Add(-2*time.Hour))
	if st, err = v.backup(ctx, day2); err != nil {
		t.Fatal(err)
	}
	if !st.DrillOK || st.Total != 5 || st.LastCount != 2 {
		t.Fatalf("second backup: %+v", st)
	}
	if saved := backupStateFrom(g.docs[backupStateDoc]); saved.Total != 5 || !saved.DrillOK {
		t.Fatalf("state in Firestore: %+v", saved)
	}

	// An entry younger than backupLag waits for the next run.
	day3 := day2.Add(24 * time.Hour)
	g.addEntry("f", day3.Add(-10*time.Second))
	if st, _ = v.backup(ctx, day3); st.LastCount != 0 || st.Total != 5 {
		t.Fatalf("an entry still committing was backed up: %+v", st)
	}
}

func TestDrillCatchesAMissedEntry(t *testing.T) {
	g := newFakeGoogle(t)
	v := testVault(t, g)
	ctx := context.Background()
	g.addEntry("a", g.clock.Add(-time.Hour))
	if _, err := v.backup(ctx, g.clock); err != nil {
		t.Fatal(err)
	}
	// An entry appears *behind* the cursor, which incremental export can't see.
	g.addEntry("late", g.clock.Add(-30*time.Minute))
	st, err := v.backup(ctx, g.clock.Add(time.Hour))
	if err == nil || st.DrillOK || !strings.Contains(st.DrillMessage, "missed or deleted") {
		t.Fatalf("drill should fail: err=%v state=%+v", err, st)
	}
	if st.Total != 1 {
		t.Fatalf("cursor advanced past a failed drill: %+v", st)
	}
}

func TestVaultNeverOverwrites(t *testing.T) {
	g := newFakeGoogle(t)
	v := testVault(t, g)
	ctx := context.Background()
	if err := v.store.create(ctx, "vault", "prod/flightlog/x.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	err := v.store.create(ctx, "vault", "prod/flightlog/x.json", []byte(`{"evil":true}`))
	if !isStatus(err, http.StatusPreconditionFailed) {
		t.Fatalf("overwrite: %v", err)
	}
	if string(g.objects["vault/prod/flightlog/x.json"]) != "{}" {
		t.Fatal("object was replaced")
	}
}

func TestRestoreBringsEverythingBack(t *testing.T) {
	g := newFakeGoogle(t)
	v := testVault(t, g)
	ctx := context.Background()
	for i, id := range []string{"a", "b", "c", "d"} {
		g.addEntry(id, g.clock.Add(-time.Duration(10-i)*time.Hour))
		if i == 1 {
			if _, err := v.backup(ctx, g.clock.Add(-8*time.Hour-30*time.Minute)); err != nil { // after b, before c
				t.Fatal(err)
			}
		}
	}
	if _, err := v.backup(ctx, g.clock); err != nil {
		t.Fatal(err)
	}
	before := map[string]fsFields{}
	for _, id := range g.entryIDs() {
		before[id] = g.docs[logCollection+"/"+id]
	}

	// Disaster: every entry is deleted.
	for id := range before {
		delete(g.docs, logCollection+"/"+id)
	}
	if n, err := v.restore(ctx, true, 100); err != nil || n != 4 || len(g.entryIDs()) != 0 {
		t.Fatalf("dry run: n=%d err=%v ids=%v", n, err, g.entryIDs())
	}
	for range 2 { // idempotent
		if n, err := v.restore(ctx, false, 100); err != nil || n != 4 {
			t.Fatalf("restore: n=%d err=%v", n, err)
		}
	}
	for id, want := range before {
		got := g.docs[logCollection+"/"+id]
		if entryFromDoc(fsDocument{Name: id, Fields: got}) != entryFromDoc(fsDocument{Name: id, Fields: want}) {
			t.Fatalf("%s restored as %v, want %v", id, got, want)
		}
	}
	if _, err := v.restore(ctx, false, 3); err == nil || !strings.Contains(err.Error(), "RESTORE_MAX") {
		t.Fatalf("restore over RESTORE_MAX: %v", err)
	}
}

func TestRestoreRefusesATamperedBackup(t *testing.T) {
	g := newFakeGoogle(t)
	v := testVault(t, g)
	ctx := context.Background()
	g.addEntry("a", g.clock.Add(-time.Hour))
	if _, err := v.backup(ctx, g.clock); err != nil {
		t.Fatal(err)
	}
	for k, data := range g.objects {
		g.objects[k] = []byte(strings.Replace(string(data), "Test a", "Evil a", 1))
	}
	if _, err := v.restore(ctx, false, 100); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered backup restored: %v", err)
	}
}

func TestMaintenanceBacksUpEvenIfPruningFails(t *testing.T) {
	g := newFakeGoogle(t)
	g.addEntry("a", g.clock.Add(-time.Hour))
	env := g.env() // no SERVICE/REGION: pruning fails
	err := runMaintenance(context.Background(), func(k string) string { return env[k] }, g.srv.Client())
	if err == nil || !strings.Contains(err.Error(), "SERVICE") {
		t.Fatalf("expected the pruning error, got %v", err)
	}
	if st := backupStateFrom(g.docs[backupStateDoc]); !st.DrillOK || st.Total != 1 {
		t.Fatalf("backup didn't run: %+v", st)
	}
}

func TestMaintenanceWarnsWhenBackupIsNotConfigured(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_ = runMaintenance(context.Background(), func(string) string { return "" }, http.DefaultClient)
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "backup skipped") {
		t.Fatalf("no warning when the vault isn't configured:\n%s", logs.String())
	}
}
