// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// levelKeys is the tree every ListLevel test lists. Its names put the
// ordering traps side by side: "b.txt" ('.' sorts before '/'), the prefix
// "b/", then "b0" ('0' sorts after '/').
var levelKeys = []string{
	"a.txt", "b.txt", "b/1", "b/2", "b/c/3", "b/c/d/4", "b0", "c/d/e/f", "z",
}

// levelWant is what ListLevel must return for each prefix of levelKeys, as
// one lexical sequence of entries (objects and prefixes merged).
var levelWant = map[string][]string{
	"":        {"a.txt", "b.txt", "b/", "b0", "c/", "z"},
	"b/":      {"b/1", "b/2", "b/c/"},
	"b/c/":    {"b/c/3", "b/c/d/"},
	"b":       {"b.txt", "b/", "b0"},
	"c/":      {"c/d/"},
	"c/d/e/":  {"c/d/e/f"},
	"nothing": nil,
}

func newLevelMemory(t *testing.T, keys []string) *blobstore.Memory {
	t.Helper()
	m := blobstore.NewMemory()
	for _, k := range keys {
		if err := m.Put(k, []byte(k), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// entries merges a page's objects and prefixes into one lexical sequence,
// failing if either list is out of order.
func entries(t *testing.T, page blobstore.LevelPage) []string {
	t.Helper()
	var out []string
	for i, o := range page.Objects {
		if i > 0 && page.Objects[i-1].Key >= o.Key {
			t.Fatalf("objects out of order: %v", keysOf(page))
		}
		out = append(out, o.Key)
	}
	for i, p := range page.Prefixes {
		if i > 0 && page.Prefixes[i-1] >= p {
			t.Fatalf("prefixes out of order: %v", page.Prefixes)
		}
		if !strings.HasSuffix(p, "/") {
			t.Fatalf("prefix %q does not end in /", p)
		}
		out = append(out, p)
	}
	sortStrings(out)
	return out
}

func keysOf(page blobstore.LevelPage) []string {
	out := make([]string, 0, len(page.Objects))
	for _, o := range page.Objects {
		out = append(out, o.Key)
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// walkLevel pages through ListLevel at limit and returns every entry in
// order, failing on an oversized page, a repeat, or paging that does not end.
func walkLevel(t *testing.T, store blobstore.Store, prefix string, limit int) []string {
	t.Helper()
	ctx := context.Background()
	var all []string
	opts := blobstore.LevelOptions{Limit: limit}
	for pages := 0; ; pages++ {
		if pages > 2*len(levelKeys)+2 {
			t.Fatalf("prefix %q limit %d: paging does not terminate", prefix, limit)
		}
		page, err := blobstore.ListLevel(ctx, store, prefix, opts)
		if err != nil {
			t.Fatalf("prefix %q limit %d: %v", prefix, limit, err)
		}
		got := entries(t, page)
		if len(got) > limit {
			t.Fatalf("prefix %q limit %d: page of %d entries", prefix, limit, len(got))
		}
		for _, e := range got {
			if len(all) > 0 && e <= all[len(all)-1] {
				t.Fatalf("prefix %q limit %d: %q after %q: a repeat or out of order", prefix, limit, e, all[len(all)-1])
			}
			all = append(all, e)
		}
		if page.Next == "" {
			return all
		}
		opts.PageToken = page.Next
	}
}

// checkLevelListing holds a store to levelWant at every page size.
func checkLevelListing(t *testing.T, store blobstore.Store) {
	t.Helper()
	for prefix, want := range levelWant {
		for limit := 1; limit <= len(want)+1; limit++ {
			if got := walkLevel(t, store, prefix, limit); !reflect.DeepEqual(got, want) {
				t.Errorf("prefix %q limit %d: %v, want %v", prefix, limit, got, want)
			}
		}
		// Limit 0 is MaxListLimit: one page.
		page, err := blobstore.ListLevel(context.Background(), store, prefix, blobstore.LevelOptions{})
		if err != nil || page.Next != "" || !reflect.DeepEqual(entries(t, page), want) {
			t.Errorf("prefix %q, no limit: %v next %q err %v, want %v", prefix, entries(t, page), page.Next, err, want)
		}
	}
}

func TestMemoryListLevel(t *testing.T) {
	t.Parallel()
	m := newLevelMemory(t, levelKeys)
	checkLevelListing(t, m)

	page, err := m.ListLevel(context.Background(), "b/", blobstore.LevelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 2 || page.Objects[0].Size != 3 || page.Objects[0].ContentType != "text/plain" || page.Objects[0].ETag == "" || page.Objects[0].LastModified.IsZero() {
		t.Fatalf("objects = %+v, want key, size, type, etag and time", page.Objects)
	}
	if _, err := m.ListLevel(context.Background(), "../b/", blobstore.LevelOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("bad prefix = %v, want ErrInvalidKey", err)
	}
}

// A page token is refused unless this store issued it for this listing.
func TestMemoryListLevelRefusesTamperedTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newLevelMemory(t, levelKeys)
	first, err := m.ListLevel(ctx, "", blobstore.LevelOptions{Limit: 2})
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.Next)
	if err != nil {
		t.Fatalf("token %q is not base64url: %v", first.Next, err)
	}
	mutate := func(f func([]byte) []byte) string {
		return base64.RawURLEncoding.EncodeToString(f(append([]byte(nil), raw...)))
	}
	bad := map[string]struct {
		prefix, token string
	}{
		"garbage":            {"", "not a token!"},
		"padded base64":      {"", base64.URLEncoding.EncodeToString(raw) + "="},
		"another prefix":     {"b/", first.Next},
		"a flipped version":  {"", mutate(func(b []byte) []byte { b[0] ^= 0xff; return b })},
		"a flipped binding":  {"", mutate(func(b []byte) []byte { b[3] ^= 0x01; return b })},
		"no cursor":          {"", mutate(func(b []byte) []byte { return b[:9] })},
		"truncated":          {"", mutate(func(b []byte) []byte { return b[:4] })},
		"empty after decode": {"", "AA"},
	}
	for name, tc := range bad {
		_, err := m.ListLevel(ctx, tc.prefix, blobstore.LevelOptions{PageToken: tc.token})
		if !errors.Is(err, blobstore.ErrInvalidPageToken) || !svcerr.IsInvalidArgument(err) {
			t.Errorf("%s: ListLevel = %v, want ErrInvalidPageToken (InvalidArgument)", name, err)
		}
	}
	// The real token, with its own prefix, continues the listing.
	next, err := m.ListLevel(ctx, "", blobstore.LevelOptions{Limit: 2, PageToken: first.Next})
	if err != nil || !reflect.DeepEqual(entries(t, next), []string{"b/", "b0"}) {
		t.Fatalf("second page = %v, %v", entries(t, next), err)
	}
}

func TestScopedListLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var keys []string
	for _, k := range levelKeys {
		keys = append(keys, "tenant-a/"+k, "tenant-b/"+k)
	}
	inner := newLevelMemory(t, keys)
	a, err := blobstore.Scoped(inner, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	checkLevelListing(t, a)

	// tenant-a's token names tenant-a's listing: tenant-b's scope refuses it.
	b, err := blobstore.Scoped(inner, "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	page, err := blobstore.ListLevel(ctx, a, "", blobstore.LevelOptions{Limit: 1})
	if err != nil || page.Next == "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if _, err := blobstore.ListLevel(ctx, b, "", blobstore.LevelOptions{PageToken: page.Next}); !errors.Is(err, blobstore.ErrInvalidPageToken) {
		t.Fatalf("another scope's token = %v, want ErrInvalidPageToken", err)
	}
	if _, err := blobstore.ListLevel(ctx, a, "../tenant-b/", blobstore.LevelOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("an escaping prefix = %v, want ErrInvalidKey", err)
	}
}

// levelLeak is a LevelLister that reports whatever page it was given.
type levelLeak struct {
	blobstore.Store
	page blobstore.LevelPage
}

func (l levelLeak) ListLevel(context.Context, string, blobstore.LevelOptions) (blobstore.LevelPage, error) {
	return l.page, nil
}

func TestScopedListLevelRefusesOutOfScopeResults(t *testing.T) {
	t.Parallel()
	for name, page := range map[string]blobstore.LevelPage{
		"an object of another tenant": {Objects: []blobstore.Object{{Key: "tenant-b/secret"}}},
		"a prefix of another tenant":  {Prefixes: []string{"tenant-a/ok/", "tenant-b/"}},
		"the bare scope prefix":       {Prefixes: []string{"tenant-a/"}},
	} {
		s, err := blobstore.Scoped(levelLeak{page: page}, "tenant-a")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := blobstore.ListLevel(context.Background(), s, "", blobstore.LevelOptions{}); err == nil {
			t.Errorf("%s: ListLevel = %+v, want an error", name, got)
		}
	}
}

// A Store that offers neither is refused, never emulated with List or
// PresignGet; a Scoped store over one says the same.
func TestUnsupportedStoresAreRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	plain := leakyStore{}
	scoped, err := blobstore.Scoped(plain, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]blobstore.Store{"plain": plain, "scoped": scoped} {
		if _, err := blobstore.ListLevel(ctx, s, "", blobstore.LevelOptions{}); !errors.Is(err, blobstore.ErrUnsupported) || !svcerr.IsUnimplemented(err) {
			t.Errorf("%s: ListLevel = %v, want ErrUnsupported", name, err)
		}
		if _, err := blobstore.PresignDownload(ctx, s, "k", blobstore.DownloadOptions{}); !errors.Is(err, blobstore.ErrUnsupported) {
			t.Errorf("%s: PresignDownload = %v, want ErrUnsupported", name, err)
		}
	}
}

// checkForcedDownload holds a PresignDownload URL to the forced type and an
// attachment disposition named name.
func checkForcedDownload(t *testing.T, req blobstore.PresignedRequest, name string) url.Values {
	t.Helper()
	u, err := url.Parse(req.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if req.Method != "GET" || q.Get("response-content-type") != "application/octet-stream" {
		t.Fatalf("PresignDownload = %s %s, want a GET forcing application/octet-stream", req.Method, req.URL)
	}
	if got := q.Get("response-content-disposition"); !strings.HasPrefix(got, `attachment; filename="`+name+`"`) {
		t.Fatalf("response-content-disposition = %q, want an attachment named %q", got, name)
	}
	return q
}

func TestMemoryPresignDownload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := blobstore.NewMemory()
	req, err := blobstore.PresignDownload(ctx, m, "t1/page.html", blobstore.DownloadOptions{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	checkForcedDownload(t, req, "page.html")
	if d := time.Until(req.ExpiresAt); d <= 0 || d > time.Minute {
		t.Fatalf("ExpiresAt in %s, want within the TTL", d)
	}
	req, err = blobstore.PresignDownload(ctx, m, "t1/page.html", blobstore.DownloadOptions{DownloadName: "saved.bin"})
	if err != nil {
		t.Fatal(err)
	}
	checkForcedDownload(t, req, "saved.bin")
	if _, err := m.PresignDownload(ctx, "../x", blobstore.DownloadOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("bad key = %v, want ErrInvalidKey", err)
	}
	if _, err := m.PresignDownload(ctx, "x", blobstore.DownloadOptions{TTL: blobstore.MaxTTL + time.Second}); !errors.Is(err, blobstore.ErrInvalidOptions) {
		t.Fatalf("TTL past MaxTTL = %v, want ErrInvalidOptions", err)
	}
	// PresignGet is unchanged: the stored type, and inline unless named.
	get, err := m.PresignGet(ctx, "t1/page.html", blobstore.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(get.URL); u.Query().Has("response-content-type") || u.Query().Has("response-content-disposition") {
		t.Fatalf("PresignGet = %s, want no response overrides", get.URL)
	}
}
