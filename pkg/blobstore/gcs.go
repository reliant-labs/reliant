// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/api/option/internaloption"

	"github.com/reliant-labs/forge/pkg/observe"
)

// GCSConfig configures NewGCS. Like S3Config, every field is a value the app
// reads from its own config; this package reads no environment of its own.
type GCSConfig struct {
	// Bucket is the bucket every key lives in. Required.
	Bucket string
	// AllowOpaqueKeys admits existing object names with path-like segments for
	// privileged, bucket-authorized readers. It applies only to reads/listing,
	// never writes or deletes. Scoped still enforces its strict tenant boundary.
	// Do not enable it for user-supplied tenant-relative keys.
	AllowOpaqueKeys bool
	// SignerEmail is the service account presigned URLs are signed as.
	// Empty means the client credential's own account: read from its JSON,
	// or under Workload Identity from the metadata server. Set it to sign
	// as another account (a dedicated reader), which the client's
	// credential needs roles/iam.serviceAccountTokenCreator on.
	SignerEmail string
	// SignBytes signs a URL's string-to-sign as SignerEmail. nil signs
	// keylessly through the IAM Credentials signBlob API under the client's
	// credentials when SignerEmail is explicit. With no explicit account,
	// the SDK may use its own credential's private key. Set it,
	// with SignerEmail, to sign without the network: a test, an emulator.
	SignBytes func([]byte) ([]byte, error)
	// ClientOptions are passed to storage.NewClient: credentials
	// (option.WithCredentials, an impersonated token source), an emulator
	// (option.WithEndpoint plus option.WithoutAuthentication). None means
	// Application Default Credentials, which is Workload Identity on GKE.
	// The client's own OpenTelemetry tracing covers its requests.
	ClientOptions []option.ClientOption
	// Logger receives the instrumentation's call records. nil: slog.Default.
	Logger *slog.Logger
	// Chain replaces the default instrumentation, as S3Config.Chain does.
	Chain *observe.ComponentChain
}

// GCS is a Store on Google Cloud Storage's native APIs: Stat, Delete and
// List through the JSON API, presigned URLs as V4 signed URLs on the XML
// API, signed without a stored key. Build it with NewGCS. Like S3 it has
// nothing to close: its client is HTTP (storage.NewClient, never gRPC), whose
// idle connections the transport reclaims.
type GCS struct {
	bucket          *storage.BucketHandle
	name            string
	signerEmail     string
	signBytes       func([]byte) ([]byte, error)
	iamSigner       *iamcredentials.Service
	clientOptions   []option.ClientOption
	allowOpaqueKeys bool
	chain           *observe.ComponentChain
}

var (
	_ Store             = (*GCS)(nil)
	_ LevelLister       = (*GCS)(nil)
	_ DownloadPresigner = (*GCS)(nil)
)

// NewGCS validates cfg and builds the store. Building the client resolves
// its credentials but calls no storage API: a wrong credential or a missing
// bucket surfaces on the first Stat, Delete or List, and a signer the
// account may not use on the first presign.
func NewGCS(ctx context.Context, cfg GCSConfig) (*GCS, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("blobstore: GCSConfig needs Bucket")
	}
	if cfg.SignBytes != nil && cfg.SignerEmail == "" {
		return nil, errors.New("blobstore: GCSConfig.SignBytes needs SignerEmail, the account it signs as")
	}
	// The new auth implementation preserves ADC discovery but performs token
	// provider HTTP on the request context rather than a cached OAuth context.
	opts := append([]option.ClientOption{internaloption.EnableNewAuthLibrary()}, cfg.ClientOptions...)
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("blobstore: building the Cloud Storage client: %w", err)
	}
	var iamSigner *iamcredentials.Service
	if cfg.SignerEmail != "" && cfg.SignBytes == nil {
		// An explicit principal must use IAM, not a private key belonging to
		// the client's ADC account. The storage SDK otherwise prefers that key.
		iamSigner, err = iamcredentials.NewService(ctx, opts...)
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("blobstore: building the IAM signer: %w", err)
		}
	}
	return &GCS{
		bucket:          client.Bucket(cfg.Bucket),
		iamSigner:       iamSigner,
		clientOptions:   opts,
		allowOpaqueKeys: cfg.AllowOpaqueKeys,
		name:            cfg.Bucket,
		signerEmail:     cfg.SignerEmail,
		signBytes:       cfg.SignBytes,
		chain:           instrumentation(cfg.Chain, cfg.Logger),
	}, nil
}

