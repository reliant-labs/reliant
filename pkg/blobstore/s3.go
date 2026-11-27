// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/s3utils"
	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"
)

// scope is the instrumentation scope of the S3 backend's spans and metrics.
const scope = "github.com/reliant-labs/forge/pkg/blobstore"

// S3Config configures NewS3. Every field is a value the app reads from its
// typed config (deploy/kcl's forge.BucketRef fills the env behind it); this
// package reads no environment of its own.
type S3Config struct {
	// Endpoint is the S3 API's base URL as THIS process reaches it, scheme
	// included: "http://localhost:9000" (MinIO in dev),
	// "https://s3.us-east-1.amazonaws.com", "https://<account>.r2.cloudflarestorage.com",
	// "https://storage.googleapis.com" (GCS interoperability). Required.
	Endpoint string
	// PublicEndpoint is the base URL a BROWSER reaches the same service at,
	// when that differs from Endpoint: a pod dials MinIO at
	// http://host.k3d.internal:9000 while the browser beside it uses
	// http://localhost:9000. Presigned URLs are signed for this host (the
	// host is part of the signature, so a URL cannot be rewritten after
	// signing). Empty means Endpoint.
	PublicEndpoint string
	// Region is the bucket's region. Empty means "us-east-1", which is what
	// MinIO and GCS interoperability accept; R2 takes "auto". Always set
	// explicitly on the client, so presigning never makes a network call to
	// look the region up.
	Region string
	// Bucket is the bucket every key lives in. Required.
	Bucket string
	// AllowOpaqueKeys permits reads/listing of existing path-like object names
	// for a privileged caller authorized for the entire bucket. Writes/deletes
	// and Scoped tenant-relative input remain strict. Default false.
	AllowOpaqueKeys bool
	// AccessKeyID and SecretAccessKey are the S3 credentials (an HMAC key
	// for GCS). Required.
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle addresses the bucket as <endpoint>/<bucket>/<key> rather
	// than <bucket>.<endpoint>/<key>. MinIO needs it; for the others false
	// lets the client choose.
	PathStyle bool

	// Transport is the base RoundTripper (nil: http.DefaultTransport). It is
	// wrapped by observe.NewHTTPTransport, so every request to the storage
	// service is a client span carrying the caller's trace context.
	Transport http.RoundTripper
	// Logger receives the instrumentation's call records. nil: slog.Default.
	Logger *slog.Logger
	// Chain replaces the default instrumentation: a span, the
	// blobstore.{calls,errors,duration} metrics and a log record per call,
	// with ErrNotFound logged as an expected outcome. Build one with
	// observe.NewComponentChain to change it.
	Chain *observe.ComponentChain
}

// S3 is a Store on an S3-compatible service. Build it with NewS3.
type S3 struct {
	bucket          string
	allowOpaqueKeys bool
	// api performs Stat, Delete and List against Endpoint; presign signs
	// URLs for PublicEndpoint. They are the same client when the two match.
	api, presign *minio.Client
	// listConfig builds a per-call, single-attempt client. Keep a pointer,
	// not a function field, so S3 retains its v1 comparability contract.
	listConfig *S3Config
	transport  http.RoundTripper
	chain      *observe.ComponentChain
}

var (
	_ Store             = (*S3)(nil)
	_ LevelLister       = (*S3)(nil)
	_ DownloadPresigner = (*S3)(nil)
	_ ObjectReader      = (*S3)(nil)
	_ ObjectWriter      = (*S3)(nil)
)

