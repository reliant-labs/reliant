// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/url"
	"strings"
	"time"
)

// ObjectReader streams stored objects into the API process. Every Store in
// this package implements it; use the package-level Open to call it on a
// Store.
type ObjectReader interface {
	// Open streams the object at key, or the byte range opts names, and
	// reports the whole object: Object.Size is the object's size, not the
	// range's. The caller must close the reader. A key that holds no object
	// is ErrNotFound, reported by Open itself rather than by the first Read.
	//
	// The stream outlives the call: the instrumentation's span, metrics and
	// log record cover opening it, not reading it, and reading continues
	// on ctx. So ctx must stay live until the stream is read, and a custom
	// S3Config.Chain or GCSConfig.Chain whose middleware puts a timeout on
	// the call's context would cancel the stream when the call returns.
	Open(ctx context.Context, key string, opts OpenOptions) (io.ReadCloser, Object, error)
}

// OpenOptions select the bytes Open streams. The zero value streams the
// whole object.
type OpenOptions struct {
	// Offset is the first byte to stream. At the object's size the stream
	// is empty; past it is ErrInvalidOptions. Negative is ErrInvalidOptions.
	Offset int64
	// Length caps the bytes streamed. Zero streams to the end of the object,
	// and so does a range that runs past it. Negative is ErrInvalidOptions.
	// To sniff a type, Length 512 reads what http.DetectContentType looks at.
	Length int64
}

// ObjectWriter stores a body the API process holds, with no presigned URL
// and no browser. Every Store in this package implements it; use the
// package-level PutObject to call it on a Store.
type ObjectWriter interface {
	// PutObject stores body at key, replacing any object there, and reports
	// what it stored, including Object.SHA256, hashed as the body streamed.
	// LastModified is zero when the service does not report it on upload
	// (an S3 PUT does not; Stat does).
	//
	// The body ends where its Read returns io.EOF itself, as io.Copy reads
	// it; a Read error, even one wrapping io.EOF, fails the call with that
	// error. A body that is not exactly opts.ContentLength bytes (when set)
	// is ErrInvalidOptions, and one longer than opts.MaxBytes is ErrTooLarge.
	// Either is caught while the body streams, and the upload is abandoned
	// before the service commits it, so no object is stored and an object
	// already at key is left as it was. Any other failure leaves it as it
	// was too: a single PUT never commits a partial body, and a multipart
	// upload that fails is never completed. Aborting it is best effort,
	// though (a cancelled ctx or a failing service can leave its parts
	// behind, invisible but billed), so give an S3 bucket a lifecycle rule
	// that aborts incomplete multipart uploads (AbortIncompleteMultipartUpload,
	// after a day). GCS discards an abandoned resumable upload after a week.
	PutObject(ctx context.Context, key string, body io.Reader, opts PutObjectOptions) (Object, error)
}

// PutObjectOptions describe the body PutObject stores.
type PutObjectOptions struct {
	// ContentType is the object's media type. Required, as for PresignPut.
	ContentType string
	// ContentLength, when > 0, is the body's exact size: a body that ends
	// sooner or runs longer fails the call. Zero means the size is not
	// known up front and the body is read to its end. Negative is
	// ErrInvalidOptions.
	ContentLength int64
	// MaxBytes, when > 0, is the largest body admitted: a ContentLength
	// above it is refused before anything is sent, and a body of unknown
	// size that runs past it fails the call (ErrTooLarge).
	MaxBytes int64
}

// Open streams key from store (ObjectReader.Open). A store that does not
// implement ObjectReader is ErrUnsupported.
func Open(ctx context.Context, store Store, key string, opts OpenOptions) (io.ReadCloser, Object, error) {
	r, ok := store.(ObjectReader)
	if !ok {
		return nil, Object{}, errorf(ErrUnsupported, "%T does not implement Open", store)
	}
	return r.Open(ctx, key, opts)
}

// PutObject stores body at key in store (ObjectWriter.PutObject). A store
// that does not implement ObjectWriter is ErrUnsupported.
func PutObject(ctx context.Context, store Store, key string, body io.Reader, opts PutObjectOptions) (Object, error) {
	w, ok := store.(ObjectWriter)
	if !ok {
		return Object{}, errorf(ErrUnsupported, "%T does not implement PutObject", store)
	}
	return w.PutObject(ctx, key, body, opts)
}