// signedURL signs a V4 URL for key. With no signer configured the client
// detects the account and signs through IAM signBlob.
func (g *GCS) signedURL(ctx context.Context, key, method string, d time.Duration, contentType string, headers []string, params url.Values) (PresignedRequest, error) {
	if err := ctx.Err(); err != nil {
		return PresignedRequest{}, err
	}
	signBytes := g.signBytes
	email := g.signerEmail
	var privateKey []byte
	signer := g.iamSigner
	if email == "" {
		var err error
		email, privateKey, err = g.defaultSigner(ctx)
		if err != nil {
			return PresignedRequest{}, err
		}
		if len(privateKey) == 0 {
			signer, err = iamcredentials.NewService(ctx, g.clientOptions...)
			if err != nil {
				return PresignedRequest{}, fmt.Errorf("blobstore: default IAM signer: %w", err)
			}
		}
	}
	if signer != nil {
		signBytes = func(payload []byte) ([]byte, error) {
			resp, err := signer.Projects.ServiceAccounts.SignBlob("projects/-/serviceAccounts/"+email,
				&iamcredentials.SignBlobRequest{Payload: base64.StdEncoding.EncodeToString(payload)}).Context(ctx).Do()
			if err != nil {
				return nil, err
			}
			return base64.StdEncoding.DecodeString(resp.SignedBlob)
		}
	}
	now := time.Now()
	u, err := g.bucket.SignedURL(key, &storage.SignedURLOptions{
		Scheme: storage.SigningSchemeV4,
		Method: method,
		// The library signs X-Goog-Expires as whole seconds from its own
		// clock reading, a moment after this one: the half second keeps
		// that exactly d (and still inside V4's 7 days + 1s at MaxTTL).
		Expires:         now.Add(d + time.Second/2),
		GoogleAccessID:  email,
		PrivateKey:      privateKey,
		SignBytes:       signBytes,
		ContentType:     contentType,
		Headers:         headers,
		QueryParameters: params,
	})
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("blobstore: sign %s %q in bucket %q: %w", method, key, g.name, err)
	}
	return PresignedRequest{Method: method, URL: u, ExpiresAt: signedExpiry(u, now.Add(d))}, nil
}

// signedExpiry is when a V4 URL stops working: its X-Goog-Date (whole
// seconds) plus X-Goog-Expires, which can be up to a second before the
// caller's now+TTL. fallback is used if the URL does not say.
func signedExpiry(signed string, fallback time.Time) time.Time {
	u, err := url.Parse(signed)
	if err != nil {
		return fallback
	}
	q := u.Query()
	date, err := time.Parse("20060102T150405Z", q.Get("X-Goog-Date"))
	if err != nil {
		return fallback
	}
	secs, err := strconv.Atoi(q.Get("X-Goog-Expires"))
	if err != nil {
		return fallback
	}
	return date.Add(time.Duration(secs) * time.Second)
}

// PresignPut implements Store. The URL signs the Content-Type and the exact
// Content-Length, so the service refuses any other body.
func (g *GCS) PresignPut(ctx context.Context, key string, opts PutOptions) (PresignedRequest, error) {
	return g.chain.Around(ctx, "blobstore.PresignPut", func(ctx context.Context) (PresignedRequest, error) {
		if err := ValidateKey(key); err != nil {
			return PresignedRequest{}, err
		}
		d, err := validatePut(opts)
		if err != nil {
			return PresignedRequest{}, err
		}
		req, err := g.signedURL(ctx, key, http.MethodPut, d, opts.ContentType,
			[]string{"Content-Length:" + strconv.FormatInt(opts.ContentLength, 10)}, nil)
		if err != nil {
			return PresignedRequest{}, err
		}
		req.Headers = map[string]string{"Content-Type": opts.ContentType}
		return req, nil
	})
}

// PresignGet implements Store.
func (g *GCS) PresignGet(ctx context.Context, key string, opts GetOptions) (PresignedRequest, error) {
	return g.chain.Around(ctx, "blobstore.PresignGet", func(ctx context.Context) (PresignedRequest, error) {
		if err := validateReadKey(key, g.allowOpaqueKeys); err != nil {
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
		return g.signedURL(ctx, key, http.MethodGet, d, "", nil, params)
	})
}

// PresignDownload implements DownloadPresigner.
func (g *GCS) PresignDownload(ctx context.Context, key string, opts DownloadOptions) (PresignedRequest, error) {
	return g.chain.Around(ctx, "blobstore.PresignDownload", func(ctx context.Context) (PresignedRequest, error) {
		if err := validateReadKey(key, g.allowOpaqueKeys); err != nil {
			return PresignedRequest{}, err
		}
		d, params, err := downloadParams(key, opts)
		if err != nil {
			return PresignedRequest{}, err
		}
		return g.signedURL(ctx, key, http.MethodGet, d, "", nil, params)
	})
}

// Stat implements Store.
func (g *GCS) Stat(ctx context.Context, key string) (Object, error) {
	return g.chain.Around(ctx, "blobstore.Stat", func(ctx context.Context) (Object, error) {
		if err := validateReadKey(key, g.allowOpaqueKeys); err != nil {
			return Object{}, err
		}
		attrs, err := g.bucket.Object(key).Attrs(ctx)
		if errors.Is(err, storage.ErrObjectNotExist) {
			return Object{}, errorf(ErrNotFound, "stat %q in bucket %q", key, g.name)
		}
		if err != nil {
			return Object{}, fmt.Errorf("blobstore: stat %q in bucket %q: %w", key, g.name, err)
		}
		return gcsObject(attrs), nil
	})
}

// Delete implements Store.
func (g *GCS) Delete(ctx context.Context, key string) error {
	return g.chain.Run(ctx, "blobstore.Delete", func(ctx context.Context) error {
		if err := ValidateKey(key); err != nil {
			return err
		}
		err := g.bucket.Object(key).Delete(ctx)
		if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
			return fmt.Errorf("blobstore: delete %q in bucket %q: %w", key, g.name, err)
		}
		return nil
	})
}