// NewS3 validates cfg and builds the store. It makes no network call: a
// wrong credential or a missing bucket surfaces on the first Stat, Delete or
// List, and as a refused upload.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("blobstore: S3Config needs Bucket, AccessKeyID and SecretAccessKey")
	}
	if err := s3utils.CheckValidBucketNameStrict(cfg.Bucket); err != nil {
		return nil, fmt.Errorf("blobstore: S3Config.Bucket %q: %w", cfg.Bucket, err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	transport := observe.NewHTTPTransport(cfg.Transport)
	api, err := newMinioClient(cfg, cfg.Endpoint, transport, 0)
	if err != nil {
		return nil, fmt.Errorf("blobstore: S3Config.Endpoint: %w", err)
	}
	presign := api
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		if presign, err = newMinioClient(cfg, cfg.PublicEndpoint, transport, 0); err != nil {
			return nil, fmt.Errorf("blobstore: S3Config.PublicEndpoint: %w", err)
		}
	}
	return &S3{
		bucket:          cfg.Bucket,
		allowOpaqueKeys: cfg.AllowOpaqueKeys,
		api:             api,
		presign:         presign,
		listConfig:      &cfg,
		transport:       transport,
		chain:           instrumentation(cfg.Chain, cfg.Logger),
	}, nil
}

// instrumentation is chain, or the default one: a span, the
// blobstore.{calls,errors,duration} metrics and a log record per call.
func instrumentation(chain *observe.ComponentChain, logger *slog.Logger) *observe.ComponentChain {
	if chain != nil {
		return chain
	}
	if logger == nil {
		logger = slog.Default()
	}
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "blobstore"),
		// A missing object is the answer a 404 is built from, not a failure.
		observe.LogMiddleware(logger, slog.LevelDebug, observe.WithExpectedErrors(ErrNotFound)),
	)
}

// newMinioClient builds a client for endpoint, a base URL with scheme.
// maxRetries caps minio's attempts per request; 0 keeps its default.
func newMinioClient(cfg S3Config, endpoint string, transport http.RoundTripper, maxRetries int) (*minio.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("%q must be a base URL like https://host[:port], with no path", endpoint)
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	return minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure:       u.Scheme == "https",
		Transport:    transport,
		Region:       cfg.Region,
		BucketLookup: lookup,
		MaxRetries:   maxRetries,
	})
}

// PresignPut implements Store. The URL signs the Content-Type and the exact
// Content-Length, so the service refuses any other body.
func (s *S3) PresignPut(ctx context.Context, key string, opts PutOptions) (PresignedRequest, error) {
	return s.chain.Around(ctx, "blobstore.PresignPut", func(ctx context.Context) (PresignedRequest, error) {
		if err := ValidateKey(key); err != nil {
			return PresignedRequest{}, err
		}
		d, err := validatePut(opts)
		if err != nil {
			return PresignedRequest{}, err
		}
		signed := http.Header{}
		signed.Set("Content-Type", opts.ContentType)
		signed.Set("Content-Length", strconv.FormatInt(opts.ContentLength, 10))
		now := time.Now()
		u, err := s.presign.PresignHeader(ctx, http.MethodPut, s.bucket, key, d, nil, signed)
		if err != nil {
			return PresignedRequest{}, fmt.Errorf("blobstore: presign PUT %q: %w", key, err)
		}
		return PresignedRequest{
			Method:    http.MethodPut,
			URL:       u.String(),
			Headers:   map[string]string{"Content-Type": opts.ContentType},
			ExpiresAt: now.Add(d),
		}, nil
	})
}

// PresignGet implements Store.
func (s *S3) PresignGet(ctx context.Context, key string, opts GetOptions) (PresignedRequest, error) {
	return s.chain.Around(ctx, "blobstore.PresignGet", func(ctx context.Context) (PresignedRequest, error) {
		if err := validateReadKey(key, s.allowOpaqueKeys); err != nil {
			return PresignedRequest{}, err
		}
		d, params, err := getParams(opts)
		if err != nil {
			return PresignedRequest{}, err
		}
		now := time.Now()
		u, err := s.presign.PresignedGetObject(ctx, s.bucket, key, d, params)
		if err != nil {
			return PresignedRequest{}, fmt.Errorf("blobstore: presign GET %q: %w", key, err)
		}
		return PresignedRequest{Method: http.MethodGet, URL: u.String(), ExpiresAt: now.Add(d)}, nil
	})
}

// Stat implements Store.
func (s *S3) Stat(ctx context.Context, key string) (Object, error) {
	return s.chain.Around(ctx, "blobstore.Stat", func(ctx context.Context) (Object, error) {
		if err := validateReadKey(key, s.allowOpaqueKeys); err != nil {
			return Object{}, err
		}
		info, err := s.api.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
		if err != nil {
			return Object{}, s.mapErr("stat", key, err)
		}
		return object(info), nil
	})
}

