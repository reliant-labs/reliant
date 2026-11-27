// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var (
	// wrappedEOF is a Read error that wraps io.EOF instead of being it.
	wrappedEOF = fmt.Errorf("copying the source: %w", io.EOF)
	// errSource, wrapped as errWrappedSource, is a source's own failure.
	errSource        = errors.New("source failed")
	errWrappedSource = fmt.Errorf("copying the source: %w", errSource)
)

// failAfter yields data, then fails with err.
func failAfter(data []byte, err error) io.Reader {
	return io.MultiReader(bytes.NewReader(data), iotest.ErrReader(err))
}

// readOpen opens key in store with opts and reads the stream whole.
func readOpen(t *testing.T, store blobstore.Store, key string, opts blobstore.OpenOptions) ([]byte, blobstore.Object, error) {
	t.Helper()
	r, o, err := blobstore.Open(context.Background(), store, key, opts)
	if err != nil {
		return nil, o, err
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading %s %+v: %v", key, opts, err)
	}
	return got, o, nil
}

// checkObjectIO holds a store's PutObject and Open to the ObjectReader and
// ObjectWriter contract, under prefix (which it alone writes): what a put
// reports and stores, the bounds a body is held to while it streams, that a
// refused body stores nothing and leaves an existing object alone, and the
// ranges Open serves.
func checkObjectIO(t *testing.T, store blobstore.Store, prefix string) {
	t.Helper()
	ctx := context.Background()
	key := func(name string) string { return prefix + "/" + name }
	absent := func(t *testing.T, k string) {
		t.Helper()
		if o, err := store.Stat(ctx, k); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("a refused put left %s: %+v, %v", k, o, err)
		}
	}
	body := []byte("hello, object world")

	t.Run("put reports and stores the body", func(t *testing.T) {
		for name, opts := range map[string]blobstore.PutObjectOptions{
			"known":   {ContentType: "text/plain", ContentLength: int64(len(body))},
			"unknown": {ContentType: "text/plain"},
			"at max":  {ContentType: "text/plain", MaxBytes: int64(len(body))},
		} {
			k := key("put-" + strings.ReplaceAll(name, " ", "-"))
			o, err := blobstore.PutObject(ctx, store, k, iotest.OneByteReader(bytes.NewReader(body)), opts)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if o.Key != k || o.Size != int64(len(body)) || o.ContentType != "text/plain" || o.ETag == "" || o.SHA256 != sha256Hex(body) {
				t.Fatalf("%s: PutObject = %+v", name, o)
			}
			st, err := store.Stat(ctx, k)
			if err != nil || st.Size != int64(len(body)) || st.ContentType != "text/plain" || st.SHA256 != "" {
				t.Fatalf("%s: Stat = %+v, %v (Stat never reports a SHA256)", name, st, err)
			}
			// A bare io.EOF that arrives with the last bytes ends the
			// body too: those bytes are stored.
			k2 := k + "-eof-with-data"
			if o, err := blobstore.PutObject(ctx, store, k2, iotest.DataErrReader(bytes.NewReader(body)), opts); err != nil || o.Size != int64(len(body)) || o.SHA256 != sha256Hex(body) {
				t.Fatalf("%s, io.EOF with the data: PutObject = %+v, %v", name, o, err)
			}
			if got, _, err := readOpen(t, store, k2, blobstore.OpenOptions{}); err != nil || !bytes.Equal(got, body) {
				t.Fatalf("%s, io.EOF with the data: stored %q, %v", name, got, err)
			}
			got, _, err := readOpen(t, store, k, blobstore.OpenOptions{})
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("%s: Open = %q, %v", name, got, err)
			}
		}
		o, err := blobstore.PutObject(ctx, store, key("empty"), strings.NewReader(""), blobstore.PutObjectOptions{ContentType: "text/plain"})
		if err != nil || o.Size != 0 || o.SHA256 != sha256Hex(nil) {
			t.Fatalf("empty body: %+v, %v", o, err)
		}
	})

	t.Run("a body breaking its bounds stores nothing", func(t *testing.T) {
		cases := map[string]struct {
			body io.Reader
			opts blobstore.PutObjectOptions
			want error
		}{
			"shorter than declared":        {bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: int64(len(body)) + 1}, blobstore.ErrInvalidOptions},
			"longer than declared":         {bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: int64(len(body)) - 1}, blobstore.ErrInvalidOptions},
			"longer, one byte reads":       {iotest.OneByteReader(bytes.NewReader(body)), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: 3}, blobstore.ErrInvalidOptions},
			"over MaxBytes":                {bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain", MaxBytes: int64(len(body)) - 1}, blobstore.ErrTooLarge},
			"over MaxBytes, eof with data": {iotest.DataErrReader(bytes.NewReader(body)), blobstore.PutObjectOptions{ContentType: "text/plain", MaxBytes: 4}, blobstore.ErrTooLarge},
			"declared over MaxBytes":       {bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: 10, MaxBytes: 9}, blobstore.ErrTooLarge},
			"no content type":              {bytes.NewReader(body), blobstore.PutObjectOptions{}, blobstore.ErrInvalidOptions},
			"bad content type":             {bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "not a type"}, blobstore.ErrInvalidOptions},
			"negative length":              {bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: -1}, blobstore.ErrInvalidOptions},
			"unreadable":                   {iotest.TimeoutReader(bytes.NewReader(body)), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: int64(len(body))}, iotest.ErrTimeout},
			// A reader failing with io.ErrUnexpectedEOF (a truncated
			// download it was copying, say) has failed: the error is not
			// the body's end, whatever its value, so nothing is stored.
			"fails with ErrUnexpectedEOF":        {failAfter(body[:11], io.ErrUnexpectedEOF), blobstore.PutObjectOptions{ContentType: "text/plain"}, io.ErrUnexpectedEOF},
			"fails with ErrUnexpectedEOF, sized": {failAfter(body[:11], io.ErrUnexpectedEOF), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: int64(len(body))}, io.ErrUnexpectedEOF},
			"fails with ErrUnexpectedEOF, max":   {failAfter(body[:11], io.ErrUnexpectedEOF), blobstore.PutObjectOptions{ContentType: "text/plain", MaxBytes: 1 << 20}, io.ErrUnexpectedEOF},
			// Only io.EOF itself ends a body, as for io.Copy and
			// io.ReadAll: a Read error that wraps io.EOF breaks the
			// io.Reader contract and may hide a truncated source, so it
			// fails the call (still errors.Is io.EOF) and stores nothing,
			// even when every declared byte arrived first.
			"wrapped EOF":                        {failAfter(body[:11], wrappedEOF), blobstore.PutObjectOptions{ContentType: "text/plain"}, io.EOF},
			"wrapped EOF, max":                   {failAfter(body[:11], wrappedEOF), blobstore.PutObjectOptions{ContentType: "text/plain", MaxBytes: 1 << 20}, io.EOF},
			"wrapped EOF, after the whole sized": {failAfter(body, wrappedEOF), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: int64(len(body))}, io.EOF},
			"wrapped other error":                {failAfter(body[:11], errWrappedSource), blobstore.PutObjectOptions{ContentType: "text/plain"}, errSource},
			"wrapped other error, sized":         {failAfter(body[:11], errWrappedSource), blobstore.PutObjectOptions{ContentType: "text/plain", ContentLength: int64(len(body))}, errSource},
		}
		for name, c := range cases {
			k := key("refused-" + strings.NewReplacer(" ", "-", ",", "").Replace(name))
			if o, err := blobstore.PutObject(ctx, store, k, c.body, c.opts); !errors.Is(err, c.want) {
				t.Fatalf("%s: PutObject = %+v, %v, want %v", name, o, err, c.want)
			}
			absent(t, k)
		}
		if _, err := blobstore.PutObject(ctx, store, "../escape", bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain"}); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Fatalf("bad key = %v, want ErrInvalidKey", err)
		}
	})

	t.Run("a refused put leaves the existing object", func(t *testing.T) {
		k := key("kept")
		if _, err := blobstore.PutObject(ctx, store, k, bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain"}); err != nil {
			t.Fatal(err)
		}
		if _, err := blobstore.PutObject(ctx, store, k, strings.NewReader("much too long a replacement"), blobstore.PutObjectOptions{ContentType: "image/png", MaxBytes: 4}); !errors.Is(err, blobstore.ErrTooLarge) {
			t.Fatalf("over MaxBytes = %v, want ErrTooLarge", err)
		}
		got, o, err := readOpen(t, store, k, blobstore.OpenOptions{})
		if err != nil || !bytes.Equal(got, body) || o.ContentType != "text/plain" {
			t.Fatalf("after a refused replacement: %q %+v %v, want the original", got, o, err)
		}
	})

	t.Run("open serves ranges", func(t *testing.T) {
		k := key("ranged")
		if _, err := blobstore.PutObject(ctx, store, k, bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain"}); err != nil {
			t.Fatal(err)
		}
		n := int64(len(body))
		for _, c := range []struct {
			opts blobstore.OpenOptions
			want string
		}{
			{blobstore.OpenOptions{}, string(body)},
			{blobstore.OpenOptions{Length: 5}, "hello"},
			{blobstore.OpenOptions{Offset: 7}, "object world"},
			{blobstore.OpenOptions{Offset: 7, Length: 6}, "object"},
			{blobstore.OpenOptions{Offset: 14, Length: 512}, "world"},
			{blobstore.OpenOptions{Offset: n}, ""},
			{blobstore.OpenOptions{Offset: n, Length: 1}, ""},
		} {
			got, o, err := readOpen(t, store, k, c.opts)
			if err != nil || string(got) != c.want {
				t.Fatalf("Open %+v = %q, %v, want %q", c.opts, got, err, c.want)
			}
			if o.Key != k || o.Size != n || o.ContentType != "text/plain" || o.ETag == "" || o.LastModified.IsZero() {
				t.Fatalf("Open %+v reported %+v, want the whole object", c.opts, o)
			}
		}
		for _, bad := range []blobstore.OpenOptions{{Offset: n + 1}, {Offset: -1}, {Length: -1}} {
			if _, _, err := blobstore.Open(ctx, store, k, bad); !errors.Is(err, blobstore.ErrInvalidOptions) {
				t.Fatalf("Open %+v = %v, want ErrInvalidOptions", bad, err)
			}
		}
		if _, err := blobstore.PutObject(ctx, store, key("blank"), strings.NewReader(""), blobstore.PutObjectOptions{ContentType: "text/plain"}); err != nil {
			t.Fatal(err)
		}
		if got, o, err := readOpen(t, store, key("blank"), blobstore.OpenOptions{Length: 512}); err != nil || len(got) != 0 || o.Size != 0 {
			t.Fatalf("sniffing an empty object = %q %+v %v, want an empty stream", got, o, err)
		}
		if _, _, err := blobstore.Open(ctx, store, key("missing"), blobstore.OpenOptions{}); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("Open(missing) = %v, want ErrNotFound", err)
		}
		if _, _, err := blobstore.Open(ctx, store, key("missing"), blobstore.OpenOptions{Length: 1}); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("Open(missing, a range) = %v, want ErrNotFound", err)
		}
		if _, _, err := blobstore.Open(ctx, store, "a/../b", blobstore.OpenOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Fatalf("Open(bad key) = %v, want ErrInvalidKey", err)
		}
	})

	t.Run("scoped", func(t *testing.T) {
		scoped, err := blobstore.Scoped(store, key("tenant"))
		if err != nil {
			t.Fatal(err)
		}
		o, err := blobstore.PutObject(ctx, scoped, "a.txt", bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain"})
		if err != nil || o.Key != "a.txt" {
			t.Fatalf("scoped PutObject = %+v, %v, want the key without the scope", o, err)
		}
		got, o, err := readOpen(t, scoped, "a.txt", blobstore.OpenOptions{})
		if err != nil || o.Key != "a.txt" || !bytes.Equal(got, body) {
			t.Fatalf("scoped Open = %q %+v %v", got, o, err)
		}
		if got, _, err := readOpen(t, store, key("tenant/a.txt"), blobstore.OpenOptions{}); err != nil || !bytes.Equal(got, body) {
			t.Fatalf("the scoped object is not under the scope: %q %v", got, err)
		}
		if _, _, err := blobstore.Open(ctx, scoped, "../kept", blobstore.OpenOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Fatalf("scoped Open escaped its prefix: %v", err)
		}
		if _, err := blobstore.PutObject(ctx, scoped, "../x", bytes.NewReader(body), blobstore.PutObjectOptions{ContentType: "text/plain"}); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Fatalf("scoped PutObject escaped its prefix: %v", err)
		}
	})
}