// gcsListAttrs are the object fields a listing asks for.
var gcsListAttrs = []string{"Name", "Size", "ContentType", "Etag", "Updated"}

func (g *GCS) query(prefix, delimiter, startOffset string) *storage.Query {
	q := &storage.Query{Prefix: prefix, Delimiter: delimiter, StartOffset: startOffset}
	// Every name is a valid attr, so this cannot fail.
	_ = q.SetAttrSelection(gcsListAttrs)
	return q
}

// List implements Store. GCS's startOffset is inclusive, so the cursor
// itself is skipped when it comes back.
func (g *GCS) List(ctx context.Context, prefix string, opts ListOptions) (ListPage, error) {
	return g.chain.Around(ctx, "blobstore.List", func(ctx context.Context) (ListPage, error) {
		if err := validateReadPrefix(prefix, g.allowOpaqueKeys); err != nil {
			return ListPage{}, err
		}
		limit := listLimit(opts.Limit)
		it := g.bucket.Objects(ctx, g.query(prefix, "", opts.After))
		// One past the page (two, with the inclusive cursor) answers in one
		// response whether another page exists.
		it.PageInfo().MaxSize = limit + 2
		var page ListPage
		for {
			attrs, err := it.Next()
			if errors.Is(err, iterator.Done) {
				return page, nil
			}
			if err != nil {
				return ListPage{}, fmt.Errorf("blobstore: list %q in bucket %q: %w", prefix, g.name, err)
			}
			if attrs.Name == opts.After {
				continue
			}
			if len(page.Objects) == limit {
				page.Next = page.Objects[limit-1].Key
				return page, nil
			}
			page.Objects = append(page.Objects, gcsObject(attrs))
		}
	})
}

// ListLevel implements LevelLister: one objects.list page with delimiter
// "/". Its page token wraps GCS's own pageToken.
func (g *GCS) ListLevel(ctx context.Context, prefix string, opts LevelOptions) (LevelPage, error) {
	return g.chain.Around(ctx, "blobstore.ListLevel", func(ctx context.Context) (LevelPage, error) {
		if err := validateReadPrefix(prefix, g.allowOpaqueKeys); err != nil {
			return LevelPage{}, err
		}
		token, err := decodePageToken("gcs", g.name, prefix, opts.PageToken)
		if err != nil {
			return LevelPage{}, err
		}
		var entries []*storage.ObjectAttrs
		next, err := iterator.NewPager(g.bucket.Objects(ctx, g.query(prefix, "/", "")), listLimit(opts.Limit), token).NextPage(&entries)
		if err != nil {
			var ge *googleapi.Error
			if token != "" && errors.As(err, &ge) && ge.Code == http.StatusBadRequest {
				return LevelPage{}, errorf(ErrInvalidPageToken, "list level %q in bucket %q: the service refused the page token: %v", prefix, g.name, err)
			}
			return LevelPage{}, fmt.Errorf("blobstore: list level %q in bucket %q: %w", prefix, g.name, err)
		}
		var page LevelPage
		for _, a := range entries {
			switch {
			case a.Prefix != "":
				page.Prefixes = append(page.Prefixes, a.Prefix)
			case isFolderPlaceholder(a.Name, prefix):
			default:
				page.Objects = append(page.Objects, gcsObject(a))
			}
		}
		if next != "" {
			page.Next = encodePageToken("gcs", g.name, prefix, next)
		}
		return page, nil
	})
}

func gcsObject(a *storage.ObjectAttrs) Object {
	return Object{
		Key:          a.Name,
		Size:         a.Size,
		ContentType:  a.ContentType,
		ETag:         a.Etag,
		LastModified: a.Updated,
	}
}
