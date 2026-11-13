// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestValidateKey(t *testing.T) {
	t.Parallel()
	ok := []string{
		"a",
		"tenant-1/uploads/0193.png",
		"with space/naïve résumé.pdf",
		"dots.in.name/..hidden/x..y",
		strings.Repeat("k", blobstore.MaxKeyBytes),
	}
	for _, k := range ok {
		if err := blobstore.ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", k, err)
		}
	}
	bad := []string{
		"",
		"/leading",
		"trailing/",
		"double//slash",
		"../escape",
		"a/../b",
		"a/./b",
		".",
		"back\\slash",
		"nul\x00byte",
		"new\nline",
		"\xff\xfe",
		strings.Repeat("k", blobstore.MaxKeyBytes+1),
	}
	for _, k := range bad {
		err := blobstore.ValidateKey(k)
		if !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Errorf("ValidateKey(%q) = %v, want ErrInvalidKey", k, err)
		}
		if !svcerr.IsInvalidArgument(err) {
			t.Errorf("ValidateKey(%q) is not classified InvalidArgument: %v", k, err)
		}
	}
}

func TestScopedKeepsKeysUnderPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem := blobstore.NewMemory()
	a, err := blobstore.Scoped(mem, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := blobstore.Scoped(mem, "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"one.txt", "dir/two.txt"} {
		if err := mem.Put("tenant-a/"+k, []byte(k), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.Put("tenant-b/secret.txt", []byte("b"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Put("tenant-ab/neighbour.txt", []byte("ab"), "text/plain"); err != nil {
		t.Fatal(err)
	}

	page, err := a.List(ctx, "", blobstore.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(page); strings.Join(got, ",") != "dir/two.txt,one.txt" {
		t.Fatalf("tenant-a lists %v, want its own two keys with the prefix removed", got)
	}
	o, err := a.Stat(ctx, "dir/two.txt")
	if err != nil || o.Key != "dir/two.txt" || o.Size != int64(len("dir/two.txt")) {
		t.Fatalf("Stat = %+v, %v", o, err)
	}
	if _, err := b.Stat(ctx, "one.txt"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("tenant-b sees tenant-a's object: %v", err)
	}
	for _, escape := range []string{"../tenant-b/secret.txt", "/tenant-b/secret.txt", "x/../../tenant-b/secret.txt"} {
		if _, err := a.PresignGet(ctx, escape, blobstore.GetOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Errorf("PresignGet(%q) = %v, want ErrInvalidKey", escape, err)
		}
		if err := a.Delete(ctx, escape); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Errorf("Delete(%q) = %v, want ErrInvalidKey", escape, err)
		}
	}
	req, err := a.PresignPut(ctx, "new.png", blobstore.PutOptions{ContentType: "image/png", ContentLength: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL, "/tenant-a/new.png") {
		t.Fatalf("scoped PresignPut URL %q is not under the prefix", req.URL)
	}
	if _, err := blobstore.Scoped(mem, "a/../b"); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("Scoped accepted a prefix with a .. segment: %v", err)
	}
}

func TestScopedListPaging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem := blobstore.NewMemory()
	s, err := blobstore.Scoped(mem, "t")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := mem.Put("t/"+k, nil, "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	var all []string
	var opts blobstore.ListOptions
	opts.Limit = 2
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging does not terminate")
		}
		page, err := s.List(ctx, "", opts)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, keys(page)...)
		if page.Next == "" {
			break
		}
		opts.After = page.Next
	}
	if strings.Join(all, ",") != "a,b,c,d,e" {
		t.Fatalf("paged listing = %v", all)
	}
}

func TestMemoryOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem := blobstore.NewMemory()
	cases := []struct {
		name string
		opts blobstore.PutOptions
		want error
	}{
		{"no content type", blobstore.PutOptions{ContentLength: 1}, blobstore.ErrInvalidOptions},
		{"bad content type", blobstore.PutOptions{ContentType: "image/", ContentLength: 1}, blobstore.ErrInvalidOptions},
		{"no length", blobstore.PutOptions{ContentType: "image/png"}, blobstore.ErrInvalidOptions},
		{"over max", blobstore.PutOptions{ContentType: "image/png", ContentLength: 11, MaxBytes: 10}, blobstore.ErrTooLarge},
		{"ttl too long", blobstore.PutOptions{ContentType: "image/png", ContentLength: 1, TTL: blobstore.MaxTTL + time.Second}, blobstore.ErrInvalidOptions},
		{"ttl too short", blobstore.PutOptions{ContentType: "image/png", ContentLength: 1, TTL: time.Millisecond}, blobstore.ErrInvalidOptions},
	}
	for _, c := range cases {
		if _, err := mem.PresignPut(ctx, "k", c.opts); !errors.Is(err, c.want) {
			t.Errorf("%s: PresignPut = %v, want %v", c.name, err, c.want)
		}
	}
	req, err := mem.PresignPut(ctx, "k", blobstore.PutOptions{ContentType: "image/png", ContentLength: 10, MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "PUT" || req.Headers["Content-Type"] != "image/png" {
		t.Fatalf("PresignPut = %+v", req)
	}
	if d := time.Until(req.ExpiresAt); d <= 0 || d > blobstore.DefaultTTL {
		t.Fatalf("ExpiresAt %s from now, want within DefaultTTL", d)
	}
	if _, err := mem.Stat(ctx, "k"); !errors.Is(err, blobstore.ErrNotFound) || !svcerr.IsNotFound(err) {
		t.Fatalf("Stat before upload = %v, want ErrNotFound classified NotFound", err)
	}
	if err := mem.Put("k", []byte("0123456789"), "image/png"); err != nil {
		t.Fatal(err)
	}
	o, err := mem.Stat(ctx, "k")
	if err != nil || o.Size != 10 || o.ContentType != "image/png" || o.ETag == "" {
		t.Fatalf("Stat = %+v, %v", o, err)
	}
	if err := mem.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Delete(ctx, "k"); err != nil {
		t.Fatalf("deleting a missing object = %v, want nil", err)
	}
}

// leakyStore is a Store that ignores the prefix it is asked to list and
// reports whatever page it was given.
type leakyStore struct {
	blobstore.Store
	page blobstore.ListPage
	stat blobstore.Object
}

func (l leakyStore) List(context.Context, string, blobstore.ListOptions) (blobstore.ListPage, error) {
	return l.page, nil
}

func (l leakyStore) Stat(context.Context, string) (blobstore.Object, error) {
	return l.stat, nil
}

// Scoped does not trust the store it wraps: a key or cursor outside the
// scope fails the call instead of reaching the caller.
func TestScopedRefusesOutOfScopeResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]leakyStore{
		"an object of another tenant": {page: blobstore.ListPage{Objects: []blobstore.Object{{Key: "tenant-a/mine"}, {Key: "tenant-b/secret"}}}},
		"a neighbouring prefix":       {page: blobstore.ListPage{Objects: []blobstore.Object{{Key: "tenant-ab/secret"}}}},
		"the bare prefix":             {page: blobstore.ListPage{Objects: []blobstore.Object{{Key: "tenant-a/"}}}},
		"a cursor of another tenant":  {page: blobstore.ListPage{Objects: []blobstore.Object{{Key: "tenant-a/mine"}}, Next: "tenant-b/secret"}},
	}
	for name, inner := range cases {
		s, err := blobstore.Scoped(inner, "tenant-a")
		if err != nil {
			t.Fatal(err)
		}
		if page, err := s.List(ctx, "", blobstore.ListOptions{}); err == nil {
			t.Errorf("%s: List = %+v, want an error", name, page)
		}
	}

	s, err := blobstore.Scoped(leakyStore{stat: blobstore.Object{Key: "tenant-b/secret"}}, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if o, err := s.Stat(ctx, "mine"); err == nil {
		t.Errorf("Stat = %+v, want an error", o)
	}

	s, err = blobstore.Scoped(leakyStore{page: blobstore.ListPage{Objects: []blobstore.Object{{Key: "tenant-a/mine"}}, Next: "tenant-a/mine"}}, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, "", blobstore.ListOptions{})
	if err != nil || strings.Join(keys(page), ",") != "mine" || page.Next != "mine" {
		t.Fatalf("in-scope List = %+v, %v", page, err)
	}
}

func keys(p blobstore.ListPage) []string {
	out := make([]string, 0, len(p.Objects))
	for _, o := range p.Objects {
		out = append(out, o.Key)
	}
	return out
}
