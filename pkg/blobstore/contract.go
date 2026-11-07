// Copyright (c) 2026 Reliant Labs

// Package blobstore is object storage for files a browser uploads and
// downloads directly: the API hands out short-lived signed URLs, and the
// bytes never pass through the API process.
//
// The interface is narrow on purpose. A handler presigns an upload for a key
// it chose (PresignPut), the browser PUTs the file to that URL, and the
// handler later confirms what landed (Stat), hands out a download URL
// (PresignGet), lists a prefix or deletes. Nothing here streams bytes.
//
// The main backend speaks the S3 API (NewS3, over minio-go), which covers
// MinIO in dev, AWS S3, Cloudflare R2, and Google Cloud Storage through its
// S3 interoperability with HMAC keys. NewMemory is the in-process fake for
// unit tests.
//
// Keys are validated (ValidateKey): no leading "/", no "." or ".." segments,
// no empty segments, no backslashes or control characters. A multi-tenant
// caller wraps the store in Scoped, which joins every key under a fixed
// prefix, so code holding the scoped store cannot name another tenant's
// object, whatever key it is handed.
//
// # Bounding upload size
//
// A presigned PUT is signed with the object's exact Content-Length (and its
// Content-Type), so the storage service refuses a body of any other size or
// type: the caller states the size up front, checks it against its own limit
// (PutOptions.MaxBytes does that here), and the signature enforces it. The
// alternative, a presigned POST policy with content-length-range, is not
// portable: Cloudflare R2 does not implement POST Object at all. After the
// upload, Stat reports what actually landed; confirm it before recording it.
package blobstore

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Store is object storage addressed by key. Every method validates its key
// (ValidateKey) before calling out.
type Store interface {
	// PresignPut returns the request a client sends to upload key: a PUT of
	// exactly opts.ContentLength bytes of opts.ContentType. An existing
	// object at key is replaced by the upload.
	PresignPut(ctx context.Context, key string, opts PutOptions) (PresignedRequest, error)
	// PresignGet returns a GET request for key. It does not check that the
	// object exists; the URL answers 404 when it does not.
	PresignGet(ctx context.Context, key string, opts GetOptions) (PresignedRequest, error)
	// Stat reports the object at key, or ErrNotFound.
	Stat(ctx context.Context, key string) (Object, error)
	// Delete removes the object at key. Deleting a missing object is not an
	// error.
	Delete(ctx context.Context, key string) error
	// List returns one page of the objects whose keys start with prefix, in
	// key order. An empty prefix lists everything the store can see.
	List(ctx context.Context, prefix string, opts ListOptions) (ListPage, error)
}

// PutOptions describe the one upload a PresignPut URL admits.
type PutOptions struct {
	// ContentType is the upload's media type. Required: it is signed, so the
	// client must send exactly this Content-Type header.
	ContentType string
	// ContentLength is the upload's exact size in bytes. Required and > 0:
	// it is signed, so the storage service refuses a body of any other size.
	ContentLength int64
	// MaxBytes, when > 0, refuses to presign a ContentLength above it
	// (ErrTooLarge). The caller's own limit, checked before anything is
	// signed.
	MaxBytes int64
	// TTL is how long the URL stays valid. Zero means DefaultTTL; it must be
	// at most MaxTTL.
	TTL time.Duration
}

// GetOptions describe a PresignGet URL.
type GetOptions struct {
	// TTL is how long the URL stays valid. Zero means DefaultTTL; it must be
	// at most MaxTTL.
	TTL time.Duration
	// DownloadName, when set, makes the response a download
	// (Content-Disposition: attachment) saved under this file name.
	DownloadName string
}

// ListOptions page through List.
type ListOptions struct {
	// After lists only keys after this one: the previous page's Next.
	After string
	// Limit caps the page. Zero means MaxListLimit, and so does anything
	// above it.
	Limit int
}

// PresignedRequest is a request a client can send without credentials.
type PresignedRequest struct {
	// Method is the HTTP method: PUT for an upload, GET for a download.
	Method string
	// URL is the signed URL, valid until ExpiresAt.
	URL string
	// Headers are the headers the request must carry, exactly. For an upload
	// that is Content-Type. Content-Length is signed too, but a browser sets
	// it from the body and refuses to let a script set it; send a body of the
	// size that was presigned.
	Headers map[string]string
	// ExpiresAt is when the URL stops working.
	ExpiresAt time.Time
}

// Object is what a store reports about one stored object.
type Object struct {
	// Key is the object's key, as the caller of this store names it (a
	// Scoped store reports keys without its prefix).
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// ListPage is one page of List.
type ListPage struct {
	Objects []Object
	// Next is the ListOptions.After that continues the listing; empty when
	// this page is the last.
	Next string
}

const (
	// DefaultTTL is the lifetime of a presigned URL whose options set none:
	// long enough to upload a large file on a slow link, short enough that a
	// leaked URL goes stale quickly.
	DefaultTTL = 15 * time.Minute
	// MaxTTL is the longest lifetime SigV4 allows a presigned URL.
	MaxTTL = 7 * 24 * time.Hour
	// MaxListLimit is the largest page List returns, S3's own maximum.
	MaxListLimit = 1000
)

// The errors a Store returns, classified for svcerr: a handler that passes
// one (or an error wrapping one) to svcerr.Wrap answers with the matching
// Connect code, and the client reads only the short detail below. The
// context a store wraps around them (the key, the vendor's message) reaches
// the log, never the client.
var (
	// ErrNotFound is Stat on a key that holds no object. It is an answer,
	// not a failure: the instrumentation logs it as an expected outcome.
	ErrNotFound = svcerr.NotFound("object")
	// ErrInvalidKey is a key (or prefix) ValidateKey refuses.
	ErrInvalidKey = svcerr.InvalidArgument("invalid object key")
	// ErrInvalidOptions is a PutOptions, GetOptions or ListOptions the store
	// cannot honour: a missing content type, a non-positive length, a TTL
	// past MaxTTL.
	ErrInvalidOptions = svcerr.InvalidArgument("invalid storage request")
	// ErrTooLarge is a PutOptions.ContentLength above its MaxBytes.
	ErrTooLarge = svcerr.InvalidArgument("upload too large")
)

// errorf wraps sentinel with operator context.
func errorf(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}
