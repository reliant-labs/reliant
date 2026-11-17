// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

const signerEmail = "storage-reader@proj.iam.gserviceaccount.com"

// fakeSigner stands in for IAM signBlob: it records every string-to-sign
// and answers a fixed signature, so signing makes no network call.
type fakeSigner struct {
	mu     sync.Mutex
	signed [][]byte
}

var fakeSignature = []byte{0xde, 0xad, 0xbe, 0xef}

func (f *fakeSigner) sign(b []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signed = append(f.signed, append([]byte(nil), b...))
	return fakeSignature, nil
}

func (f *fakeSigner) last(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.signed) == 0 {
		t.Fatal("the signer was never called")
	}
	return string(f.signed[len(f.signed)-1])
}

func newSigningGCS(t *testing.T) (*blobstore.GCS, *fakeSigner) {
	t.Helper()
	signer := &fakeSigner{}
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{
		Bucket:        "uploads",
		SignerEmail:   signerEmail,
		SignBytes:     signer.sign,
		ClientOptions: []option.ClientOption{option.WithoutAuthentication()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, signer
}

func TestNewGCSRefusesIncompleteConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	noAuth := []option.ClientOption{option.WithoutAuthentication()}
	if _, err := blobstore.NewGCS(ctx, blobstore.GCSConfig{ClientOptions: noAuth}); err == nil {
		t.Error("NewGCS accepted no bucket")
	}
	sign := func([]byte) ([]byte, error) { return nil, nil }
	if _, err := blobstore.NewGCS(ctx, blobstore.GCSConfig{Bucket: "uploads", SignBytes: sign, ClientOptions: noAuth}); err == nil {
		t.Error("NewGCS accepted SignBytes without the account it signs as")
	}
}

// signedQuery parses a V4 signed URL and checks what every one carries: the
// XML API host, path-style, the algorithm, the signer's credential scope,
// the TTL, and the signer's signature.
func signedQuery(t *testing.T, raw, wantPath string, ttl time.Duration) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "storage.googleapis.com" || u.Path != wantPath {
		t.Fatalf("signed URL %s, want https://storage.googleapis.com%s", raw, wantPath)
	}
	q := u.Query()
	cred := strings.Split(q.Get("X-Goog-Credential"), "/")
	if len(cred) != 5 || cred[0] != signerEmail || cred[2] != "auto" || cred[3] != "storage" || cred[4] != "goog4_request" {
		t.Fatalf("X-Goog-Credential = %q, want %s/<date>/auto/storage/goog4_request", q.Get("X-Goog-Credential"), signerEmail)
	}
	if _, err := time.Parse("20060102", cred[1]); err != nil {
		t.Fatalf("credential date %q: %v", cred[1], err)
	}
	if q.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" || q.Get("X-Goog-Expires") != strconv.Itoa(int(ttl.Seconds())) {
		t.Fatalf("query = %v, want GOOG4-RSA-SHA256 valid for %s", q, ttl)
	}
	if _, err := time.Parse("20060102T150405Z", q.Get("X-Goog-Date")); err != nil {
		t.Fatalf("X-Goog-Date %q: %v", q.Get("X-Goog-Date"), err)
	}
	if q.Get("X-Goog-Signature") != hex.EncodeToString(fakeSignature) {
		t.Fatalf("X-Goog-Signature = %q, want the signer's signature in hex", q.Get("X-Goog-Signature"))
	}
	return q
}

// checkStringToSign holds the signer's input to the V4 shape: algorithm,
// timestamp, the signer's scope, and the canonical request's digest.
func checkStringToSign(t *testing.T, s string, q url.Values) {
	t.Helper()
	lines := strings.Split(s, "\n")
	cred := q.Get("X-Goog-Credential")
	scope := cred[strings.Index(cred, "/")+1:]
	if len(lines) != 4 || lines[0] != "GOOG4-RSA-SHA256" || lines[1] != q.Get("X-Goog-Date") || lines[2] != scope || len(lines[3]) != 64 {
		t.Fatalf("string to sign = %q, want GOOG4-RSA-SHA256, the date, %s and a sha256", s, scope)
	}
}

