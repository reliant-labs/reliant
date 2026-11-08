// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"crypto/md5" //nolint:gosec // an ETag, as S3 computes it for a single-part upload; not a security boundary
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Memory is an in-process Store for unit tests. It validates keys and
// options exactly as the S3 backend does, and its presigned URLs are
// "memory://" URLs nothing can fetch: a test plays the browser by calling Put
// with the bytes the client would have sent.
type Memory struct {
	mu      sync.Mutex
	objects map[string]memObject
	now     func() time.Time
}

type memObject struct {
	data        []byte
	contentType string
	modified    time.Time
}

// NewMemory returns an empty Memory store.
func NewMemory() *Memory {
	return &Memory{objects: map[string]memObject{}, now: time.Now}
}

var (
	_ Store             = (*Memory)(nil)
	_ LevelLister       = (*Memory)(nil)
	_ DownloadPresigner = (*Memory)(nil)
)

// Put stores data at key, as a completed upload would. It is the test's
// stand-in for the browser's PUT.
func (m *Memory) Put(key string, data []byte, contentType string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{data: append([]byte(nil), data...), contentType: contentType, modified: m.now()}
	return nil
}

// Get returns a copy of the bytes stored at key.
func (m *Memory) Get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), o.data...), true
}

func (m *Memory) presigned(method, key string, d time.Duration, q url.Values) PresignedRequest {
	exp := m.now().Add(d)
	q.Set("expires", strconv.FormatInt(exp.Unix(), 10))
	u := url.URL{Scheme: "memory", Host: "blobstore", Path: "/" + key, RawQuery: q.Encode()}
	return PresignedRequest{Method: method, URL: u.String(), ExpiresAt: exp}
}

// PresignPut implements Store.
func (m *Memory) PresignPut(_ context.Context, key string, opts PutOptions) (PresignedRequest, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedRequest{}, err
	}
	d, err := validatePut(opts)
	if err != nil {
		return PresignedRequest{}, err
	}
	q := url.Values{"content-length": {strconv.FormatInt(opts.ContentLength, 10)}}
	req := m.presigned("PUT", key, d, q)
	req.Headers = map[string]string{"Content-Type": opts.ContentType}
	return req, nil
}

// PresignGet implements Store.
func (m *Memory) PresignGet(_ context.Context, key string, opts GetOptions) (PresignedRequest, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedRequest{}, err
	}
	d, err := ttl(opts.TTL)
	if err != nil {
		return PresignedRequest{}, err
	}
	q := url.Values{}
	if opts.DownloadName != "" {
		q.Set("response-content-disposition", contentDisposition(opts.DownloadName))
	}
	return m.presigned("GET", key, d, q), nil
}

// Stat implements Store.
func (m *Memory) Stat(_ context.Context, key string) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return Object{}, errorf(ErrNotFound, "%q", key)
	}
	return o.info(key), nil
}

// Delete implements Store.
func (m *Memory) Delete(_ context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// List implements Store.
func (m *Memory) List(_ context.Context, prefix string, opts ListOptions) (ListPage, error) {
	if err := validatePrefix(prefix); err != nil {
		return ListPage{}, err
	}
	limit := listLimit(opts.Limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) && k > opts.After {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var page ListPage
	for i, k := range keys {
		if i == limit {
			page.Next = page.Objects[len(page.Objects)-1].Key
			break
		}
		page.Objects = append(page.Objects, m.objects[k].info(k))
	}
	return page, nil
}

// ListLevel implements LevelLister. Its page token's cursor is the last
// entry (key or common prefix) the previous page returned.
func (m *Memory) ListLevel(_ context.Context, prefix string, opts LevelOptions) (LevelPage, error) {
	if err := validatePrefix(prefix); err != nil {
		return LevelPage{}, err
	}
	cursor, err := decodePageToken("memory", "", prefix, opts.PageToken)
	if err != nil {
		return LevelPage{}, err
	}
	limit := listLimit(opts.Limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	// Over sorted keys the entries (a key, or its common prefix) never
	// decrease, and the keys under one common prefix are contiguous, so a
	// page resumes at the first entry past the cursor.
	var page LevelPage
	last, n := "", 0
	for _, k := range keys {
		entry, isPrefix := k, false
		if i := strings.IndexByte(k[len(prefix):], '/'); i >= 0 {
			entry, isPrefix = k[:len(prefix)+i+1], true
		}
		if entry <= cursor || entry == last {
			continue
		}
		if n == limit {
			page.Next = encodePageToken("memory", "", prefix, last)
			break
		}
		n, last = n+1, entry
		if isPrefix {
			page.Prefixes = append(page.Prefixes, entry)
		} else {
			page.Objects = append(page.Objects, m.objects[k].info(k))
		}
	}
	return page, nil
}

// PresignDownload implements DownloadPresigner.
func (m *Memory) PresignDownload(_ context.Context, key string, opts DownloadOptions) (PresignedRequest, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedRequest{}, err
	}
	d, q, err := downloadParams(key, opts)
	if err != nil {
		return PresignedRequest{}, err
	}
	return m.presigned("GET", key, d, q), nil
}

func (o memObject) info(key string) Object {
	sum := md5.Sum(o.data) //nolint:gosec // see the import
	return Object{
		Key:          key,
		Size:         int64(len(o.data)),
		ContentType:  o.contentType,
		ETag:         hex.EncodeToString(sum[:]),
		LastModified: o.modified,
	}
}