func TestMemoryObjectIO(t *testing.T) {
	t.Parallel()
	checkObjectIO(t, blobstore.NewMemory(), "io")
}

// What a browser uploaded through Memory.Put, the server reads with Open.
func TestMemoryOpenReadsAnUpload(t *testing.T) {
	t.Parallel()
	m := blobstore.NewMemory()
	png := []byte("\x89PNG\r\n\x1a\n rest of it")
	if err := m.Put("t/a", png, "image/png"); err != nil {
		t.Fatal(err)
	}
	head, o, err := readOpen(t, m, "t/a", blobstore.OpenOptions{Length: 8})
	if err != nil || string(head) != "\x89PNG\r\n\x1a\n" || o.Size != int64(len(png)) || o.ContentType != "image/png" {
		t.Fatalf("Open = %q %+v %v", head, o, err)
	}
}

// openLeak is a store whose Open and PutObject report a key outside the
// scope they were asked for; closed records whether the stream was closed.
type openLeak struct {
	leakyStore
	closed *bool
}

type closeRecorder struct {
	io.Reader
	closed *bool
}

func (c closeRecorder) Close() error { *c.closed = true; return nil }

func (l openLeak) Open(context.Context, string, blobstore.OpenOptions) (io.ReadCloser, blobstore.Object, error) {
	return closeRecorder{strings.NewReader("secret"), l.closed}, blobstore.Object{Key: "tenant-b/secret"}, nil
}