// Delete implements Store.
func (s *S3) Delete(ctx context.Context, key string) error {
	return s.chain.Run(ctx, "blobstore.Delete", func(ctx context.Context) error {
		if err := ValidateKey(key); err != nil {
			return err
		}
		if err := s.api.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
			return s.mapErr("delete", key, err)
		}
		return nil
	})
}

// List implements Store.
func (s *S3) List(ctx context.Context, prefix string, opts ListOptions) (ListPage, error) {
	return s.chain.Around(ctx, "blobstore.List", func(ctx context.Context) (ListPage, error) {
		if err := validateReadPrefix(prefix, s.allowOpaqueKeys); err != nil {
			return ListPage{}, err
		}
		limit := listLimit(opts.Limit)
		// Stop the listing goroutine once the page is full.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var page ListPage
		// MaxKeys is minio's per-request page size, not a cap on the
		// iteration. Asking for one key past the page lets the first
		// response alone show whether another page exists (the iterator
		// would otherwise make a second request, with the continuation
		// token, to find out). The service clamps it to its own 1000.
		for info := range s.api.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
			Prefix:     prefix,
			StartAfter: opts.After,
			Recursive:  true,
			MaxKeys:    limit + 1,
		}) {
			if info.Err != nil {
				return ListPage{}, s.mapErr("list", prefix, info.Err)
			}
			if len(page.Objects) == limit {
				// The one-past object only proves another page exists;
				// it is not returned. Next is the last key returned, and
				// the next List is a fresh request (no continuation token)
				// with start-after = Next, which S3 treats as exclusive,
				// so it starts at that object. The iterator's own
				// continuation state, discarded by cancel, is never needed.
				page.Next = page.Objects[limit-1].Key
				break
			}
			page.Objects = append(page.Objects, object(info))
		}
		return page, nil
	})
}

// ListLevel implements LevelLister: one ListObjectsV2 request with
// delimiter "/" and max-keys Limit. Its page token wraps the service's own
// continuation token, so a page never re-reads what an earlier one returned,
// even when it ended on a common prefix.
func (s *S3) ListLevel(ctx context.Context, prefix string, opts LevelOptions) (LevelPage, error) {
	return s.chain.Around(ctx, "blobstore.ListLevel", func(ctx context.Context) (LevelPage, error) {
		if err := validateReadPrefix(prefix, s.allowOpaqueKeys); err != nil {
			return LevelPage{}, err
		}
		cont, err := decodePageToken("s3", s.bucket, prefix, opts.PageToken)
		if err != nil {
			return LevelPage{}, err
		}
		res, err := s.listObjectsV2(ctx, prefix, cont, listLimit(opts.Limit))
		if err != nil {
			if resp := minio.ToErrorResponse(err); cont != "" && (resp.Code == "InvalidArgument" || resp.StatusCode == http.StatusBadRequest) {
				return LevelPage{}, errorf(ErrInvalidPageToken, "list level %q in bucket %q: the service refused the continuation token: %v", prefix, s.bucket, err)
			}
			return LevelPage{}, fmt.Errorf("blobstore: list level %q in bucket %q: %w", prefix, s.bucket, err)
		}
		var page LevelPage
		for _, info := range res.Contents {
			if isFolderPlaceholder(info.Key, prefix) {
				continue
			}
			info.ETag = strings.Trim(info.ETag, `"`)
			page.Objects = append(page.Objects, object(info))
		}
		for _, p := range res.CommonPrefixes {
			page.Prefixes = append(page.Prefixes, p.Prefix)
		}
		if res.IsTruncated {
			if res.NextContinuationToken == "" {
				return LevelPage{}, fmt.Errorf("blobstore: list level %q in bucket %q: the service truncated the listing without a continuation token", prefix, s.bucket)
			}
			page.Next = encodePageToken("s3", s.bucket, prefix, res.NextContinuationToken)
		}
		return page, nil
	})
}

