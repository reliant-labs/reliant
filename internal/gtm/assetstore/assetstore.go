// Copyright (c) 2026 Reliant Labs

// Package assetstore is gtm.AssetStore over forge's pkg/blobstore: the GTM
// assets bucket (forge.Bucket "gtm-assets", the MinIO in dev, an
// S3-compatible bucket in prod), and the sweep of PENDING assets nobody
// confirmed (Sweeper).
//
// It holds the keys to the shapes the gtm Service builds and refuses any
// other, so a mistake upstream cannot reach past an asset's own objects:
//
//   - gtm/<org_id>/<asset_id>.upload, the staging object. PresignPut signs
//     only these; nothing else here writes one, and PresignGet never serves
//     one, since its bytes are unchecked.
//   - gtm/<org_id>/<asset_id>, the asset's own key. Only Put writes it, with
//     bytes the Service read and checked, and PresignGet serves only it.
//
// # Downloads, and X-Content-Type-Options
//
// A presigned GET is answered by the bucket, not by reliant, so the only
// response headers it can set are the ones S3 and GCS let a signature
// override: Content-Type and Content-Disposition (blobstore.GetOptions).
// Neither service lets a URL add X-Content-Type-Options: nosniff, and
// neither sends it on its own (checked 2026-11-27). MinIO, the dev bucket,
// does send it on every response (minio_integration_test.go logs it). So
// the gtm contract's nosniff holds only where the backend adds it. What
// holds everywhere:
//
//   - The bucket is its own origin: no reliant cookie or session reaches it.
//   - Inline is only for blobstore.InlineSafe types (PNG, JPEG, GIF, WebP,
//     MP4, WebM), and the URL forces that Content-Type whatever was stored.
//   - Everything else is an attachment (download), never rendered.
//   - Every Content-Disposition names the file per RFC 6266: an ASCII
//     filename= fallback, and the real name UTF-8 percent-encoded in
//     filename* (RFC 5987).
//   - Final keys are written only by the server, with the type it sniffed,
//     and an attachment is served with that stored type only when it is the
//     type asked for (otherwise application/octet-stream).
//
// internal/app (PR-12) builds it: StoreFromConfig, New, and NewSweeper.
//
//forge:exclude-contract: an adapter implementing internal/gtm's AssetStore contract over forge's blobstore, plus its sweep; callers depend on gtm.AssetStore, not on this package
package assetstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"

	"github.com/reliant-labs/reliant/internal/gtm"
)

const (
	// maxObjectBytes is the largest object this store writes or presigns,
	// the gtm_assets table's own ceiling (gtm.MaxCopiedAssetBytes). The
	// Service enforces its tighter limits first.
	maxObjectBytes = gtm.MaxCopiedAssetBytes
	// DownloadTTL is how long a presigned GET stays valid.
	DownloadTTL = 5 * time.Minute
	// stagingSuffix ends a staging key.
	stagingSuffix = ".upload"
	// fallbackFilename names a download whose asset has no filename.
	fallbackFilename = "file"
)

// errNotAssetKey is a key that is not one of the two shapes of a GTM asset,
// or not the shape a call takes.
var errNotAssetKey = fmt.Errorf("%w: not a GTM asset key", blobstore.ErrInvalidKey)

// Store is gtm.AssetStore over a blobstore.Store.
type Store struct {
	blobs blobstore.Store
}

var _ gtm.AssetStore = (*Store)(nil)

// New returns the AssetStore over blobs, or nil (assets off: the Service
// fails every byte-touching asset call closed) when blobs is nil. blobs is
// the whole bucket: keys are the full gtm/<org>/... keys the Service builds.
func New(blobs blobstore.Store) gtm.AssetStore {
	if blobs == nil {
		return nil
	}
	return &Store{blobs: blobs}
}

// keyKind is which of an asset's two keys a key is.
type keyKind int

const (
	finalKey keyKind = iota + 1
	stagingKey
)

// parseKey checks key is gtm/<org>/<id> or gtm/<org>/<id>.upload, with
// non-empty segments blobstore accepts, and says which.
func parseKey(key string) (keyKind, error) {
	if err := blobstore.ValidateKey(key); err != nil {
		return 0, err
	}
	rest, ok := strings.CutPrefix(key, "gtm/")
	if !ok {
		return 0, fmt.Errorf("%w: %q", errNotAssetKey, key)
	}
	org, id, ok := strings.Cut(rest, "/")
	if !ok || org == "" || id == "" || strings.Contains(id, "/") {
		return 0, fmt.Errorf("%w: %q", errNotAssetKey, key)
	}
	if base, staging := strings.CutSuffix(id, stagingSuffix); staging {
		if base == "" {
			return 0, fmt.Errorf("%w: %q", errNotAssetKey, key)
		}
		return stagingKey, nil
	}
	return finalKey, nil
}

// needKey is parseKey refusing any kind but want.
func needKey(key string, want keyKind) error {
	kind, err := parseKey(key)
	if err != nil {
		return err
	}
	if kind != want {
		return fmt.Errorf("%w: %q is the wrong kind of key for this call", errNotAssetKey, key)
	}
	return nil
}

// notFound maps blobstore's missing object to the gtm package's.
func notFound(op, key string, err error) error {
	if errors.Is(err, blobstore.ErrNotFound) {
		return fmt.Errorf("assetstore: %s %q: %w", op, key, gtm.ErrObjectNotFound)
	}
	return fmt.Errorf("assetstore: %s %q: %w", op, key, err)
}