func (l openLeak) PutObject(context.Context, string, io.Reader, blobstore.PutObjectOptions) (blobstore.Object, error) {
	return blobstore.Object{Key: "tenant-b/secret"}, nil
}

func TestScopedObjectIORefusesOutOfScopeResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	closed := false
	s, err := blobstore.Scoped(openLeak{closed: &closed}, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, o, err := blobstore.Open(ctx, s, "mine", blobstore.OpenOptions{}); err == nil {
		t.Fatalf("Open = %+v, want an error", o)
	}
	if !closed {
		t.Fatal("a refused Open left the inner stream open")
	}
	if o, err := blobstore.PutObject(ctx, s, "mine", strings.NewReader("x"), blobstore.PutObjectOptions{ContentType: "text/plain"}); err == nil {
		t.Fatalf("PutObject = %+v, want an error", o)
	}
}

func TestObjectIOUnsupportedStoresAreRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	plain := leakyStore{}
	scoped, err := blobstore.Scoped(plain, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]blobstore.Store{"plain": plain, "scoped": scoped} {
		if _, _, err := blobstore.Open(ctx, s, "k", blobstore.OpenOptions{}); !errors.Is(err, blobstore.ErrUnsupported) {
			t.Errorf("%s: Open = %v, want ErrUnsupported", name, err)
		}
		if _, err := blobstore.PutObject(ctx, s, "k", strings.NewReader("x"), blobstore.PutObjectOptions{ContentType: "text/plain"}); !errors.Is(err, blobstore.ErrUnsupported) {
			t.Errorf("%s: PutObject = %v, want ErrUnsupported", name, err)
		}
	}
}

