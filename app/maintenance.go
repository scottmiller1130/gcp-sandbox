package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strconv"
	"time"
)

// Cloud Run keeps every revision ever deployed. This job, run daily by Cloud
// Scheduler, deletes old ones: it keeps the newest KEEP revisions plus any
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
	if c.token, err = accessToken(ctx, hc, orDefault(getenv("METADATA_TOKEN_URL"), metadataURL)); err != nil {
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

// accessToken gets a token for the job's service account from the metadata server.
func accessToken(ctx context.Context, hc *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata server: %s", resp.Status)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}
