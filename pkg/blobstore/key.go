// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"fmt"
	"mime"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxKeyBytes is the longest key S3 accepts.
const MaxKeyBytes = 1024

// ValidateKey reports whether key is a safe object key: non-empty, valid
// UTF-8, at most MaxKeyBytes, "/"-separated segments none of which is empty,
// "." or "..", and no backslash or control character anywhere.
//
// S3 itself accepts all of those, which is the point: a key is often built
// from user input (a file name), and a key that LOOKS like a path but does not
// behave like one ("a/../b" is a different object from "b", "/a" from "a") is
// how a prefix check gets fooled. Refusing them keeps a key's prefix an honest
// statement of where it lives.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errorf(ErrInvalidKey, "empty key")
	case len(key) > MaxKeyBytes:
		return errorf(ErrInvalidKey, "key is %d bytes, more than %d", len(key), MaxKeyBytes)
	case !utf8.ValidString(key):
		return errorf(ErrInvalidKey, "key is not valid UTF-8")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return errorf(ErrInvalidKey, "key %q contains %q", key, r)
		}
	}
	for _, seg := range strings.Split(key, "/") {
		switch seg {
		case "":
			return errorf(ErrInvalidKey, "key %q has an empty segment (a leading, trailing or doubled \"/\")", key)
		case ".", "..":
			return errorf(ErrInvalidKey, "key %q has a %q segment", key, seg)
		}
	}
	return nil
}

// validateReadKey preserves opaque provider names only for an explicitly
// privileged bucket reader. It never normalizes paths; bucket authorization
// must already have happened. Empty, oversized and non-UTF-8 keys are refused.
func validateReadKey(key string, privileged bool) error {
	if !privileged {
		return ValidateKey(key)
	}
	if key == "" || len(key) > MaxKeyBytes || !utf8.ValidString(key) {
		return errorf(ErrInvalidKey, "invalid opaque object key")
	}
	return nil
}

func validateReadPrefix(prefix string, privileged bool) error {
	if privileged && prefix != "" {
		return validateReadKey(prefix, true)
	}
	return validatePrefix(prefix)
}

// validatePrefix accepts "" (everything) or a prefix whose segments are valid
// key segments. A trailing "/" is allowed (it lists one "directory"); anything
// else ValidateKey refuses is refused here too.
func validatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if err := ValidateKey(strings.TrimSuffix(prefix, "/")); err != nil {
		return errorf(ErrInvalidKey, "prefix %q: %v", prefix, err)
	}
	return nil
}

// validatePut checks a PresignPut's options and returns its TTL.
func validatePut(opts PutOptions) (time.Duration, error) {
	if opts.ContentType == "" {
		return 0, errorf(ErrInvalidOptions, "PutOptions.ContentType is required: it is signed into the URL")
	}
	if _, _, err := mime.ParseMediaType(opts.ContentType); err != nil {
		return 0, errorf(ErrInvalidOptions, "PutOptions.ContentType %q: %v", opts.ContentType, err)
	}
	if opts.ContentLength <= 0 {
		return 0, errorf(ErrInvalidOptions, "PutOptions.ContentLength must be > 0 (got %d): it is signed into the URL", opts.ContentLength)
	}
	if opts.MaxBytes > 0 && opts.ContentLength > opts.MaxBytes {
		return 0, errorf(ErrTooLarge, "%d bytes, the limit is %d", opts.ContentLength, opts.MaxBytes)
	}
	return ttl(opts.TTL)
}

// ttl applies DefaultTTL and the [1s, MaxTTL] bounds SigV4 enforces.
func ttl(d time.Duration) (time.Duration, error) {
	switch {
	case d == 0:
		return DefaultTTL, nil
	case d < time.Second || d > MaxTTL:
		return 0, errorf(ErrInvalidOptions, "TTL %s is outside [1s, %s]", d, MaxTTL)
	}
	return d, nil
}

// listLimit applies MaxListLimit.
func listLimit(n int) int {
	if n <= 0 || n > MaxListLimit {
		return MaxListLimit
	}
	return n
}

