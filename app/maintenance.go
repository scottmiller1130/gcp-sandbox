package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strconv"
	"time"
)

// The nightly maintenance job, run by Cloud Scheduler. It does two things in
// one run (the free tier has only 3 Scheduler jobs per billing account):
//
//   - backs up the flight log to the vault and runs a restore drill (backup.go)
//   - prunes old revisions
//
// Cloud Run keeps every revision ever deployed. The pruner deletes old ones: it keeps the newest KEEP revisions plus any
// revision that is serving traffic, tagged (previous/candidate/green) or the
// latest ready one, so rollbacks always have something to roll back to.

const (
	runAPI      = "https://run.googleapis.com/v2"
	metadataURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
)

type revision struct {
	Name       string    `json:"name"` // projects/.../revisions/<short name>
	CreateTime time.Time `json:"createTime"`
}

type serviceStatus struct {
	LatestReadyRevision string `json:"latestReadyRevision"`
	TrafficStatuses     []struct {
		Revision string `json:"revision"`
		Percent  int    `json:"percent"`
		Tag      string `json:"tag"`
	} `json:"trafficStatuses"`
}

// revisionsToDelete returns the short names of revisions that are safe to delete.
func revisionsToDelete(revs []revision, protected map[string]bool, keep int) []string {
	sort.Slice(revs, func(i, j int) bool { return revs[i].CreateTime.After(revs[j].CreateTime) })
	var del []string
	for i, r := range revs {
		name := path.Base(r.Name)
		if i < keep || protected[name] {
			continue
		}
		del = append(del, name)
	}
	return del
}

type runClient struct {
	http  *http.Client
	base  string
	token string
}

func (c *runClient) do(ctx context.Context, method, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func runMaintenance(ctx context.Context, getenv func(string) string, hc *http.Client) error {
	token := metadataToken(hc, orDefault(getenv("METADATA_TOKEN_URL"), metadataURL))
	// Back up first: a pruning problem must never cost a night's backup.
	var backupErr error
	if getenv("BACKUP_BUCKET") == "" {
		// Not an error (local runs have no vault), but never silent: in the
		// cloud this means tonight's data has no copy.
		slog.Warn("backup skipped: BACKUP_BUCKET is not set, so new flight log entries have no copy in the vault")
	} else {
		v, err := vaultFromEnv(getenv, hc, token)
		if err == nil {
			_, err = v.backup(ctx, time.Now())
		}
		if err != nil {
			slog.Error("backup failed", "err", err)
			backupErr = fmt.Errorf("backup: %w", err)
		}
	}
	return errors.Join(backupErr, pruneRevisions(ctx, getenv, hc, token))
}

func pruneRevisions(ctx context.Context, getenv func(string) string, hc *http.Client, token tokenFunc) error {
	project, region, service := getenv("GOOGLE_CLOUD_PROJECT"), getenv("REGION"), getenv("SERVICE")
	if project == "" || region == "" || service == "" {
		return fmt.Errorf("GOOGLE_CLOUD_PROJECT, REGION and SERVICE must be set")
	}
	keep, err := strconv.Atoi(orDefault(getenv("KEEP"), "5"))
	if err != nil || keep < 1 {
		return fmt.Errorf("KEEP must be a positive number")
	}
	dryRun := getenv("DRY_RUN") == "true"

	c := &runClient{http: hc, base: orDefault(getenv("RUN_API"), runAPI)}
	if c.token, err = token(ctx); err != nil {
		return fmt.Errorf("get access token: %w", err)
	}
	svcURL := fmt.Sprintf("%s/projects/%s/locations/%s/services/%s", c.base, project, region, service)

	var svc serviceStatus
	if err := c.do(ctx, http.MethodGet, svcURL, &svc); err != nil {
		return err
	}
	protected := map[string]bool{path.Base(svc.LatestReadyRevision): true}
	for _, t := range svc.TrafficStatuses {
		if t.Percent > 0 || t.Tag != "" {
			protected[t.Revision] = true
		}
	}

	var revs []revision
	for pageToken := ""; ; {
		var page struct {
			Revisions     []revision `json:"revisions"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := c.do(ctx, http.MethodGet, svcURL+"/revisions?pageSize=100&pageToken="+pageToken, &page); err != nil {
			return err
		}
		revs = append(revs, page.Revisions...)
		if pageToken = page.NextPageToken; pageToken == "" {
			break
		}
	}

	del := revisionsToDelete(revs, protected, keep)
	slog.Info("maintenance plan", "service", service, "revisions", len(revs), "keep", keep,
		"protected", len(protected), "delete", len(del), "dry_run", dryRun)

	var failed int
	for _, name := range del {
		if dryRun {
			slog.Info("would delete revision", "revision", name)
			continue
		}
		// Deletion is a long-running operation; no need to wait for it.
		if err := c.do(ctx, http.MethodDelete, svcURL+"/revisions/"+name, nil); err != nil {
			slog.Error("delete revision", "revision", name, "err", err)
			failed++
			continue
		}
		slog.Info("deleted revision", "revision", name)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d deletions failed", failed, len(del))
	}
	return nil
}