func TestInlineSafe(t *testing.T) {
	t.Parallel()
	for _, ct := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "video/mp4", "video/webm", "IMAGE/PNG", "image/png "} {
		if !blobstore.InlineSafe(ct) {
			t.Errorf("InlineSafe(%q) = false", ct)
		}
	}
	for _, ct := range []string{"", "text/html", "image/svg+xml", "application/pdf", "application/octet-stream", "text/plain",
		"image/png; charset=utf-8", "video/quicktime", "image/*", "image/png/x", "image/x-png", "application/xhtml+xml"} {
		if blobstore.InlineSafe(ct) {
			t.Errorf("InlineSafe(%q) = true", ct)
		}
	}
}

// inlineCase is one GetOptions and the response overrides its URL must
// carry (absent: none).
type inlineCase struct {
	opts                      blobstore.GetOptions
	wantType, wantDisposition string
}

var inlineCases = []inlineCase{
	{blobstore.GetOptions{Inline: true, ResponseContentType: "image/PNG"}, "image/png", "inline"},
	{blobstore.GetOptions{Inline: true, ResponseContentType: "video/webm", DownloadName: "clip one.webm"},
		"video/webm", `inline; filename="clip one.webm"; filename*=UTF-8''clip%20one.webm`},
	{blobstore.GetOptions{ResponseContentType: "image/jpeg"}, "image/jpeg", ""},
	{blobstore.GetOptions{ResponseContentType: "image/gif", DownloadName: "a.gif"}, "image/gif", `attachment; filename="a.gif"; filename*=UTF-8''a.gif`},
}

