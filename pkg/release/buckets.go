package release

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
)

// Where a bucket lives ([ShapeBucket.RuntimeKind]): the runtime its
// forge.Bucket declaration binds it to.
const (
	// BucketRuntimeMinIO is a bucket on the env's dev MinIO
	// (forge.BucketOnMinIO). Only a machine on the developer's loopback
	// reaches it, so it records no endpoint.
	BucketRuntimeMinIO = "minio"
	// BucketRuntimeS3 is an S3-compatible bucket forge does not manage
	// (forge.BucketOnS3): Bucket and Endpoint say where it is.
	BucketRuntimeS3 = "s3"
	// BucketRuntimeGCS is a GCS bucket forge does not manage, reached through
	// the JSON API rather than an HMAC key. Bucket is its name; Endpoint is
	// empty for storage.googleapis.com.
	BucketRuntimeGCS = "gcs"
	// BucketRuntimeHosted is a bucket Reliant hosting provisions
	// (forge.OnHosted). It records neither a cloud name nor an endpoint: the
	// platform derives the cloud bucket, and a reader resolves it from the
	// ManagedBucket's status, never by re-deriving it.
	BucketRuntimeHosted = "hosted"
)

// BucketRuntimeKinds is the closed set of [ShapeBucket.RuntimeKind], in a
// stable order.
var BucketRuntimeKinds = []string{BucketRuntimeMinIO, BucketRuntimeS3, BucketRuntimeGCS, BucketRuntimeHosted}

// ShapeBucket is one object-storage bucket the env declares (forge.Bucket),
// as a reader outside the app needs to find it: the control plane's storage
// browser lists these, and resolves each to a backend by RuntimeKind.
//
// NAMES ONLY. It never carries a credential: an S3 bucket's keys stay in the
// secrets its declaration references, a hosted bucket's in the Secret the
// platform writes. [Shape.Validate] refuses an endpoint with user info for
// the same reason.
type ShapeBucket struct {
	// Name is the forge.Bucket's name: what a BucketRef names it by, and the
	// key a reader looks it up by ([Shape.BucketNamed]).
	Name string `json:"name"`
	// RuntimeKind is one of [BucketRuntimeKinds].
	RuntimeKind string `json:"runtime_kind"`
	// PublicRead is forge.Bucket.public_read: every object is readable
	// without a signature.
	PublicRead bool `json:"public_read,omitempty"`
	// Bucket is the real bucket name, for the runtimes forge does not manage
	// (s3, gcs), resolved: the declared one, else Name. Empty for minio
	// (whose bucket is Name) and hosted (resolved from the ManagedBucket).
	Bucket string `json:"bucket,omitempty"`
	// Endpoint is the S3 API's base URL for an s3 bucket (and a gcs bucket
	// that is not on storage.googleapis.com), scheme included. Empty
	// otherwise.
	Endpoint string `json:"endpoint,omitempty"`
}

// BucketNamed returns the env's bucket called name, and whether it declares
// one.
func (s Shape) BucketNamed(name string) (ShapeBucket, bool) {
	for _, b := range s.Buckets {
		if b.Name == name {
			return b, true
		}
	}
	return ShapeBucket{}, false
}

var (
	// shapeBucketNameRE is forge.Bucket.name's rule (kcl/schema.k).
	shapeBucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	// shapeCloudBucketRE admits an S3 name (dots allowed) and a GCS one
	// (underscores, up to 222 characters with dots).
	shapeCloudBucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)
)

func (b ShapeBucket) validate() error {
	if !shapeBucketNameRE.MatchString(b.Name) {
		return fmt.Errorf("%w: shape bucket %q: name must be 3..63 lowercase letters, digits and hyphens", ErrInvalid, b.Name)
	}
	if !slices.Contains(BucketRuntimeKinds, b.RuntimeKind) {
		return fmt.Errorf("%w: shape bucket %q: runtime_kind %q (expected one of %v)", ErrInvalid, b.Name, b.RuntimeKind, BucketRuntimeKinds)
	}
	external := b.RuntimeKind == BucketRuntimeS3 || b.RuntimeKind == BucketRuntimeGCS
	switch {
	case external && !shapeCloudBucketRE.MatchString(b.Bucket):
		return fmt.Errorf("%w: shape bucket %q: a %s bucket names its real bucket, and %q is not a bucket name", ErrInvalid, b.Name, b.RuntimeKind, b.Bucket)
	case b.RuntimeKind == BucketRuntimeHosted && b.Bucket != "":
		return fmt.Errorf("%w: shape bucket %q: a hosted bucket records no cloud bucket name; a reader resolves it from the ManagedBucket", ErrInvalid, b.Name)
	case b.RuntimeKind == BucketRuntimeMinIO && b.Bucket != "":
		return fmt.Errorf("%w: shape bucket %q: a minio bucket records no bucket name; it is the bucket's name", ErrInvalid, b.Name)
	case b.RuntimeKind == BucketRuntimeS3 && b.Endpoint == "":
		return fmt.Errorf("%w: shape bucket %q: an s3 bucket names its endpoint", ErrInvalid, b.Name)
	case !external && b.Endpoint != "":
		return fmt.Errorf("%w: shape bucket %q: a %s bucket records no endpoint", ErrInvalid, b.Name, b.RuntimeKind)
	}
	if b.Endpoint != "" {
		u, err := url.Parse(b.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("%w: shape bucket %q: endpoint %q is not an http(s) base URL", ErrInvalid, b.Name, b.Endpoint)
		}
		// A shape is stored forever and shown to every member: a key in the
		// URL would be a credential recorded in it.
		if u.User != nil {
			return fmt.Errorf("%w: shape bucket %q: endpoint carries user info; a credential never belongs in a shape", ErrInvalid, b.Name)
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: shape bucket %q: endpoint %q must be a base URL, with no path, query or fragment", ErrInvalid, b.Name, b.Endpoint)
		}
	}
	return nil
}

func validateShapeBuckets(bs []ShapeBucket) error {
	seen := map[string]bool{}
	for _, b := range bs {
		if err := b.validate(); err != nil {
			return err
		}
		if seen[b.Name] {
			return fmt.Errorf("%w: shape names bucket %q twice", ErrInvalid, b.Name)
		}
		seen[b.Name] = true
	}
	return nil
}

// canonicalShapeBuckets sorts by name. Nil for none, so a shape that declares
// no bucket carries no key.
func canonicalShapeBuckets(in []ShapeBucket) []ShapeBucket {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
