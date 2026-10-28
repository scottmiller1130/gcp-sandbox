package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Free backups for the flight log. Firestore's managed backups and
// point-in-time recovery are billed, so the nightly maintenance job does it
// itself:
//
//  1. export   entries created since the last backup, as one JSON file
//  2. vault    upload it to a bucket in the *shared* project, using an identity
//              that can add files but never change or delete them; the bucket's
//              retention policy stops anyone deleting them for 90 days
//  3. drill    read the file back from the vault, check it byte-for-byte and by
//              checksum, then check the database holds exactly as many entries
//              as all the backups together - so nothing was missed
//
// Each step's result is saved in meta/backup, which Mission Control shows.
// `server restore` replays every file in the vault back into Firestore.

const (
	backupFormat = "flightlog-backup/v1"
	// Entries newer than this are left for the next run, so an entry whose
	// transaction is still committing can't slip between two backups.
	backupLag     = time.Minute
	restoreBatch  = 400 // Firestore allows 500 writes per commit
	defaultMaxRes = 15000
)

type backupFile struct {
	Format  string     `json:"format"`
	Project string     `json:"project"`
	After   time.Time  `json:"after"`   // exclusive
	Through time.Time  `json:"through"` // inclusive
	Count   int        `json:"count"`
	SHA256  string     `json:"sha256"` // of the JSON encoding of Entries
	Entries []logEntry `json:"entries"`
}

type backupState struct {
	Cursor       time.Time `json:"cursor"` // everything up to here is in the vault
	Total        int       `json:"total"`  // entries in the vault
	LastObject   string    `json:"last_object"`
	LastAt       time.Time `json:"last_at"`
	LastCount    int       `json:"last_count"`
	DrillOK      bool      `json:"drill_ok"`
	DrillAt      time.Time `json:"drill_at"`
	DrillMessage string    `json:"drill_message"`
}

func (b backupState) fields() fsFields {
	return fsFields{
		"cursor": fsTime(b.Cursor), "total": fsInt(b.Total), "last_object": fsString(b.LastObject),
		"last_at": fsTime(b.LastAt), "last_count": fsInt(b.LastCount), "drill_ok": fsBool(b.DrillOK),
		"drill_at": fsTime(b.DrillAt), "drill_message": fsString(b.DrillMessage),
	}
}

func backupStateFrom(f fsFields) backupState {
	return backupState{Cursor: f.time("cursor"), Total: f.int("total"), LastObject: f.str("last_object"),
		LastAt: f.time("last_at"), LastCount: f.int("last_count"), DrillOK: f.bool("drill_ok"),
		DrillAt: f.time("drill_at"), DrillMessage: f.str("drill_message")}
}

func digest(entries []logEntry) string {
	b, _ := json.Marshal(entries)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// parseBackup reads a backup file and checks it is complete and unaltered.
func parseBackup(data []byte) (backupFile, error) {
	var f backupFile
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("not a backup file: %w", err)
	}
	if f.Format != backupFormat {
		return f, fmt.Errorf("unknown format %q", f.Format)
	}
	if f.Count != len(f.Entries) {
		return f, fmt.Errorf("says %d entries but holds %d", f.Count, len(f.Entries))
	}
	if got := digest(f.Entries); got != f.SHA256 {
		return f, fmt.Errorf("checksum mismatch: the file was altered or truncated")
	}
	return f, nil
}

type vault struct {
	db     *firestore
	store  *storage
	bucket string
	prefix string // e.g. "prod/", so environments never mix
}

func (v *vault) dir() string { return v.prefix + "flightlog/" }

func (v *vault) state(ctx context.Context) (backupState, error) {
	doc, _, err := v.db.get(ctx, backupStateDoc, "")
	return backupStateFrom(doc.Fields), err
}

func (v *vault) backup(ctx context.Context, now time.Time) (backupState, error) {
	prev, err := v.state(ctx)
	if err != nil {
		return prev, fmt.Errorf("read backup state: %w", err)
	}
	through := now.Add(-backupLag).UTC().Truncate(time.Second)
	if !through.After(prev.Cursor) {
		return prev, nil
	}
	entries, err := entriesBetween(ctx, v.db, prev.Cursor, through)
	if err != nil {
		return prev, fmt.Errorf("export: %w", err)
	}
	file := backupFile{Format: backupFormat, Project: v.db.project, After: prev.Cursor, Through: through,
		Count: len(entries), SHA256: digest(entries), Entries: entries}
	if file.Entries == nil {
		file.Entries = []logEntry{}
		file.SHA256 = digest(file.Entries)
	}
	data, err := json.MarshalIndent(file, "", " ")
	if err != nil {
		return prev, err
	}
	name := v.dir() + through.Format("20060102T150405Z") + ".json"
	if err := v.store.create(ctx, v.bucket, name, data); err != nil {
		return prev, fmt.Errorf("upload to vault: %w", err)
	}
	slog.Info("backup written", "object", name, "entries", len(entries))

	next := prev
	next.LastObject, next.LastAt, next.LastCount, next.DrillAt = name, now.UTC(), len(entries), now.UTC()
	total := prev.Total + len(entries)
	inDB, drillErr := v.drill(ctx, name, data, through)
	if drillErr == nil && inDB != total {
		drillErr = fmt.Errorf("the database holds %d entries up to %s but the backups hold %d: something was missed or deleted",
			inDB, through.Format(time.RFC3339), total)
	}
	if drillErr != nil {
		// Keep the cursor where it was: the next run backs this range up again.
		next.DrillOK, next.DrillMessage = false, drillErr.Error()
	} else {
		next.Cursor, next.Total, next.DrillOK = through, total, true
		next.DrillMessage = fmt.Sprintf("Read %d new entries back from the vault, checksum matches; database and backups both hold %d", len(entries), total)
	}
	if err := v.db.set(ctx, backupStateDoc, next.fields()); err != nil {
		return next, fmt.Errorf("save backup state: %w", err)
	}
	if drillErr != nil {
		slog.Error("restore drill failed", "object", name, "err", drillErr)
		return next, fmt.Errorf("restore drill: %w", drillErr)
	}
	slog.Info("restore drill passed", "object", name, "entries", len(entries), "total", next.Total)
	return next, nil
}

