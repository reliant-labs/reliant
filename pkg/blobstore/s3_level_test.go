// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

// fakeLevelS3 answers delimited ListObjectsV2 over keys (fakeLevel). It
// refuses a request that is not delimited by "/", that carries start-after,
// or (when refuse is set) that carries any continuation token.
func fakeLevelS3(t *testing.T, keys []string, refuse *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/uploads/" && r.URL.Path != "/uploads" || q.Get("list-type") != "2" || q.Get("delimiter") != "/" || q.Get("start-after") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var cursor string
		if tok := q.Get("continuation-token"); tok != "" {
			var ok bool
			if cursor, ok = fakeCursor(tok); !ok || refuse.Load() {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidArgument</Code><Message>The continuation token provided is incorrect</Message></Error>`)
				return
			}
		}
		maxKeys, err := strconv.Atoi(q.Get("max-keys"))
		if err != nil || maxKeys <= 0 || maxKeys > 1000 {
			maxKeys = 1000
		}
		objects, prefixes, next := fakeLevel(keys, q.Get("prefix"), cursor, maxKeys)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>uploads</Name>`)
		fmt.Fprintf(&b, "<Prefix>%s</Prefix><Delimiter>/</Delimiter><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys><IsTruncated>%t</IsTruncated>",
			q.Get("prefix"), len(objects)+len(prefixes), maxKeys, next != "")
		if next != "" {
			fmt.Fprintf(&b, "<NextContinuationToken>%s</NextContinuationToken>", fakeToken(next))
		}
		for _, k := range objects {
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size><ETag>"etag-%s"</ETag><LastModified>2026-01-02T03:04:05.000Z</LastModified></Contents>`, k, len(k), k)
		}
		for _, p := range prefixes {
			fmt.Fprintf(&b, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", p)
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, b.String())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestS3ListLevel(t *testing.T) {
	t.Parallel()
	var refuse atomic.Bool
	s, err := blobstore.NewS3(s3Config(fakeLevelS3(t, levelServiceKeys, &refuse).URL))
	if err != nil {
		t.Fatal(err)
	}
	checkLevelListing(t, s)

	ctx := context.Background()
	page, err := s.ListLevel(ctx, "b/", blobstore.LevelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	o := page.Objects[0]
	if o.Key != "b/1" || o.Size != 3 || o.ETag != "etag-b/1" || !o.LastModified.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("object = %+v, want its listed size, unquoted etag and time", o)
	}

	first, err := s.ListLevel(ctx, "", blobstore.LevelOptions{Limit: 1})
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	// Bound to its bucket, prefix and backend before any request is made.
	if _, err := s.ListLevel(ctx, "b/", blobstore.LevelOptions{PageToken: first.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("another prefix's token = %v, want ErrInvalidPageToken", err)
	}
	memTok, err := newLevelMemory(t, levelKeys).ListLevel(ctx, "", blobstore.LevelOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListLevel(ctx, "", blobstore.LevelOptions{PageToken: memTok.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("a Memory token = %v, want ErrInvalidPageToken", err)
	}
	// A token the service itself refuses is the caller's error too.
	refuse.Store(true)
	if _, err := s.ListLevel(ctx, "", blobstore.LevelOptions{PageToken: first.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("a refused continuation token = %v, want ErrInvalidPageToken", err)
	}
}

// A listing whose caller gives up cancels the request itself, though
// minio's single-request listing takes no context: the server sees its
// request cancelled, and nothing is retried after the deadline. The
// handler is never released by the test; only the cancellation ends it.
func TestS3ListLevelHonoursCancellation(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	cancelled := make(chan struct{}, 1)
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	s, err := blobstore.NewS3(s3Config(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.ListLevel(ctx, "", blobstore.LevelOptions{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("listing never reached the server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled listing = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("underlying listing did not exit after cancellation")
	}
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("the server's request was never cancelled: the listing outlived its caller")
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("server saw %d requests, want 1: nothing is retried past the deadline", n)
	}
}

// A transient server error is retried, on the caller's context.
func TestS3ListLevelRetriesAServerError(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>uploads</Name><KeyCount>1</KeyCount><IsTruncated>false</IsTruncated><Contents><Key>a.txt</Key><Size>1</Size></Contents></ListBucketResult>`)
	}))
	t.Cleanup(srv.Close)
	s, err := blobstore.NewS3(s3Config(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListLevel(context.Background(), "", blobstore.LevelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != "a.txt" || requests.Load() != 2 {
		t.Fatalf("ListLevel = %+v after %d requests, want a.txt after 2", page, requests.Load())
	}
}

// Presigning is local computation: no request reaches the endpoint.
func TestS3PresignDownloadForcesAnAttachment(t *testing.T) {
	t.Parallel()
	s, err := blobstore.NewS3(s3Config("https://s3.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := blobstore.PresignDownload(context.Background(), s, "t1/evil.svg", blobstore.DownloadOptions{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	q := checkForcedDownload(t, req, "evil.svg")
	if q.Get("X-Amz-Expires") != "60" || q.Get("X-Amz-Signature") == "" {
		t.Fatalf("query = %v, want a signed URL valid for 60s", q)
	}
	// PresignGet keeps the stored type: no forced response-content-type.
	get, err := s.PresignGet(context.Background(), "t1/evil.svg", blobstore.GetOptions{DownloadName: "evil.svg"})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(get.URL); u.Query().Has("response-content-type") {
		t.Fatalf("PresignGet = %s, want no response-content-type", get.URL)
	}
}
