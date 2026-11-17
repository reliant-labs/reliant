// Copyright (c) 2026 Reliant Labs

package blobstore_test

import (
	"encoding/base64"
	"sort"
	"strings"
)

// levelServiceKeys is levelKeys plus the zero-byte "folder" object a cloud
// console creates for "b/": a level listing of "b/" must not show it.
var levelServiceKeys = append([]string{"b/"}, levelKeys...)

// fakeLevel answers one delimited ("/") listing request over keys the way S3
// and GCS do: the keys directly under prefix and the common prefixes that
// group deeper ones, in lexical order, after cursor (the last entry of the
// previous response, exclusive; a common-prefix cursor skips its keys), at
// most max entries. next is the last entry returned when more remain.
func fakeLevel(keys []string, prefix, cursor string, max int) (objects, prefixes []string, next string) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	last, n := "", 0
	for _, k := range sorted {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		entry, isPrefix := k, false
		if i := strings.IndexByte(k[len(prefix):], '/'); i >= 0 {
			entry, isPrefix = k[:len(prefix)+i+1], true
		}
		if entry <= cursor || entry == last {
			continue
		}
		if n == max {
			return objects, prefixes, last
		}
		n, last = n+1, entry
		if isPrefix {
			prefixes = append(prefixes, entry)
		} else {
			objects = append(objects, k)
		}
	}
	return objects, prefixes, ""
}

// The fakes' continuation tokens are opaque, as the services' are, and a
// fake refuses one it did not issue.
func fakeToken(cursor string) string {
	return "opaque-" + base64.RawURLEncoding.EncodeToString([]byte(cursor))
}

func fakeCursor(token string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "opaque-"))
	return string(raw), err == nil && strings.HasPrefix(token, "opaque-")
}
