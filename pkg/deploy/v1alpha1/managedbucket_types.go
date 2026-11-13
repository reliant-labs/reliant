package v1alpha1

import (
	"errors"
	"fmt"
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ManagedBucketSpec is an object-storage bucket the hosted platform
// provisions for one environment: a forge.Bucket bound to forge.OnHosted.
//
// The control plane's managedbucket operator owns everything behind it — the
// cloud bucket, a credential scoped to that bucket alone, and the Secret
// BucketCredentialSecretName(name) in the environment's namespace that a
// bucketRef env var reads. The bucket's real name is the platform's choice
// and is RECORDED in status, never declared: bucket names are global, so an
// author-chosen one could collide with, or name, someone else's.
//
// SCALARS AND A CLOSED LIST ONLY. There is no lifecycle-rule passthrough, no
// storage class, no location and no raw IAM: a field the platform would have
// to REJECT is a field it does not declare.
type ManagedBucketSpec struct {
	// PublicRead makes every object readable without a signature (avatars,
	// a public gallery). Off, objects are reached only through presigned
	// URLs, and the bucket's public access prevention stays enforced.
	// +optional
	PublicRead bool `json:"publicRead,omitempty"`

	// CORSOrigins are the browser origins that upload to and download from
	// the bucket directly, each scheme://host[:port] or "*". The platform
	// ADDS the environment's own hosted frontend origins and bound custom
	// domains, so an author never lists their own app here.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +listType=set
	CORSOrigins []string `json:"corsOrigins,omitempty"`

	// DeletionPolicy is what deleting this resource does to the objects.
	// Default retain, for the reason DeletionPolicy states: deleting a
	// declaration says nothing about whether the uploads should exist.
	// +optional
	// +kubebuilder:default=retain
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// MaxBucketCORSOrigins is the most origins a ManagedBucket may declare.
const MaxBucketCORSOrigins = 32

// corsOriginRE is one CORS origin: scheme://host[:port], no path. The same
// rule as kcl/schema.k Bucket.cors_origins.
var corsOriginRE = regexp.MustCompile(`^https?://[^/\s]+$`)

// WithDefaults fills every unset field.
func (s ManagedBucketSpec) WithDefaults() ManagedBucketSpec {
	if s.DeletionPolicy == "" {
		s.DeletionPolicy = DeletionPolicyRetain
	}
	return s
}

// Validate checks a ManagedBucket spec.
func (s ManagedBucketSpec) Validate() error {
	var errs []error
	d := s.WithDefaults()
	if d.DeletionPolicy != DeletionPolicyRetain && d.DeletionPolicy != DeletionPolicyDelete {
		errs = append(errs, fmt.Errorf("deletionPolicy %q must be retain or delete", s.DeletionPolicy))
	}
	if len(s.CORSOrigins) > MaxBucketCORSOrigins {
		errs = append(errs, fmt.Errorf("corsOrigins declares %d origins; at most %d", len(s.CORSOrigins), MaxBucketCORSOrigins))
	}
	seen := map[string]bool{}
	for _, o := range s.CORSOrigins {
		if o != "*" && !corsOriginRE.MatchString(o) {
			errs = append(errs, fmt.Errorf("corsOrigins %q must be an origin (scheme://host[:port], no path) or \"*\"", o))
		}
		if seen[o] {
			errs = append(errs, fmt.Errorf("corsOrigins lists %q twice", o))
		}
		seen[o] = true
	}
	return errors.Join(errs...)
}

// MaxBucketNameLength is the longest ManagedBucket metadata.name. The cloud
// bucket's name is the platform's prefix plus this name, and a cloud bucket
// name is at most 63 characters, so 40 leaves the prefix its room. An
// over-long name is refused, never truncated: two truncated names could
// address one bucket.
const MaxBucketNameLength = 40

// bucketNameRE is a ManagedBucket's name: lowercase letters, digits and
// hyphens, starting and ending with a letter or digit (kcl/schema.k
// Bucket.name, without the 63-character ceiling a self-managed bucket has).
var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ValidateBucketName checks a ManagedBucket's metadata.name: 3 to
// MaxBucketNameLength lowercase letters, digits and hyphens.
func ValidateBucketName(name string) error {
	if len(name) < 3 || len(name) > MaxBucketNameLength || !bucketNameRE.MatchString(name) {
		return fmt.Errorf("bucket name %q must be 3-%d lowercase letters, digits and hyphens, starting and ending with a letter or digit", name, MaxBucketNameLength)
	}
	return nil
}

// BucketCredentialKey selects one connection fact of a ManagedBucket: the
// fields of forge/pkg/blobstore's S3Config, and exactly the keys of the
// Secret BucketCredentialSecretName names (kcl/workload.k BUCKET_REF_KEYS).
//
// +kubebuilder:validation:Enum=endpoint;public_endpoint;region;bucket;access_key_id;secret_access_key;path_style
type BucketCredentialKey string

const (
	BucketKeyEndpoint        BucketCredentialKey = "endpoint"
	BucketKeyPublicEndpoint  BucketCredentialKey = "public_endpoint"
	BucketKeyRegion          BucketCredentialKey = "region"
	BucketKeyBucket          BucketCredentialKey = "bucket"
	BucketKeyAccessKeyID     BucketCredentialKey = "access_key_id"
	BucketKeySecretAccessKey BucketCredentialKey = "secret_access_key"
	BucketKeyPathStyle       BucketCredentialKey = "path_style"
)

// BucketCredentialKeys is every BucketCredentialKey, in the order
// forge.bucket_env emits them. The Secret the platform writes carries all of
// them.
var BucketCredentialKeys = []BucketCredentialKey{
	BucketKeyEndpoint, BucketKeyPublicEndpoint, BucketKeyRegion, BucketKeyBucket,
	BucketKeyAccessKeyID, BucketKeySecretAccessKey, BucketKeyPathStyle,
}

// Valid reports whether k is one of BucketCredentialKeys.
func (k BucketCredentialKey) Valid() bool {
	for _, v := range BucketCredentialKeys {
		if k == v {
			return true
		}
	}
	return false
}

// BucketRef names a ManagedBucket, in the workload's own namespace, and one
// connection fact of it to project. Like DatabaseRef it NAMES A BUCKET, NEVER
// A SECRET, so what it can resolve to is closed by construction.
type BucketRef struct {
	// Name is the ManagedBucket's name.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]*[a-z0-9]$`
	Name string `json:"name"`
	// Key is the connection fact. Required: unlike a database's uri, no
	// one fact of a bucket is the obvious default.
	Key BucketCredentialKey `json:"key"`
}

// Validate checks a bucket reference.
func (r BucketRef) Validate() error {
	var errs []error
	if err := ValidateBucketName(r.Name); err != nil {
		errs = append(errs, fmt.Errorf("bucketRef.name: %w", err))
	}
	if !r.Key.Valid() {
		errs = append(errs, fmt.Errorf("bucketRef.key %q must be one of endpoint, public_endpoint, region, bucket, access_key_id, secret_access_key, path_style", r.Key))
	}
	return errors.Join(errs...)
}

// ManagedBucketStatus is the observed state of a ManagedBucket.
type ManagedBucketStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// BucketName is the cloud bucket actually created. RECORDED rather than
	// re-derived, so a later change to the naming rule cannot re-point a
	// name that already holds objects.
	// +optional
	BucketName string `json:"bucketName,omitempty"`

	// ServiceAccount is the identity the credential belongs to: the one
	// principal granted object access on this bucket alone.
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// CredentialID is the id of the access key the Secret currently
	// carries (an HMAC access id; not secret). A rotation creates a second
	// key, moves the Secret to it, and only then retires this one.
	// +optional
	CredentialID string `json:"credentialID,omitempty"`

	// SecretName is the credential Secret, in the resource's namespace.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ReadyAt is nil until the bucket, its credential and the Secret have
	// all existed together at least once.
	// +optional
	ReadyAt *metav1.Time `json:"readyAt,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ManagedBucket is an object-storage bucket the hosted platform provisions.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mbk,categories=forge
// +kubebuilder:printcolumn:name="Public",type=boolean,JSONPath=`.spec.publicRead`
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucketName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ManagedBucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedBucketSpec   `json:"spec,omitempty"`
	Status ManagedBucketStatus `json:"status,omitempty"`
}

// ManagedBucketList contains a list of ManagedBucket.
//
// +kubebuilder:object:root=true
type ManagedBucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedBucket `json:"items"`
}