// validateOpen checks an Open's options.
func validateOpen(opts OpenOptions) error {
	if opts.Offset < 0 || opts.Length < 0 {
		return errorf(ErrInvalidOptions, "OpenOptions offset %d, length %d: neither may be negative", opts.Offset, opts.Length)
	}
	return nil
}

// openRange resolves opts against an object of size bytes: the offset to
// read from and how many bytes to read.
func openRange(size int64, opts OpenOptions) (offset, n int64, err error) {
	if opts.Offset > size {
		return 0, 0, errorf(ErrInvalidOptions, "OpenOptions.Offset %d is past the object's end (%d bytes)", opts.Offset, size)
	}
	n = size - opts.Offset
	if opts.Length > 0 && opts.Length < n {
		n = opts.Length
	}
	return opts.Offset, n, nil
}

// emptyBody is the stream of a range that holds no bytes.
func emptyBody() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }

// validatePutObject checks a PutObject's options.
func validatePutObject(opts PutObjectOptions) error {
	if opts.ContentType == "" {
		return errorf(ErrInvalidOptions, "PutObjectOptions.ContentType is required")
	}
	if _, _, err := mime.ParseMediaType(opts.ContentType); err != nil {
		return errorf(ErrInvalidOptions, "PutObjectOptions.ContentType %q: %v", opts.ContentType, err)
	}
	if opts.ContentLength < 0 {
		return errorf(ErrInvalidOptions, "PutObjectOptions.ContentLength must be >= 0 (got %d)", opts.ContentLength)
	}
	if opts.MaxBytes > 0 && opts.ContentLength > opts.MaxBytes {
		return errorf(ErrTooLarge, "%d bytes, the limit is %d", opts.ContentLength, opts.MaxBytes)
	}
	return nil
}

// putBody is a PutObject body on its way to the service. It holds the body
// to its declared length (exact) or to MaxBytes (max), and hashes every byte
// that passes. A body that breaks a bound fails the Read that would have
// delivered its last admissible bytes, so the service never receives a
// complete body and commits nothing: the bytes of that Read are withheld,
// and the error is returned with n == 0 (io.ReadFull would drop an error
// that came with a full buffer).
type putBody struct {
	r     io.Reader
	exact int64 // > 0: the body must be exactly this long
	max   int64 // > 0: the body may be at most this long
	n     int64
	sha   hash.Hash
	done  bool
	// err is the first bound the body broke, or the first error reading it.
	// It outranks whatever error the service's client wrapped it in.
	err error
}

func newPutBody(r io.Reader, opts PutObjectOptions) *putBody {
	return &putBody{r: r, exact: opts.ContentLength, max: opts.MaxBytes, sha: sha256.New()}
}

// limit is the most bytes the body may hold, or -1 for no bound.
func (b *putBody) limit() int64 {
	switch {
	case b.exact > 0:
		return b.exact
	case b.max > 0:
		return b.max
	}
	return -1
}

