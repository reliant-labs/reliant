// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"testing"
)

// A page token is checked, not sealed: whoever recomputes the binding can
// name any cursor. That moves the listing's starting point and nothing else;
// the prefix still bounds what a page returns.
func TestForgedCursorStaysInsideThePrefix(t *testing.T) {
	t.Parallel()
	m := NewMemory()
	for _, k := range []string{"a/secret", "b/1", "b/2", "c/secret"} {
		if err := m.Put(k, []byte("x"), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	for _, cursor := range []string{"a", "a/secret", "\x00", "b/1"} {
		page, err := m.ListLevel(context.Background(), "b/", LevelOptions{PageToken: encodePageToken("memory", "", "b/", cursor)})
		if err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		for _, o := range page.Objects {
			if o.Key[:2] != "b/" {
				t.Fatalf("cursor %q listed %q, outside the prefix", cursor, o.Key)
			}
		}
	}
}

func TestPageTokenRoundTrip(t *testing.T) {
	t.Parallel()
	for _, cursor := range []string{"x", "b/c/", "opaque+/=token", string([]byte{0, 0xff})} {
		tok := encodePageToken("s3", "uploads", "p/", cursor)
		got, err := decodePageToken("s3", "uploads", "p/", tok)
		if err != nil || got != cursor {
			t.Fatalf("round trip of %q = %q, %v", cursor, got, err)
		}
		for _, other := range [][3]string{{"gcs", "uploads", "p/"}, {"s3", "other", "p/"}, {"s3", "uploads", "p"}, {"s3", "uploads", ""}} {
			if _, err := decodePageToken(other[0], other[1], other[2], tok); err == nil {
				t.Fatalf("a token for s3/uploads/p/ decoded as %v", other)
			}
		}
	}
}
