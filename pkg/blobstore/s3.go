// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
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
	bucket string
	// api performs Stat, Delete and List against Endpoint; presign signs
	// URLs for PublicEndpoint. They are the same client when the two match.
	api, presign *minio.Client
	chain        *observe.ComponentChain
}

var _ Store = (*S3)(nil)

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
	api, err := newMinioClient(cfg, cfg.Endpoint, transport)
	if err != nil {
		return nil, fmt.Errorf("blobstore: S3Config.Endpoint: %w", err)
	}
	presign := api
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		if presign, err = newMinioClient(cfg, cfg.PublicEndpoint, transport); err != nil {
			return nil, fmt.Errorf("blobstore: S3Config.PublicEndpoint: %w", err)
		}
	}
	chain := cfg.Chain
	if chain == nil {
		logger := cfg.Logger
		if logger == nil {
			logger = slog.Default()
		}
		chain = observe.NewComponentChain(
			observe.RecoverMiddleware(logger),
			observe.TraceMiddleware(otel.Tracer(scope)),
			observe.MetricsMiddleware(otel.Meter(scope), "blobstore"),
			// A missing object is the answer a 404 is built from, not a failure.
			observe.LogMiddleware(logger, slog.LevelDebug, observe.WithExpectedErrors(ErrNotFound)),
		)
	}
	return &S3{bucket: cfg.Bucket, api: api, presign: presign, chain: chain}, nil
}

// newMinioClient builds a client for endpoint, a base URL with scheme.
func newMinioClient(cfg S3Config, endpoint string, transport http.RoundTripper) (*minio.Client, error) {
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
		if err := ValidateKey(key); err != nil {
			return PresignedRequest{}, err
		}
		d, err := ttl(opts.TTL)
		if err != nil {
			return PresignedRequest{}, err
		}
		var params url.Values
		if opts.DownloadName != "" {
			params = url.Values{"response-content-disposition": {contentDisposition(opts.DownloadName)}}
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
		if err := ValidateKey(key); err != nil {
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
		if err := validatePrefix(prefix); err != nil {
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
