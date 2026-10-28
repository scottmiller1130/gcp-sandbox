package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Small clients for the Google APIs the app uses (Firestore, Cloud Storage,
// IAM Credentials), over plain REST, so the image stays stdlib-only.

const (
	firestoreAPI      = "https://firestore.googleapis.com"
	storageAPI        = "https://storage.googleapis.com"
	iamCredentialsAPI = "https://iamcredentials.googleapis.com"
	cloudPlatform     = "https://www.googleapis.com/auth/cloud-platform"
)

// tokenFunc returns an OAuth access token for one identity.
type tokenFunc func(ctx context.Context) (string, error)

// apiError is a non-2xx answer from a Google API.
type apiError struct {
	Method, URL string
	Status      int
	Body        string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.URL, e.Status, http.StatusText(e.Status), e.Body)
}

func isStatus(err error, code int) bool {
	e, ok := err.(*apiError)
	return ok && e.Status == code
}

// callJSON sends body (if any) as JSON and decodes the answer into out (if any).
func callJSON(ctx context.Context, hc *http.Client, token tokenFunc, method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	return callRaw(ctx, hc, token, method, u, "application/json", rd, out)
}

func callRaw(ctx context.Context, hc *http.Client, token tokenFunc, method, u, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if token != nil {
		tok, err := token(ctx)
		if err != nil {
			return fmt.Errorf("get access token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &apiError{Method: method, URL: u, Status: resp.StatusCode, Body: strings.TrimSpace(string(data[:min(len(data), 500)]))}
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = data
		return nil
	default:
		return json.Unmarshal(data, out)
	}
}

// --- Tokens ---------------------------------------------------------------

// cachedToken reuses a token until a minute before it expires.
type cachedToken struct {
	fetch func(ctx context.Context) (string, time.Time, error)
	mu    sync.Mutex
	tok   string
	exp   time.Time
}

func (c *cachedToken) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && time.Until(c.exp) > time.Minute {
		return c.tok, nil
	}
	tok, exp, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.tok, c.exp = tok, exp
	return tok, nil
}

// metadataToken gets tokens for the identity this instance or job runs as.
func metadataToken(hc *http.Client, url string) tokenFunc {
	c := &cachedToken{fetch: func(ctx context.Context) (string, time.Time, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := hc.Do(req)
		if err != nil {
			return "", time.Time{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", time.Time{}, fmt.Errorf("metadata server: %s", resp.Status)
		}
		var tok struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
			return "", time.Time{}, err
		}
		return tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second), nil
	}}
	return c.token
}

// impersonate borrows another service account's identity for a few minutes.
// The caller needs roles/iam.serviceAccountTokenCreator on that account.
func impersonate(hc *http.Client, base string, source tokenFunc, email string) tokenFunc {
	c := &cachedToken{fetch: func(ctx context.Context) (string, time.Time, error) {
		var out struct {
			AccessToken string    `json:"accessToken"`
			ExpireTime  time.Time `json:"expireTime"`
		}
		u := base + "/v1/projects/-/serviceAccounts/" + url.PathEscape(email) + ":generateAccessToken"
		err := callJSON(ctx, hc, source, http.MethodPost, u, map[string]any{"scope": []string{cloudPlatform}, "lifetime": "600s"}, &out)
		return out.AccessToken, out.ExpireTime, err
	}}
	return c.token
}

// --- Firestore ------------------------------------------------------------

// fsValue is one Firestore field value (only the types this app stores).
type fsValue struct {
	StringValue    *string `json:"stringValue,omitempty"`
	IntegerValue   *string `json:"integerValue,omitempty"`
	BooleanValue   *bool   `json:"booleanValue,omitempty"`
	TimestampValue *string `json:"timestampValue,omitempty"`
	ReferenceValue *string `json:"referenceValue,omitempty"`
}

func fsString(s string) fsValue { return fsValue{StringValue: &s} }
func fsInt(n int) fsValue {
	s := strconv.Itoa(n)
	return fsValue{IntegerValue: &s}
}
func fsBool(b bool) fsValue { return fsValue{BooleanValue: &b} }
func fsTime(t time.Time) fsValue {
	s := t.UTC().Format(time.RFC3339Nano)
	return fsValue{TimestampValue: &s}
}

type fsFields map[string]fsValue

func (f fsFields) str(k string) string {
	if v := f[k].StringValue; v != nil {
		return *v
	}
	return ""
}

