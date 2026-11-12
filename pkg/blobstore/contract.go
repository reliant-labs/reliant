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
// S3 interoperability with HMAC keys. NewGCS is Google Cloud Storage natively,
// with no HMAC key: it lists and stats through the JSON API under the
// process's own credentials and signs V4 URLs keylessly, through IAM signBlob
// as a service account. NewMemory is the in-process fake for unit tests.
//
// # Browsing and safe downloads
//
// Beside the flat List, every store here is a LevelLister: ListLevel lists
// one "/"-delimited level of the key space (objects and common prefixes) with
// an opaque continuation token, the shape a file browser pages through. And
// every store is a DownloadPresigner: PresignDownload signs a GET whose
// response is always an attachment of type application/octet-stream, so a
// stored HTML or SVG object can never render inline on the bucket's origin.
// The package-level ListLevel and PresignDownload take any Store and refuse
// (ErrUnsupported) one that does not offer them, rather than degrading to a
// weaker answer.
//
// Keys are validated (ValidateKey): no leading "/", no "." or ".." segments,
// no empty segments, no backslashes or control characters. A multi-tenant
// caller wraps the store in Scoped, which joins every key under a fixed
// prefix, so code holding the scoped store cannot name another tenant's
// object, whatever key it is handed. Privileged bucket readers may opt into
// S3Config.AllowOpaqueKeys or GCSConfig.AllowOpaqueKeys to read pre-existing
// provider names without normalizing them. That opt-in never changes upload,
// delete or Scoped validation; authorize the whole bucket before enabling it.
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

// Store is object storage addressed by key. Methods validate keys before
// calling out; configured privileged bucket reads may accept opaque provider
// names, while writes and tenant-scoped access always require ValidateKey.
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
	// ErrInvalidPageToken is a LevelOptions.PageToken this store did not
	// issue for this listing: garbled, from another prefix, bucket or
	// backend, or refused by the service.
	ErrInvalidPageToken = svcerr.InvalidArgument("invalid page token")
	// ErrUnsupported is ListLevel or PresignDownload on a Store that does
	// not implement it (every store in this package does).
	ErrUnsupported = svcerr.Unimplemented("storage operation not supported by this store")
)

// LevelLister lists one level of a store's "/"-delimited key hierarchy.
// Every Store in this package implements it; use the package-level
// ListLevel to call it on a Store.
type LevelLister interface {
	// ListLevel returns one page of what lies directly under prefix: the
	// objects whose keys start with prefix and hold no further "/" after
	// it, and the common prefixes (each ending in "/") that group the
	// deeper keys, like the entries of a directory. It never recurses.
	ListLevel(ctx context.Context, prefix string, opts LevelOptions) (LevelPage, error)
}

// LevelOptions page through ListLevel.
type LevelOptions struct {
	// PageToken continues a listing: the previous page's Next, passed with
	// the same prefix to the same store. Empty starts at the beginning. A
	// token is opaque and bound to its store, bucket and prefix; any other
	// is ErrInvalidPageToken.
	PageToken string
	// Limit caps the entries (objects plus prefixes) of the page. Zero
	// means MaxListLimit, and so does anything above it.
	Limit int
}

// LevelPage is one page of ListLevel. Objects and Prefixes are each in key
// order; together they are the page's slice of one lexical listing.
type LevelPage struct {
	// Objects are the objects at this level. Object.ContentType is empty
	// when the backend's listing does not report it (S3's does not; Stat
	// does).
	Objects []Object
	// Prefixes are the common prefixes at this level, each the prefix
	// asked for plus one more segment and a trailing "/": list one with
	// ListLevel to descend.
	Prefixes []string
	// Next is the LevelOptions.PageToken that continues the listing; empty
	// when this page is the last. A page may hold fewer than Limit entries
	// (even none) and still have a Next.
	Next string
}

// DownloadPresigner signs forced downloads. Every Store in this package
// implements it; use the package-level PresignDownload to call it on a Store.
type DownloadPresigner interface {
	// PresignDownload returns a GET request for key whose response is
	// Content-Type application/octet-stream with an attachment
	// Content-Disposition, whatever type the object was stored with. Like
	// PresignGet, it does not check that the object exists.
	PresignDownload(ctx context.Context, key string, opts DownloadOptions) (PresignedRequest, error)
}

// DownloadOptions describe a PresignDownload URL.
type DownloadOptions struct {
	// TTL is how long the URL stays valid. Zero means DefaultTTL; it must be
	// at most MaxTTL.
	TTL time.Duration
	// DownloadName is the file name the download is saved under. Empty
	// means the key's last segment.
	DownloadName string
}

// DownloadContentType is the response Content-Type PresignDownload forces:
// a type no browser renders, so the response can only be saved.
const DownloadContentType = "application/octet-stream"

// ListLevel lists one level of store under prefix (LevelLister.ListLevel).
// A store that does not implement LevelLister is ErrUnsupported: it is never
// emulated with a flat List, which would read every key below the level.
func ListLevel(ctx context.Context, store Store, prefix string, opts LevelOptions) (LevelPage, error) {
	l, ok := store.(LevelLister)
	if !ok {
		return LevelPage{}, errorf(ErrUnsupported, "%T does not implement ListLevel", store)
	}
	return l.ListLevel(ctx, prefix, opts)
}

// PresignDownload signs a forced download of key from store
// (DownloadPresigner.PresignDownload). A store that does not implement
// DownloadPresigner is ErrUnsupported: it never falls back to PresignGet,
// whose response carries the stored type.
func PresignDownload(ctx context.Context, store Store, key string, opts DownloadOptions) (PresignedRequest, error) {
	d, ok := store.(DownloadPresigner)
	if !ok {
		return PresignedRequest{}, errorf(ErrUnsupported, "%T does not implement PresignDownload", store)
	}
	return d.PresignDownload(ctx, key, opts)
}

// errorf wraps sentinel with operator context.
func errorf(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}
