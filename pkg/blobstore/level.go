// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"path"
	"strings"
	"time"
)

// A ListLevel page token is base64url(version | binding | cursor). The
// cursor is the backend's own continuation state (S3's continuation token,
// GCS's page token, Memory's last entry). The binding is a digest of the
// backend, bucket and prefix the token was issued for, so a token replayed
// against another listing is refused here instead of being handed to the
// service. It is a check, not a seal: the digest is unkeyed, so a caller who
// must not be able to forge a cursor seals the token itself (as an rpc that
// hands it to a browser does).
const (
	pageTokenVersion = 1
	bindingBytes     = 8
)

func pageTokenBinding(backend, bucket, prefix string) []byte {
	sum := sha256.Sum256([]byte("forge/blobstore/list-level\x00" + backend + "\x00" + bucket + "\x00" + prefix))
	return sum[:bindingBytes]
}

// encodePageToken wraps a non-empty backend cursor for (backend, bucket, prefix).
func encodePageToken(backend, bucket, prefix, cursor string) string {
	b := make([]byte, 0, 1+bindingBytes+len(cursor))
	b = append(b, pageTokenVersion)
	b = append(b, pageTokenBinding(backend, bucket, prefix)...)
	b = append(b, cursor...)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodePageToken returns the cursor of a token encodePageToken issued for
// the same (backend, bucket, prefix), "" for an empty token, and
// ErrInvalidPageToken for anything else.
func decodePageToken(backend, bucket, prefix, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	switch {
	case err != nil:
		return "", errorf(ErrInvalidPageToken, "not a page token: %v", err)
	case len(raw) <= 1+bindingBytes || raw[0] != pageTokenVersion:
		return "", errorf(ErrInvalidPageToken, "not a page token (%d bytes)", len(raw))
	case subtle.ConstantTimeCompare(raw[1:1+bindingBytes], pageTokenBinding(backend, bucket, prefix)) != 1:
		return "", errorf(ErrInvalidPageToken, "the token was issued for another listing, not prefix %q", prefix)
	}
	return string(raw[1+bindingBytes:]), nil
}

// isFolderPlaceholder reports whether an object a level listing returned is
// the zero-byte "folder" a console creates for the prefix itself ("a/b/"
// listed under "a/b/"). It is the level being listed, not an entry of it.
func isFolderPlaceholder(key, prefix string) bool {
	return key == prefix && strings.HasSuffix(prefix, "/")
}

// downloadParams checks a PresignDownload's options and returns its TTL and
// the response overrides every backend signs: S3, GCS's XML API and Memory
// all name them response-content-type and response-content-disposition.
func downloadParams(key string, opts DownloadOptions) (time.Duration, url.Values, error) {
	d, err := ttl(opts.TTL)
	if err != nil {
		return 0, nil, err
	}
	name := opts.DownloadName
	if name == "" {
		name = path.Base(key)
	}
	return d, url.Values{
		"response-content-type":        {DownloadContentType},
		"response-content-disposition": {contentDisposition(name)},
	}, nil
}