func (f fsFields) int(k string) int {
	if v := f[k].IntegerValue; v != nil {
		n, _ := strconv.Atoi(*v)
		return n
	}
	return 0
}

func (f fsFields) bool(k string) bool {
	v := f[k].BooleanValue
	return v != nil && *v
}

func (f fsFields) time(k string) time.Time {
	if v := f[k].TimestampValue; v != nil {
		t, _ := time.Parse(time.RFC3339Nano, *v)
		return t
	}
	return time.Time{}
}

type fsDocument struct {
	Name   string   `json:"name,omitempty"`
	Fields fsFields `json:"fields"`
}

// ID is the last part of the document's path.
func (d fsDocument) ID() string { return d.Name[strings.LastIndex(d.Name, "/")+1:] }

// Structured queries: only the parts this app uses.
type fsFieldRef struct {
	FieldPath string `json:"fieldPath"`
}

type fsFilter struct {
	FieldFilter     *fsFieldFilter     `json:"fieldFilter,omitempty"`
	CompositeFilter *fsCompositeFilter `json:"compositeFilter,omitempty"`
}

type fsFieldFilter struct {
	Field fsFieldRef `json:"field"`
	Op    string     `json:"op"`
	Value fsValue    `json:"value"`
}

type fsCompositeFilter struct {
	Op      string     `json:"op"`
	Filters []fsFilter `json:"filters"`
}

func fieldFilter(field, op string, v fsValue) fsFilter {
	return fsFilter{FieldFilter: &fsFieldFilter{fsFieldRef{field}, op, v}}
}

func and(filters ...fsFilter) fsFilter {
	return fsFilter{CompositeFilter: &fsCompositeFilter{"AND", filters}}
}

type fsOrder struct {
	Field     fsFieldRef `json:"field"`
	Direction string     `json:"direction"`
}

type fsCursor struct {
	Values []fsValue `json:"values"`
	Before bool      `json:"before"`
}

type fsQuery struct {
	Coll    string    `json:"-"` // collection id
	Where   *fsFilter `json:"where,omitempty"`
	OrderBy []fsOrder `json:"orderBy,omitempty"`
	StartAt *fsCursor `json:"startAt,omitempty"`
	Limit   int       `json:"limit,omitempty"`
}

func (q fsQuery) MarshalJSON() ([]byte, error) {
	type plain fsQuery
	return json.Marshal(struct {
		From []map[string]string `json:"from"`
		plain
	}{[]map[string]string{{"collectionId": q.Coll}}, plain(q)})
}

type fsWrite struct {
	Update           *fsDocument     `json:"update,omitempty"`
	UpdateMask       *fsMask         `json:"updateMask,omitempty"`
	UpdateTransforms []fsTransform   `json:"updateTransforms,omitempty"`
	CurrentDocument  *fsPrecondition `json:"currentDocument,omitempty"`
}

type fsMask struct {
	FieldPaths []string `json:"fieldPaths"`
}

type fsPrecondition struct {
	Exists bool `json:"exists"`
}

type fsTransform struct {
	FieldPath        string   `json:"fieldPath"`
	SetToServerValue string   `json:"setToServerValue,omitempty"`
	Increment        *fsValue `json:"increment,omitempty"`
}

func mustNotExist(w fsWrite) fsWrite {
	w.CurrentDocument = &fsPrecondition{Exists: false}
	return w
}

// firestore talks to one project's (default) database.
type firestore struct {
	hc      *http.Client
	base    string
	project string
	token   tokenFunc
}

func (f *firestore) dbName() string {
	return "projects/" + f.project + "/databases/(default)"
}

func (f *firestore) docName(path string) string { return f.dbName() + "/documents/" + path }

func (f *firestore) url(suffix string) string { return f.base + "/v1/" + f.dbName() + suffix }

func (f *firestore) get(ctx context.Context, path, txn string) (fsDocument, bool, error) {
	u := f.url("/documents/" + path)
	if txn != "" {
		u += "?transaction=" + url.QueryEscape(txn)
	}
	var d fsDocument
	err := callJSON(ctx, f.hc, f.token, http.MethodGet, u, nil, &d)
	if isStatus(err, http.StatusNotFound) {
		return fsDocument{}, false, nil
	}
	return d, err == nil, err
}

// set creates or replaces a document.
func (f *firestore) set(ctx context.Context, path string, fields fsFields) error {
	return callJSON(ctx, f.hc, f.token, http.MethodPatch, f.url("/documents/"+path), fsDocument{Fields: fields}, nil)
}