// drill proves the file just written can be restored, and returns how many
// entries the database holds up to through.
func (v *vault) drill(ctx context.Context, name string, written []byte, through time.Time) (int, error) {
	got, err := v.store.read(ctx, v.bucket, name)
	if err != nil {
		return 0, fmt.Errorf("couldn't read the backup back from the vault: %w", err)
	}
	if !bytes.Equal(got, written) {
		return 0, fmt.Errorf("the vault copy differs from what was written")
	}
	if _, err := parseBackup(got); err != nil {
		return 0, err
	}
	return countEntries(ctx, v.db, through)
}

// restore writes every entry in the vault back into Firestore. It is
// idempotent: entries keep their IDs, so running it twice changes nothing.
func (v *vault) restore(ctx context.Context, dryRun bool, maxEntries int) (int, error) {
	names, err := v.store.list(ctx, v.bucket, v.dir())
	if err != nil {
		return 0, fmt.Errorf("list vault: %w", err)
	}
	slices.Sort(names)
	byID := map[string]logEntry{}
	for _, name := range names {
		data, err := v.store.read(ctx, v.bucket, name)
		if err != nil {
			return 0, err
		}
		f, err := parseBackup(data)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		for _, e := range f.Entries {
			byID[e.ID] = e
		}
	}
	slog.Info("restore plan", "files", len(names), "entries", len(byID), "dry_run", dryRun)
	// Each entry is one write; keep a restore inside the daily free tier.
	if len(byID) > maxEntries {
		return 0, fmt.Errorf("%d entries is more than RESTORE_MAX (%d): raise it knowing writes past 20k/day are billed", len(byID), maxEntries)
	}
	if dryRun {
		return len(byID), nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for chunk := range slices.Chunk(ids, restoreBatch) {
		writes := make([]fsWrite, 0, len(chunk))
		for _, id := range chunk {
			e := byID[id]
			fields := e.fields()
			fields["created"] = fsTime(e.Created)
			writes = append(writes, fsWrite{Update: &fsDocument{Name: v.db.docName(logCollection + "/" + id), Fields: fields}})
		}
		if _, err := v.db.commit(ctx, "", writes); err != nil {
			return 0, fmt.Errorf("write entries: %w", err)
		}
	}
	slog.Info("restore complete", "entries", len(ids))
	return len(ids), nil
}

// vaultFromEnv wires the job to Firestore (as itself) and the vault (as the
// write-only backup identity it is allowed to borrow).
func vaultFromEnv(getenv func(string) string, hc *http.Client, jobToken tokenFunc) (*vault, error) {
	project, bucket := getenv("GOOGLE_CLOUD_PROJECT"), getenv("BACKUP_BUCKET")
	if project == "" || bucket == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT and BACKUP_BUCKET must be set")
	}
	prefix := getenv("BACKUP_PREFIX")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	vaultToken := jobToken
	if writer := getenv("BACKUP_WRITER"); writer != "" {
		vaultToken = impersonate(hc, orDefault(getenv("IAM_CREDENTIALS_API"), iamCredentialsAPI), jobToken, writer)
	}
	return &vault{
		db:     &firestore{hc: hc, base: orDefault(getenv("FIRESTORE_API"), firestoreAPI), project: project, token: jobToken},
		store:  &storage{hc: hc, base: orDefault(getenv("STORAGE_API"), storageAPI), token: vaultToken},
		bucket: bucket,
		prefix: prefix,
	}, nil
}

// runRestore is `server restore`, run as a one-off execution of the job.
func runRestore(ctx context.Context, getenv func(string) string, hc *http.Client) error {
	token := metadataToken(hc, orDefault(getenv("METADATA_TOKEN_URL"), metadataURL))
	v, err := vaultFromEnv(getenv, hc, token)
	if err != nil {
		return err
	}
	maxEntries := defaultMaxRes
	if s := getenv("RESTORE_MAX"); s != "" {
		if maxEntries, err = strconv.Atoi(s); err != nil || maxEntries < 0 {
			return fmt.Errorf("RESTORE_MAX must be a number")
		}
	}
	_, err = v.restore(ctx, getenv("DRY_RUN") == "true", maxEntries)
	return err
}