// listAttempts bounds the attempts listObjectsV2 makes at one page.
const listAttempts = 3

// listObjectsV2 is one delimited ListObjectsV2 request. minio-go exposes a
// single request with its continuation token only through Core, whose
// ListObjectsV2 takes no context. (Its iterator takes a context but hides
// the token, and resuming it by start-after re-reads a common prefix the
// previous page ended on.) So the request goes through a client built for
// this call whose transport carries ctx: cancellation and the deadline end
// the HTTP request itself, and its connection, not just the wait for it.
// That client makes one attempt, because minio would retry on its own
// background context; the retries of a transient failure run here, on ctx.
func (s *S3) listObjectsV2(ctx context.Context, prefix, cont string, maxKeys int) (minio.ListBucketV2Result, error) {
	client, err := newMinioClient(*s.listConfig, s.listConfig.Endpoint, contextTransport{ctx: ctx, base: s.transport}, 1)
	if err != nil {
		return minio.ListBucketV2Result{}, err
	}
	core := minio.Core{Client: client}
	for attempt := 1; ; attempt++ {
		res, err := core.ListObjectsV2(s.bucket, prefix, "", cont, "/", maxKeys)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return minio.ListBucketV2Result{}, ctxErr
		}
		if err == nil || attempt == listAttempts || !retryableList(err) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return minio.ListBucketV2Result{}, ctx.Err()
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
}

// retryableList reports whether a failed listing is worth another attempt:
// no response at all, throttling, or a server error.
func retryableList(err error) bool {
	code := minio.ToErrorResponse(err).StatusCode
	return code == 0 || code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// contextTransport sends every request with ctx, for a client whose API
// builds its requests on a background context.
type contextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t contextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r.WithContext(t.ctx))
}

// PresignDownload implements DownloadPresigner.
func (s *S3) PresignDownload(ctx context.Context, key string, opts DownloadOptions) (PresignedRequest, error) {
	return s.chain.Around(ctx, "blobstore.PresignDownload", func(ctx context.Context) (PresignedRequest, error) {
		if err := validateReadKey(key, s.allowOpaqueKeys); err != nil {
			return PresignedRequest{}, err
		}
		d, params, err := downloadParams(key, opts)
		if err != nil {
			return PresignedRequest{}, err
		}
		now := time.Now()
		u, err := s.presign.PresignedGetObject(ctx, s.bucket, key, d, params)
		if err != nil {
			return PresignedRequest{}, fmt.Errorf("blobstore: presign download %q: %w", key, err)
		}
		return PresignedRequest{Method: http.MethodGet, URL: u.String(), ExpiresAt: now.Add(d)}, nil
	})
}

// opened is what Open hands back through the instrumentation.
type opened struct {
	body io.ReadCloser
	obj  Object
}

// Open implements ObjectReader: one GET, with a Range header when opts name
// a range. The object's full size comes from the same response's
// Content-Range, so it describes the version whose bytes are streamed.
func (s *S3) Open(ctx context.Context, key string, opts OpenOptions) (io.ReadCloser, Object, error) {
	o, err := s.chain.Around(ctx, "blobstore.Open", func(ctx context.Context) (opened, error) {
		if err := validateReadKey(key, s.allowOpaqueKeys); err != nil {
			return opened{}, err
		}
		if err := validateOpen(opts); err != nil {
			return opened{}, err
		}
		var get minio.GetObjectOptions
		ranged := opts.Offset > 0 || opts.Length > 0
		if ranged {
			end := int64(0) // to the end
			if opts.Length > 0 && opts.Length <= math.MaxInt64-opts.Offset {
				end = opts.Offset + opts.Length - 1
			}
			if err := get.SetRange(opts.Offset, end); err != nil {
				return opened{}, errorf(ErrInvalidOptions, "open %q: %v", key, err)
			}
		}
		body, info, header, err := minio.Core{Client: s.api}.GetObject(ctx, s.bucket, key, get)
		if err != nil {
			if minio.ToErrorResponse(err).StatusCode == http.StatusRequestedRangeNotSatisfiable {
				return s.openPastEnd(ctx, key, opts)
			}
			return opened{}, s.mapErr("open", key, err)
		}
		obj := object(info)
		if ranged {
			// A service that ignores Range answers 200 with the whole
			// object: its bytes start at 0, not at the offset, and its
			// Content-Length is no range's. Refuse it rather than serve
			// the wrong bytes.
			size, ok := contentRangeSize(header.Get("Content-Range"))
			if !ok {
				_ = body.Close()
				return opened{}, fmt.Errorf("blobstore: open %q in bucket %q: the service answered a ranged GET without a Content-Range", key, s.bucket)
			}
			obj.Size = size
			if opts.Length > 0 {
				// Never more than asked, whatever the service sends.
				body = limitedBody{Reader: io.LimitReader(body, opts.Length), Closer: body}
			}
		}
		return opened{body: body, obj: obj}, nil
	})
	if err != nil {
		return nil, Object{}, err
	}
	return o.body, o.obj, nil
}