// mediaType is ct's base type, lowercased, or "" when ct does not parse.
func mediaType(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return mt
}

// PresignPut presigns the client's PUT of exactly opts.Size bytes of
// opts.ContentType to a staging key, valid for opts.TTL (which must be set:
// the Service bounds every upload URL by it).
func (s *Store) PresignPut(ctx context.Context, key string, opts gtm.PutOptions) (gtm.PresignedRequest, error) {
	if err := needKey(key, stagingKey); err != nil {
		return gtm.PresignedRequest{}, err
	}
	if opts.TTL <= 0 {
		return gtm.PresignedRequest{}, fmt.Errorf("%w: PresignPut needs a TTL", blobstore.ErrInvalidOptions)
	}
	req, err := s.blobs.PresignPut(ctx, key, blobstore.PutOptions{
		ContentType:   opts.ContentType,
		ContentLength: opts.Size,
		MaxBytes:      maxObjectBytes,
		TTL:           opts.TTL,
	})
	if err != nil {
		return gtm.PresignedRequest{}, fmt.Errorf("assetstore: presign PUT %q: %w", key, err)
	}
	return gtm.PresignedRequest{URL: req.URL, Headers: maps.Clone(req.Headers), ExpiresAt: req.ExpiresAt}, nil
}

// Open streams key, either kind; a missing object is gtm.ErrObjectNotFound.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if _, err := parseKey(key); err != nil {
		return nil, err
	}
	rc, _, err := blobstore.Open(ctx, s.blobs, key, blobstore.OpenOptions{})
	if err != nil {
		return nil, notFound("open", key, err)
	}
	return rc, nil
}

// Put writes an asset's own key from r, exactly opts.Size bytes of
// opts.ContentType. A body of any other length fails before anything is
// stored, and an object already there is left as it was.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, opts gtm.PutOptions) error {
	if err := needKey(key, finalKey); err != nil {
		return err
	}
	if opts.Size <= 0 {
		return fmt.Errorf("%w: Put needs the body's size", blobstore.ErrInvalidOptions)
	}
	if _, err := blobstore.PutObject(ctx, s.blobs, key, r, blobstore.PutObjectOptions{
		ContentType:   opts.ContentType,
		ContentLength: opts.Size,
		MaxBytes:      maxObjectBytes,
	}); err != nil {
		return fmt.Errorf("assetstore: put %q: %w", key, err)
	}
	return nil
}

// Delete removes key, either kind; a missing object is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	if _, err := parseKey(key); err != nil {
		return err
	}
	if err := s.blobs.Delete(ctx, key); err != nil {
		return fmt.Errorf("assetstore: delete %q: %w", key, err)
	}
	return nil
}

// Stat reports key's size (a HEAD); a missing object is
// gtm.ErrObjectNotFound.
func (s *Store) Stat(ctx context.Context, key string) (int64, error) {
	if _, err := parseKey(key); err != nil {
		return 0, err
	}
	obj, err := s.blobs.Stat(ctx, key)
	if err != nil {
		return 0, notFound("stat", key, err)
	}
	return obj.Size, nil
}

// PresignGet presigns a GET of an asset's own key for DownloadTTL (see the
// package doc for the headers it can and cannot carry):
//
//   - opts.Inline with an InlineSafe opts.ContentType: inline, that type
//     forced on the response.
//   - Otherwise an attachment under opts.Filename, carrying the stored type
//     when it is opts.ContentType (only Put writes it, with the type the
//     Service checked), and application/octet-stream when it is not.
func (s *Store) PresignGet(ctx context.Context, key string, opts gtm.GetOptions) (gtm.PresignedRequest, error) {
	if err := needKey(key, finalKey); err != nil {
		return gtm.PresignedRequest{}, err
	}
	ct := mediaType(opts.ContentType)
	if ct == "" {
		return gtm.PresignedRequest{}, fmt.Errorf("%w: PresignGet needs a content type", blobstore.ErrInvalidOptions)
	}
	name := opts.Filename
	if strings.TrimSpace(name) == "" {
		name = fallbackFilename
	}
	var (
		req blobstore.PresignedRequest
		err error
	)
	switch {
	case opts.Inline && blobstore.InlineSafe(ct):
		req, err = s.blobs.PresignGet(ctx, key, blobstore.GetOptions{
			TTL: DownloadTTL, Inline: true, ResponseContentType: ct, DownloadName: name,
		})
	default:
		req, err = s.presignAttachment(ctx, key, ct, name)
	}
	if err != nil {
		return gtm.PresignedRequest{}, fmt.Errorf("assetstore: presign GET %q: %w", key, err)
	}
	return gtm.PresignedRequest{URL: req.URL, Headers: maps.Clone(req.Headers), ExpiresAt: req.ExpiresAt}, nil
}

// presignAttachment signs a download of key under name. A signature can
// force only an InlineSafe type, so the response carries the stored type;
// it is checked to be ct first, and anything else downloads as
// application/octet-stream.
func (s *Store) presignAttachment(ctx context.Context, key, ct, name string) (blobstore.PresignedRequest, error) {
	obj, err := s.blobs.Stat(ctx, key)
	if err != nil {
		return blobstore.PresignedRequest{}, notFound("stat", key, err)
	}
	if mediaType(obj.ContentType) == ct {
		return s.blobs.PresignGet(ctx, key, blobstore.GetOptions{TTL: DownloadTTL, DownloadName: name})
	}
	return blobstore.PresignDownload(ctx, s.blobs, key, blobstore.DownloadOptions{TTL: DownloadTTL, DownloadName: name})
}
