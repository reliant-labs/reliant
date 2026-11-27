// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an ETag, as S3 computes it; not a security boundary
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

// writeRange answers a GET or HEAD of data, honouring a "bytes=a-b" or
// "bytes=a-" Range header as S3 and GCS's XML API do: 206 with a
// Content-Range naming the complete length, and 416 for a range that starts
// at or past the end, even of an empty object.
func writeRange(w http.ResponseWriter, r *http.Request, data []byte) {
	start, end := int64(0), int64(len(data))-1
	rng := r.Header.Get("Range")
	if rng != "" {
		spec, ok := strings.CutPrefix(rng, "bytes=")
		from, to, _ := strings.Cut(spec, "-")
		var err error
		if start, err = strconv.ParseInt(from, 10, 64); !ok || err != nil || start >= int64(len(data)) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidRange</Code></Error>`)
			return
		}
		if to != "" {
			if e, err := strconv.ParseInt(to, 10, 64); err == nil && e < end {
				end = e
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if rng != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write(data[start : end+1])
	}
}

// fakeS3Objects is an S3 bucket as S3.Open and S3.PutObject reach it over
// TLS (so minio-go sends a plain body, not a streaming signature): single
// PUTs, ranged GETs, HEAD and DELETE. A PUT is committed only when its
// whole body arrived, as the service does.
type fakeS3Objects struct {
	mu      sync.Mutex
	objects map[string]fakeS3Object
	// ignoreRange answers a ranged GET with 200 and the whole object, as
	// a service that does not implement Range would. overlong answers it
	// with 206 but the bytes from the offset to the end, past its end.
	ignoreRange, overlong bool
	// failPuts answers that many PUTs with a 500 after reading their body,
	// as a service failing transiently would; puts counts every PUT.
	failPuts, puts int
}

type fakeS3Object struct {
	data        []byte
	contentType string
}

func (f *fakeS3Objects) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.URL.Path, "/uploads/")
		if !ok || r.URL.RawQuery != "" && r.Method != http.MethodGet {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		o, found := f.objects[key]
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil || int64(len(data)) != r.ContentLength {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.puts++
			if f.failPuts > 0 {
				f.failPuts--
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code></Error>`)
				return
			}
			f.objects[key] = fakeS3Object{data: data, contentType: r.Header.Get("Content-Type")}
			w.Header().Set("ETag", etag(data))
		case http.MethodGet, http.MethodHead:
			if !found {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code></Error>`)
				}
				return
			}
			w.Header().Set("ETag", etag(o.data))
			w.Header().Set("Last-Modified", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(http.TimeFormat))
			w.Header().Set("Content-Type", o.contentType)
			switch {
			case f.ignoreRange:
				r.Header.Del("Range")
			case f.overlong:
				if from, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
					from, _, _ = strings.Cut(from, "-")
					r.Header.Set("Range", "bytes="+from+"-")
				}
			}
			writeRange(w, r, o.data)
		case http.MethodDelete:
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // see the import
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// The S3 backend's Open and PutObject over a fake bucket; the same contract
// holds against a real MinIO in the integration lane, which also covers
// multipart uploads.
func TestS3ObjectIO(t *testing.T) {
	t.Parallel()
	s, _ := newObjectS3(t)
	checkObjectIO(t, s, "io")
}

func newObjectS3(t *testing.T) (*blobstore.S3, *fakeS3Objects) {
	t.Helper()
	fake := &fakeS3Objects{objects: map[string]fakeS3Object{}}
	srv := fake.serve(t)
	cfg := s3Config(srv.URL)
	cfg.Transport = srv.Client().Transport
	s, err := blobstore.NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

// A buffered body of unknown size spanning several chunks is still one
// seekable body, so minio-go retries its PUT after a transient failure and
// the retry stores every byte.
func TestS3PutObjectRetriesABufferedBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, fake := newObjectS3(t)
	body := bytes.Repeat([]byte("0123456789abcdef"), 300<<10/16) // 300 KiB: three chunks
	fake.mu.Lock()
	fake.failPuts = 1
	fake.mu.Unlock()
	o, err := s.PutObject(ctx, "t/retried", bytes.NewBuffer(body), blobstore.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil || o.Size != int64(len(body)) || o.SHA256 != sha256Hex(body) {
		t.Fatalf("PutObject after a transient 500 = %+v, %v", o, err)
	}
	got, _, err := readOpen(t, s, "t/retried", blobstore.OpenOptions{})
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored %d bytes, %v; want the whole %d-byte body", len(got), err, len(body))
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.puts != 2 {
		t.Fatalf("%d PUTs, want 2 (one failed, one retried)", fake.puts)
	}
}

// A ranged Open trusts only a ranged answer: a service that ignores Range
// (200, the whole object) is an error, not the wrong bytes, and one that
// sends more than the range asked is cut at the range's length.
func TestS3OpenHoldsTheServiceToTheRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, fake := newObjectS3(t)
	if _, err := s.PutObject(ctx, "t/a", strings.NewReader("hello, world"), blobstore.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.ignoreRange = true
	fake.mu.Unlock()
	for _, opts := range []blobstore.OpenOptions{{Offset: 7}, {Length: 5}} {
		if r, o, err := s.Open(ctx, "t/a", opts); err == nil {
			r.Close()
			t.Fatalf("Open %+v of a service ignoring Range = %+v, want an error", opts, o)
		}
	}
	if got, _, err := readOpen(t, s, "t/a", blobstore.OpenOptions{}); err != nil || string(got) != "hello, world" {
		t.Fatalf("an unranged Open = %q, %v", got, err)
	}
	fake.mu.Lock()
	fake.ignoreRange, fake.overlong = false, true
	fake.mu.Unlock()
	if got, o, err := readOpen(t, s, "t/a", blobstore.OpenOptions{Offset: 7, Length: 3}); err != nil || string(got) != "wor" || o.Size != 12 {
		t.Fatalf("Open of an overlong range = %q %+v %v, want \"wor\"", got, o, err)
	}
}