func (f *firestore) query(ctx context.Context, q fsQuery) ([]fsDocument, error) {
	var rows []struct {
		Document *fsDocument `json:"document"`
	}
	if err := callJSON(ctx, f.hc, f.token, http.MethodPost, f.url("/documents:runQuery"), map[string]any{"structuredQuery": q}, &rows); err != nil {
		return nil, err
	}
	var docs []fsDocument
	for _, r := range rows {
		if r.Document != nil {
			docs = append(docs, *r.Document)
		}
	}
	return docs, nil
}

// count runs a COUNT aggregation: one read per 1000 matching documents.
func (f *firestore) count(ctx context.Context, q fsQuery) (int, error) {
	var rows []struct {
		Result struct {
			AggregateFields fsFields `json:"aggregateFields"`
		} `json:"result"`
	}
	body := map[string]any{"structuredAggregationQuery": map[string]any{
		"structuredQuery": q,
		"aggregations":    []map[string]any{{"alias": "n", "count": map[string]any{}}},
	}}
	if err := callJSON(ctx, f.hc, f.token, http.MethodPost, f.url("/documents:runAggregationQuery"), body, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("count: empty response")
	}
	return rows[0].Result.AggregateFields.int("n"), nil
}

func (f *firestore) begin(ctx context.Context) (string, error) {
	var out struct {
		Transaction string `json:"transaction"`
	}
	err := callJSON(ctx, f.hc, f.token, http.MethodPost, f.url("/documents:beginTransaction"), map[string]any{}, &out)
	return out.Transaction, err
}

type fsCommitResult struct {
	WriteResults []struct {
		TransformResults []fsValue `json:"transformResults"`
	} `json:"writeResults"`
	CommitTime time.Time `json:"commitTime"`
}

// commit applies writes atomically (inside txn, when given).
func (f *firestore) commit(ctx context.Context, txn string, writes []fsWrite) (fsCommitResult, error) {
	body := map[string]any{"writes": writes}
	if txn != "" {
		body["transaction"] = txn
	}
	var out fsCommitResult
	err := callJSON(ctx, f.hc, f.token, http.MethodPost, f.url("/documents:commit"), body, &out)
	return out, err
}

// databaseInfo is the database's own configuration: where its data lives and
// what protects it. Reading it isn't billed as a document read.
type databaseInfo struct {
	LocationID            string `json:"locationId"`
	DeleteProtectionState string `json:"deleteProtectionState"`
	PointInTimeRecovery   string `json:"pointInTimeRecoveryEnablement"`
}

func (f *firestore) database(ctx context.Context) (databaseInfo, error) {
	var d databaseInfo
	err := callJSON(ctx, f.hc, f.token, http.MethodGet, f.base+"/v1/"+f.dbName(), nil, &d)
	return d, err
}

// --- Cloud Storage --------------------------------------------------------

type storage struct {
	hc    *http.Client
	base  string
	token tokenFunc
}

// objectPath escapes an object name for the JSON API, slashes included.
func objectPath(name string) string {
	return strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
}

// create uploads a new object and fails if one with that name already exists,
// so a backup can never overwrite an earlier one.
func (s *storage) create(ctx context.Context, bucket, name string, data []byte) error {
	u := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&ifGenerationMatch=0&name=%s", s.base, bucket, url.QueryEscape(name))
	return callRaw(ctx, s.hc, s.token, http.MethodPost, u, "application/json", bytes.NewReader(data), nil)
}

func (s *storage) read(ctx context.Context, bucket, name string) ([]byte, error) {
	var data []byte
	err := callRaw(ctx, s.hc, s.token, http.MethodGet, fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media", s.base, bucket, objectPath(name)), "", nil, &data)
	return data, err
}

func (s *storage) list(ctx context.Context, bucket, prefix string) ([]string, error) {
	var names []string
	for pageToken := ""; ; {
		var page struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		u := fmt.Sprintf("%s/storage/v1/b/%s/o?prefix=%s&pageToken=%s", s.base, bucket, url.QueryEscape(prefix), url.QueryEscape(pageToken))
		if err := callJSON(ctx, s.hc, s.token, http.MethodGet, u, nil, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			names = append(names, it.Name)
		}
		if pageToken = page.NextPageToken; pageToken == "" {
			return names, nil
		}
	}
}