// openPastEnd answers a range S3 refused as unsatisfiable: one that starts
// at or past the object's end, which S3 refuses even when it is empty (the
// first 512 bytes of an empty object). At the end the stream is empty; past
// it the offset is invalid.
func (s *S3) openPastEnd(ctx context.Context, key string, opts OpenOptions) (opened, error) {
	info, err := s.api.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return opened{}, s.mapErr("open", key, err)
	}
	if _, _, err := openRange(info.Size, opts); err != nil {
		return opened{}, err
	}
	return opened{body: emptyBody(), obj: object(info)}, nil
}

// limitedBody caps a response body's Reader and closes the body itself.
type limitedBody struct {
	io.Reader
	io.Closer
}

// contentRangeSize is the complete length a Content-Range header
// ("bytes 0-511/1234") states.
func contentRangeSize(v string) (int64, bool) {
	_, total, ok := strings.Cut(v, "/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	return n, err == nil && n >= 0
}

// s3PartSize is the part size of a multipart PutObject, and the most of a
// body of unknown size PutObject buffers to find out whether it fits one
// PUT: minio-go's own minimum part size.
const s3PartSize = 16 << 20

// s3FirstChunk is the first buffer bufferPart allocates. Each next one is
// twice the last, so a small body costs little and nothing is ever copied.
const s3FirstChunk = 64 << 10

// bufferPart reads r into chunks holding at most limit bytes in all, and
// reports how many it read and whether r ended within them. The chunks are
// never grown or copied, so buffering holds limit bytes at most, never the
// doubling a growing buffer reaches.
func bufferPart(r io.Reader, limit int64) (chunks [][]byte, n int64, ended bool, err error) {
	size := int64(s3FirstChunk)
	for n < limit {
		c := make([]byte, min(size, limit-n))
		m, rerr := io.ReadFull(r, c)
		n += int64(m)
		if m > 0 {
			chunks = append(chunks, c[:m])
		}
		// io.ReadFull returns io.EOF and io.ErrUnexpectedEOF themselves, and
		// only when r ended. Compare by identity: an error that merely
		// wraps one (putBody's "reading the body: unexpected EOF", a
		// reader's own failure) is the body failing, never its end.
		switch {
		case rerr == io.EOF || rerr == io.ErrUnexpectedEOF: //nolint:errorlint // io.ReadFull's own sentinels, never wrapped; see above
			return chunks, n, true, nil
		case rerr != nil:
			return nil, n, false, rerr
		}
		size *= 2
	}
	return chunks, n, false, nil
}

// s3BufferLimit is how much of a body of unknown size PutObject buffers:
// one part, or MaxBytes+1 when that is less. The body ends by MaxBytes or
// fails, and one byte more always sees which.
func s3BufferLimit(maxBytes int64) int64 {
	if maxBytes > 0 && maxBytes < s3PartSize {
		return maxBytes + 1
	}
	return s3PartSize
}

// chunkReader reads chunks in order as one seekable stream. minio-go
// retries a failed PUT only when it can seek its body back to the start, so
// a buffered body of any size stays retryable without being copied into one
// contiguous buffer.
type chunkReader struct {
	chunks    [][]byte
	size, pos int64
}

func chunksReader(chunks [][]byte) *chunkReader {
	r := &chunkReader{chunks: chunks}
	for _, c := range chunks {
		r.size += int64(len(c))
	}
	return r
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	n, skip := 0, r.pos
	for _, c := range r.chunks {
		if skip >= int64(len(c)) {
			skip -= int64(len(c))
			continue
		}
		n += copy(p[n:], c[skip:])
		skip = 0
		if n == len(p) {
			break
		}
	}
	r.pos += int64(n)
	return n, nil
}

// Seek implements io.Seeker.
func (r *chunkReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.pos
	case io.SeekEnd:
		offset += r.size
	default:
		return 0, errors.New("blobstore: chunkReader.Seek: invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("blobstore: chunkReader.Seek: negative position")
	}
	r.pos = offset
	return offset, nil
}

// PutObject implements ObjectWriter. A body of known size is one PUT (a
// multipart upload past s3PartSize). A body of unknown size is buffered
// first, up to s3PartSize (or MaxBytes+1 when that is less): one that ends
// within it is a single PUT of known size, and a longer one streams as a
// multipart upload. So PutObject itself holds at most s3PartSize of a body,
// beside minio-go's own part buffer.
//
// When the body or a part fails, minio-go aborts the multipart upload, on
// ctx: best effort, since a cancelled ctx or a failing service can leave the
// uploaded parts behind, invisible but billed. Give the bucket a lifecycle
// rule (AbortIncompleteMultipartUpload) that cleans them up.
func (s *S3) PutObject(ctx context.Context, key string, body io.Reader, opts PutObjectOptions) (Object, error) {
	return s.chain.Around(ctx, "blobstore.PutObject", func(ctx context.Context) (Object, error) {
		if err := ValidateKey(key); err != nil {
			return Object{}, err
		}
		if err := validatePutObject(opts); err != nil {
			return Object{}, err
		}
		b := newPutBody(body, opts)
		var r io.Reader = b
		size := opts.ContentLength
		if size == 0 {
			chunks, n, ended, err := bufferPart(b, s3BufferLimit(opts.MaxBytes))
			switch {
			case err != nil || b.err != nil:
				// A body that broke a bound or failed to read is refused,
				// whatever bufferPart made of the error it saw.
				return Object{}, fmt.Errorf("blobstore: put %q in bucket %q: %w", key, s.bucket, b.failed(err))
			case ended:
				r, size = chunksReader(chunks), n
			default:
				r, size = io.MultiReader(chunksReader(chunks), b), -1
			}
		}
		info, err := s.api.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
			ContentType: opts.ContentType,
			PartSize:    s3PartSize,
		})
		if err != nil {
			if b.err != nil {
				// The body broke a bound or failed to read: that is the
				// cause, whatever the client wrapped it in.
				return Object{}, fmt.Errorf("blobstore: put %q in bucket %q: %w", key, s.bucket, b.err)
			}
			return Object{}, s.mapErr("put", key, err)
		}
		return b.object(key, opts.ContentType, info.ETag, info.LastModified), nil
	})
}

// mapErr turns the service's "no such key" into ErrNotFound and keeps
// everything else as a failure with its operator context.
func (s *S3) mapErr(op, key string, err error) error {
	resp := minio.ToErrorResponse(err)
	// A HEAD answers 404 with no body, so StatObject cannot always name the
	// code; a missing BUCKET is a misconfiguration, never "no such object".
	if resp.Code == minio.NoSuchKey || (resp.StatusCode == http.StatusNotFound && resp.Code != minio.NoSuchBucket) {
		return errorf(ErrNotFound, "%s %q in bucket %q", op, key, s.bucket)
	}
	return fmt.Errorf("blobstore: %s %q in bucket %q: %w", op, key, s.bucket, err)
}

func object(info minio.ObjectInfo) Object {
	return Object{
		Key:          info.Key,
		Size:         info.Size,
		ContentType:  info.ContentType,
		ETag:         info.ETag,
		LastModified: info.LastModified,
	}
}