// refusedInline are GetOptions every PresignGet refuses: a forced type
// outside InlineSafe, or Inline with no forced type.
var refusedInline = []blobstore.GetOptions{
	{Inline: true},
	{Inline: true, DownloadName: "a.png"},
	{Inline: true, ResponseContentType: "text/html"},
	{Inline: true, ResponseContentType: "image/svg+xml"},
	{ResponseContentType: "text/html"},
	{ResponseContentType: "application/pdf", DownloadName: "a.pdf"},
	{Inline: true, ResponseContentType: "image/png; charset=utf-8"},
}

// checkInlineGets presigns every inlineCase and refusedInline with store,
// and returns each case's URL query for the caller's backend checks.
func checkInlineGets(t *testing.T, store blobstore.Store) []url.Values {
	t.Helper()
	ctx := context.Background()
	var out []url.Values
	for _, c := range inlineCases {
		req, err := store.PresignGet(ctx, "t1/a.png", c.opts)
		if err != nil {
			t.Fatalf("PresignGet %+v: %v", c.opts, err)
		}
		u, err := url.Parse(req.URL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if got := q.Get("response-content-type"); got != c.wantType {
			t.Fatalf("PresignGet %+v: response-content-type %q, want %q", c.opts, got, c.wantType)
		}
		if got := q.Get("response-content-disposition"); got != c.wantDisposition || (c.wantDisposition == "" && q.Has("response-content-disposition")) {
			t.Fatalf("PresignGet %+v: response-content-disposition %q, want %q", c.opts, got, c.wantDisposition)
		}
		out = append(out, q)
	}
	for _, opts := range refusedInline {
		if req, err := store.PresignGet(ctx, "t1/a.png", opts); !errors.Is(err, blobstore.ErrInvalidOptions) {
			t.Fatalf("PresignGet %+v = %+v, %v, want ErrInvalidOptions", opts, req, err)
		}
	}
	return out
}

func TestMemoryPresignGetInline(t *testing.T) {
	t.Parallel()
	checkInlineGets(t, blobstore.NewMemory())
	if _, err := blobstore.NewMemory().PresignGet(context.Background(), "k", blobstore.GetOptions{Inline: true, ResponseContentType: "image/png", TTL: blobstore.MaxTTL + time.Second}); !errors.Is(err, blobstore.ErrInvalidOptions) {
		t.Fatalf("TTL past MaxTTL = %v, want ErrInvalidOptions", err)
	}
}