func (b *putBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.done {
		return 0, io.EOF
	}
	limit := b.limit()
	if limit >= 0 {
		rem := limit - b.n
		if rem == 0 {
			if err := b.atLimit(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if int64(len(p)) > rem {
			p = p[:rem]
		}
	}
	n, err := b.r.Read(p)
	b.n += int64(n)
	b.sha.Write(p[:n])
	switch {
	// Only io.EOF itself ends the body. The io.Reader contract has Read
	// return io.EOF unwrapped, and io.Copy and io.ReadAll compare with ==
	// too. An error that wraps io.EOF ("copying the source: EOF") is the
	// source failing, possibly truncated: taking it for the end would store
	// a short body of unknown size and report success. It fails the call
	// (still errors.Is io.EOF) and nothing is stored.
	case err == io.EOF: //nolint:errorlint // only io.EOF itself ends a body; a wrapped one is a failure (see above)
		if b.exact > 0 && b.n < b.exact {
			return 0, b.fail(errorf(ErrInvalidOptions, "the body ended after %d of its declared %d bytes", b.n, b.exact))
		}
		b.done = true
		return n, io.EOF
	case err != nil:
		return 0, b.fail(fmt.Errorf("reading the body: %w", err))
	case limit >= 0 && b.n == limit:
		if err := b.atLimit(); err != nil {
			return 0, err
		}
		return n, io.EOF
	}
	return n, nil
}

// atLimit checks that a body which reached its limit ends there: nil when
// it does, and the broken bound when another byte follows.
func (b *putBody) atLimit() error {
	var one [1]byte
	for attempt := 0; ; attempt++ {
		if attempt == 100 {
			return b.fail(io.ErrNoProgress)
		}
		n, err := b.r.Read(one[:])
		if n > 0 {
			if b.exact > 0 {
				return b.fail(errorf(ErrInvalidOptions, "the body is longer than its declared %d bytes", b.exact))
			}
			return b.fail(errorf(ErrTooLarge, "the body is longer than the limit of %d bytes", b.max))
		}
		if err == io.EOF { //nolint:errorlint // as in Read: only io.EOF itself ends a body
			b.done = true
			return nil
		}
		if err != nil {
			return b.fail(fmt.Errorf("reading the body: %w", err))
		}
	}
}

func (b *putBody) fail(err error) error {
	b.err = err
	return err
}

// failed is the error PutObject reports when the upload failed: the body's
// own error when it broke a bound or could not be read, which is the cause
// of whatever the client then reported, and otherwise the client's.
func (b *putBody) failed(err error) error {
	if b.err != nil {
		return b.err
	}
	return err
}

// object reports what a completed upload of the body stored.
func (b *putBody) object(key, contentType, etag string, modified time.Time) Object {
	return Object{
		Key:          key,
		Size:         b.n,
		ContentType:  contentType,
		ETag:         etag,
		LastModified: modified,
		SHA256:       b.sum(),
	}
}

// sum is the lowercase hex SHA-256 of the bytes read so far.
func (b *putBody) sum() string { return hex.EncodeToString(b.sha.Sum(nil)) }

// inlineTypes are the media types a GetOptions.ResponseContentType may
// force: images and video a browser decodes and displays without running
// anything, the storage browser's own preview rule. Never add a type a
// browser can execute or script (text/html, image/svg+xml, application/pdf,
// anything XML or JavaScript).
var inlineTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"video/mp4":  true,
	"video/webm": true,
}

// InlineSafe reports whether contentType is one a presigned GET may force
// for an inline preview (GetOptions.ResponseContentType): image/png,
// image/jpeg, image/gif, image/webp, video/mp4 or video/webm, in any case,
// with no parameters.
func InlineSafe(contentType string) bool {
	_, ok := inlineType(contentType)
	return ok
}

// inlineType is contentType's canonical (lowercase) form, if InlineSafe
// accepts it.
func inlineType(contentType string) (string, bool) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 || !inlineTypes[mt] {
		return "", false
	}
	return mt, true
}

// getParams checks a PresignGet's options and returns its TTL and the
// response overrides it signs (nil when there are none), named as S3, GCS's
// XML API and Memory all name them.
func getParams(opts GetOptions) (time.Duration, url.Values, error) {
	d, err := ttl(opts.TTL)
	if err != nil {
		return 0, nil, err
	}
	var params url.Values
	if opts.ResponseContentType != "" {
		ct, ok := inlineType(opts.ResponseContentType)
		if !ok {
			return 0, nil, errorf(ErrInvalidOptions, "GetOptions.ResponseContentType %q is not a type a response may be forced to (see InlineSafe)", opts.ResponseContentType)
		}
		params = url.Values{"response-content-type": {ct}}
	}
	switch {
	case opts.Inline && params == nil:
		return 0, nil, errorf(ErrInvalidOptions, "GetOptions.Inline needs ResponseContentType: an inline response never carries the stored type")
	case opts.Inline:
		params.Set("response-content-disposition", disposition("inline", opts.DownloadName))
	case opts.DownloadName != "":
		if params == nil {
			params = url.Values{}
		}
		params.Set("response-content-disposition", contentDisposition(opts.DownloadName))
	}
	return d, params, nil
}