// Signing is local computation under a fake signer: no request reaches
// Cloud Storage or IAM.
func TestGCSPresignPutSignsTypeAndLength(t *testing.T) {
	t.Parallel()
	g, signer := newSigningGCS(t)
	req, err := g.PresignPut(context.Background(), "t1/a b.png", blobstore.PutOptions{ContentType: "image/png", ContentLength: 1234, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPut {
		t.Fatalf("Method = %s, want PUT", req.Method)
	}
	q := signedQuery(t, req.URL, "/uploads/t1/a b.png", time.Minute)
	if got := q.Get("X-Goog-SignedHeaders"); got != "content-length;content-type;host" {
		t.Fatalf("X-Goog-SignedHeaders = %q: the length and type must be signed", got)
	}
	checkStringToSign(t, signer.last(t), q)
	if req.Headers["Content-Type"] != "image/png" || len(req.Headers) != 1 {
		t.Fatalf("Headers = %v, want only Content-Type (a browser sets Content-Length itself)", req.Headers)
	}
	if d := time.Until(req.ExpiresAt); d <= 0 || d > time.Minute {
		t.Fatalf("ExpiresAt in %s, want within the TTL", d)
	}
	ctx := context.Background()
	if _, err := g.PresignPut(ctx, "x", blobstore.PutOptions{ContentType: "image/png", ContentLength: 2, MaxBytes: 1}); !errors.Is(err, blobstore.ErrTooLarge) {
		t.Fatalf("over MaxBytes = %v, want ErrTooLarge", err)
	}
	if _, err := g.PresignPut(ctx, "../x", blobstore.PutOptions{ContentType: "image/png", ContentLength: 1}); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("bad key = %v, want ErrInvalidKey", err)
	}
	if _, err := g.PresignPut(ctx, "x", blobstore.PutOptions{ContentType: "image/png", ContentLength: 1, TTL: blobstore.MaxTTL}); err != nil {
		t.Fatalf("a MaxTTL URL = %v, want it signed (V4 allows 7 days)", err)
	}
}

func TestGCSPresignGetAndDownload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, signer := newSigningGCS(t)

	get, err := g.PresignGet(ctx, "t1/a.png", blobstore.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := signedQuery(t, get.URL, "/uploads/t1/a.png", blobstore.DefaultTTL)
	if q.Get("X-Goog-SignedHeaders") != "host" || q.Has("response-content-type") || q.Has("response-content-disposition") {
		t.Fatalf("PresignGet query = %v, want only host signed and no response overrides", q)
	}
	checkStringToSign(t, signer.last(t), q)

	named, err := g.PresignGet(ctx, "k", blobstore.GetOptions{DownloadName: `résumé "final".pdf`})
	if err != nil {
		t.Fatal(err)
	}
	q = signedQuery(t, named.URL, "/uploads/k", blobstore.DefaultTTL)
	want := `attachment; filename="r_sum_ _final_.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22final%22.pdf`
	if got := q.Get("response-content-disposition"); got != want || q.Has("response-content-type") {
		t.Fatalf("PresignGet with a name: disposition %q type %q, want %q and no type", got, q.Get("response-content-type"), want)
	}

	dl, err := blobstore.PresignDownload(ctx, g, "t1/page.html", blobstore.DownloadOptions{TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	q = checkForcedDownload(t, dl, "page.html")
	signedQuery(t, dl.URL, "/uploads/t1/page.html", 5*time.Minute)
	checkStringToSign(t, signer.last(t), q)
	if _, err := g.PresignDownload(ctx, "x", blobstore.DownloadOptions{TTL: blobstore.MaxTTL + time.Second}); !errors.Is(err, blobstore.ErrInvalidOptions) {
		t.Fatalf("TTL past MaxTTL = %v, want ErrInvalidOptions", err)
	}
}

// fakeGCS answers the JSON API calls the store makes: objects.get,
// objects.delete and objects.list (flat with startOffset, or delimited by
// "/"; fakeLevel). Its page tokens are opaque and, when refuse is set, every
// token is refused.
type fakeGCS struct {
	mu     sync.Mutex
	keys   []string
	refuse bool
}

func (f *fakeGCS) serve(t *testing.T) *httptest.Server {
	t.Helper()
	const base = "/storage/v1/b/uploads/o"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == base && r.Method == http.MethodGet:
			f.list(w, r.URL.Query())
		case strings.HasPrefix(r.URL.Path, base+"/"):
			key := strings.TrimPrefix(r.URL.Path, base+"/")
			i := sort.SearchStrings(f.keys, key)
			if i == len(f.keys) || f.keys[i] != key {
				gcsError(w, http.StatusNotFound, "No such object")
				return
			}
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, gcsResource(key))
			case http.MethodDelete:
				f.keys = append(f.keys[:i], f.keys[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
			default:
				gcsError(w, http.StatusMethodNotAllowed, r.Method)
			}
		default:
			gcsError(w, http.StatusForbidden, r.Method+" "+r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeGCS) list(w http.ResponseWriter, q url.Values) {
	var cursor string
	if tok := q.Get("pageToken"); tok != "" {
		var ok bool
		if cursor, ok = fakeCursor(tok); !ok || f.refuse {
			gcsError(w, http.StatusBadRequest, "Invalid page token")
			return
		}
	}
	maxResults, err := strconv.Atoi(q.Get("maxResults"))
	if err != nil || maxResults <= 0 || maxResults > 1000 {
		maxResults = 1000
	}
	resp := map[string]any{"kind": "storage#objects"}
	var items []map[string]any
	var next string
	switch q.Get("delimiter") {
	case "/":
		var objects, prefixes []string
		objects, prefixes, next = fakeLevel(f.keys, q.Get("prefix"), cursor, maxResults)
		for _, k := range objects {
			items = append(items, gcsResource(k))
		}
		if prefixes != nil {
			resp["prefixes"] = prefixes
		}
	case "":
		for _, k := range f.keys {
			if !strings.HasPrefix(k, q.Get("prefix")) || k < q.Get("startOffset") || k <= cursor {
				continue
			}
			if len(items) == maxResults {
				next = items[len(items)-1]["name"].(string)
				break
			}
			items = append(items, gcsResource(k))
		}
	default:
		gcsError(w, http.StatusBadRequest, "delimiter")
		return
	}
	if items != nil {
		resp["items"] = items
	}
	if next != "" {
		resp["nextPageToken"] = fakeToken(next)
	}
	writeJSON(w, resp)
}

func gcsResource(key string) map[string]any {
	ct := "text/plain"
	if strings.HasSuffix(key, ".html") {
		ct = "text/html"
	}
	return map[string]any{
		"kind": "storage#object", "bucket": "uploads", "name": key,
		"size": strconv.Itoa(len(key)), "contentType": ct, "etag": "etag-" + key,
		"updated": "2026-01-02T03:04:05.000Z",
	}
}

func gcsError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newFakeGCS(t *testing.T, keys []string) (*blobstore.GCS, *fakeGCS) {
	t.Helper()
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	fake := &fakeGCS{keys: sorted}
	srv := fake.serve(t)
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{
		Bucket:        "uploads",
		ClientOptions: []option.ClientOption{option.WithEndpoint(srv.URL + "/storage/v1/"), option.WithoutAuthentication()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, fake
}

func TestGCSStatAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, _ := newFakeGCS(t, []string{"t1/page.html", "t1/b"})
	o, err := g.Stat(ctx, "t1/page.html")
	if err != nil || o.Key != "t1/page.html" || o.Size != 12 || o.ContentType != "text/html" || o.ETag != "etag-t1/page.html" ||
		!o.LastModified.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("Stat = %+v, %v", o, err)
	}
	if _, err := g.Stat(ctx, "t1/missing"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("Stat(missing) = %v, want ErrNotFound", err)
	}
	if err := g.Delete(ctx, "t1/page.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Stat(ctx, "t1/page.html"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("Stat after Delete = %v, want ErrNotFound", err)
	}
	if err := g.Delete(ctx, "t1/page.html"); err != nil {
		t.Fatalf("deleting a missing object = %v, want nil", err)
	}
	if _, err := g.Stat(ctx, "../x"); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("bad key = %v, want ErrInvalidKey", err)
	}
}

func TestGCSListPagesThroughEveryObject(t *testing.T) {
	t.Parallel()
	g, _ := newFakeGCS(t, []string{"t/a", "t/b", "t/c", "t/d", "t/e", "u/x"})
	for limit := 1; limit <= 6; limit++ {
		var got []string
		opts := blobstore.ListOptions{Limit: limit}
		for pages := 0; ; pages++ {
			if pages > 6 {
				t.Fatalf("limit %d: paging does not terminate", limit)
			}
			page, err := g.List(context.Background(), "t/", opts)
			if err != nil {
				t.Fatalf("limit %d: %v", limit, err)
			}
			if len(page.Objects) > limit {
				t.Fatalf("limit %d: page of %d objects", limit, len(page.Objects))
			}
			got = append(got, keys(page)...)
			if page.Next == "" {
				break
			}
			opts.After = page.Next
		}
		if strings.Join(got, ",") != "t/a,t/b,t/c,t/d,t/e" {
			t.Fatalf("limit %d: paged listing = %v", limit, got)
		}
	}
}

func TestGCSListLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, fake := newFakeGCS(t, levelServiceKeys)
	checkLevelListing(t, g)

	page, err := g.ListLevel(ctx, "b/", blobstore.LevelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if o := page.Objects[0]; o.Key != "b/1" || o.Size != 3 || o.ContentType != "text/plain" || o.ETag != "etag-b/1" {
		t.Fatalf("object = %+v, want the listed size, type and etag", o)
	}

	first, err := g.ListLevel(ctx, "", blobstore.LevelOptions{Limit: 1})
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if _, err := g.ListLevel(ctx, "b/", blobstore.LevelOptions{PageToken: first.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("another prefix's token = %v, want ErrInvalidPageToken", err)
	}
	memTok, err := newLevelMemory(t, levelKeys).ListLevel(ctx, "", blobstore.LevelOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ListLevel(ctx, "", blobstore.LevelOptions{PageToken: memTok.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("another backend's token = %v, want ErrInvalidPageToken", err)
	}
	fake.mu.Lock()
	fake.refuse = true
	fake.mu.Unlock()
	if _, err := g.ListLevel(ctx, "", blobstore.LevelOptions{PageToken: first.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("a refused page token = %v, want ErrInvalidPageToken", err)
	}
}