// contentDisposition is an attachment header saving the download as name.
// The quoted form carries an ASCII fallback; filename* (RFC 6266 / RFC 5987)
// carries the real name, which browsers prefer.
func contentDisposition(name string) string {
	fallback := strings.Map(func(r rune) rune {
		if r < 0x20 || r >= 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + fallback + `"; filename*=UTF-8''` + rfc5987(name)
}

// rfc5987 percent-encodes everything but RFC 5987's attr-char.
func rfc5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

// Scoped returns a Store that keeps every key under prefix: key "a/b" is
// stored as "<prefix>/a/b", List lists only under the prefix, and the keys
// it reports have the prefix removed. prefix must be a valid key (typically
// a tenant id, or "<tenant>/<kind>").
//
// It is the tenant boundary. Key validation refuses ".." and empty segments,
// and S3 keys are opaque strings (nothing resolves them as paths), so no key
// handed to the scoped store can name an object outside the prefix. Nor does
// it trust the wrapped store: a Stat or List answer naming a key (or cursor)
// outside the prefix is an error, never passed through.
func Scoped(store Store, prefix string) (Store, error) {
	if err := ValidateKey(prefix); err != nil {
		return nil, errorf(ErrInvalidKey, "scope prefix %q: %v", prefix, err)
	}
	return &scoped{inner: store, prefix: prefix + "/"}, nil
}

type scoped struct {
	inner  Store
	prefix string
}

func (s *scoped) key(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

func (s *scoped) PresignPut(ctx context.Context, key string, opts PutOptions) (PresignedRequest, error) {
	k, err := s.key(key)
	if err != nil {
		return PresignedRequest{}, err
	}
	return s.inner.PresignPut(ctx, k, opts)
}

func (s *scoped) PresignGet(ctx context.Context, key string, opts GetOptions) (PresignedRequest, error) {
	k, err := s.key(key)
	if err != nil {
		return PresignedRequest{}, err
	}
	return s.inner.PresignGet(ctx, k, opts)
}

func (s *scoped) Stat(ctx context.Context, key string) (Object, error) {
	k, err := s.key(key)
	if err != nil {
		return Object{}, err
	}
	o, err := s.inner.Stat(ctx, k)
	if err != nil {
		return Object{}, err
	}
	if o.Key, err = s.unscope(o.Key); err != nil {
		return Object{}, err
	}
	return o, nil
}

func (s *scoped) Delete(ctx context.Context, key string) error {
	k, err := s.key(key)
	if err != nil {
		return err
	}
	return s.inner.Delete(ctx, k)
}

func (s *scoped) List(ctx context.Context, prefix string, opts ListOptions) (ListPage, error) {
	if err := validatePrefix(prefix); err != nil {
		return ListPage{}, err
	}
	if opts.After != "" {
		if err := ValidateKey(opts.After); err != nil {
			return ListPage{}, err
		}
		opts.After = s.prefix + opts.After
	}
	page, err := s.inner.List(ctx, s.prefix+prefix, opts)
	if err != nil {
		return ListPage{}, err
	}
	// The boundary does not trust the inner store to honour the prefix it
	// was asked for: a key or cursor outside the scope fails the call
	// rather than reaching the caller (or being silently dropped, which
	// would truncate the listing).
	for i := range page.Objects {
		if page.Objects[i].Key, err = s.unscope(page.Objects[i].Key); err != nil {
			return ListPage{}, err
		}
	}
	if page.Next != "" {
		if page.Next, err = s.unscope(page.Next); err != nil {
			return ListPage{}, err
		}
	}
	return page, nil
}

// ListLevel implements LevelLister when the wrapped store does (otherwise
// ErrUnsupported). The page token passes through untouched: the inner store
// bound it to the scoped prefix, so a token from outside the scope is
// refused there.
func (s *scoped) ListLevel(ctx context.Context, prefix string, opts LevelOptions) (LevelPage, error) {
	if err := validatePrefix(prefix); err != nil {
		return LevelPage{}, err
	}
	page, err := ListLevel(ctx, s.inner, s.prefix+prefix, opts)
	if err != nil {
		return LevelPage{}, err
	}
	for i := range page.Objects {
		if page.Objects[i].Key, err = s.unscope(page.Objects[i].Key); err != nil {
			return LevelPage{}, err
		}
	}
	for i := range page.Prefixes {
		if page.Prefixes[i], err = s.unscope(page.Prefixes[i]); err != nil {
			return LevelPage{}, err
		}
	}
	return page, nil
}

// PresignDownload implements DownloadPresigner when the wrapped store does
// (otherwise ErrUnsupported). The default download name is the key's last
// segment either way, so the scope prefix never shows in it.
func (s *scoped) PresignDownload(ctx context.Context, key string, opts DownloadOptions) (PresignedRequest, error) {
	k, err := s.key(key)
	if err != nil {
		return PresignedRequest{}, err
	}
	return PresignDownload(ctx, s.inner, k, opts)
}

// unscope removes the scope prefix from a key the inner store reported, and
// refuses one that is not under it.
func (s *scoped) unscope(key string) (string, error) {
	rel, ok := strings.CutPrefix(key, s.prefix)
	if !ok || rel == "" {
		return "", fmt.Errorf("blobstore: scope %q: the inner store reported key %q, outside the scope", s.prefix, key)
	}
	if err := validatePrefix(rel); err != nil {
		return "", fmt.Errorf("blobstore: scope %q: unsafe returned key %q: %w", s.prefix, key, err)
	}
	return rel, nil
}
