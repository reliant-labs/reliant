package v1alpha1

import (
	"strings"
	"testing"
)

func TestManagedBucketValidateAndDefaults(t *testing.T) {
	d := ManagedBucketSpec{}.WithDefaults()
	if d.DeletionPolicy != DeletionPolicyRetain || d.PublicRead {
		t.Fatalf("defaults = %+v; retain MUST be the deletion default and objects private", d)
	}
	if err := (ManagedBucketSpec{}).Validate(); err != nil {
		t.Fatalf("an empty spec must validate via defaults: %v", err)
	}
	if err := (ManagedBucketSpec{CORSOrigins: []string{"https://app.acme.com", "http://localhost:5173", "*"}}).Validate(); err != nil {
		t.Fatal(err)
	}
	mustFail(t, ManagedBucketSpec{DeletionPolicy: "purge"}.Validate(), "deletionPolicy")
	mustFail(t, ManagedBucketSpec{CORSOrigins: []string{"https://app.acme.com/path"}}.Validate(), "no path")
	mustFail(t, ManagedBucketSpec{CORSOrigins: []string{"app.acme.com"}}.Validate(), "origin")
	mustFail(t, ManagedBucketSpec{CORSOrigins: []string{"https://a.com", "https://a.com"}}.Validate(), "twice")
	many := make([]string, MaxBucketCORSOrigins+1)
	for i := range many {
		many[i] = "https://a" + strings.Repeat("b", i) + ".com"
	}
	mustFail(t, ManagedBucketSpec{CORSOrigins: many}.Validate(), "at most 32")
}

func TestValidateBucketName(t *testing.T) {
	for _, ok := range []string{"uploads", "a-1", strings.Repeat("a", MaxBucketNameLength)} {
		if err := ValidateBucketName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	// 41 characters would leave the platform's prefix no room inside a
	// 63-character cloud bucket name; refused, never truncated.
	for _, bad := range []string{"ab", "Uploads", "-up", "up-", "up.loads", "up_loads", strings.Repeat("a", MaxBucketNameLength+1)} {
		mustFail(t, ValidateBucketName(bad), "bucket name")
	}
}

// TestEnvVarBucketRef: bucketRef is a channel of its own — validated, counted
// against "one channel per entry", and allowed on the restricted profile.
func TestEnvVarBucketRef(t *testing.T) {
	ok := EnvVar{Name: "UPLOADS_BUCKET", BucketRef: &BucketRef{Name: "uploads", Key: BucketKeyBucket}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, k := range BucketCredentialKeys {
		if err := (EnvVar{Name: "X", BucketRef: &BucketRef{Name: "uploads", Key: k}}).Validate(); err != nil {
			t.Errorf("key %s: %v", k, err)
		}
	}
	mustFail(t, EnvVar{Name: "X", BucketRef: &BucketRef{Name: "uploads"}}.Validate(), "bucketRef.key")
	mustFail(t, EnvVar{Name: "X", BucketRef: &BucketRef{Name: "uploads", Key: "uri"}}.Validate(), "bucketRef.key")
	mustFail(t, EnvVar{Name: "X", BucketRef: &BucketRef{Name: "Up", Key: BucketKeyRegion}}.Validate(), "bucketRef.name")
	mustFail(t, EnvVar{Name: "X", Value: "v", BucketRef: &BucketRef{Name: "uploads", Key: BucketKeyRegion}}.Validate(), "more than one")

	w := WorkloadSpec{Kind: KindService, Image: "ghcr.io/acme/api:v1", Env: []EnvVar{ok}}
	if err := w.Validate(ProfileRestricted); err != nil {
		t.Fatalf("a bucketRef must be admissible on the hosted (restricted) profile: %v", err)
	}
}
