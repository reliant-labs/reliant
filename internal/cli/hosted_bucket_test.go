package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// output.buckets (kcl/render.k _render_bucket) decodes into the env's hosted
// buckets, and the hosted group publishes each as a bucket-tier item carrying
// its ManagedBucket spec.
func TestHostedBucketsDecodeAndPublish(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"output": {
		"control_plane": {"type": "reliant", "endpoint": "https://api.example.com"},
		"buckets": [{"name": "uploads", "runtime": "hosted",
			"spec": {"publicRead": true, "corsOrigins": ["https://partner.example.com"], "deletionPolicy": "retain"}}]
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Buckets) != 1 || !e.Buckets[0].Hosted() {
		t.Fatalf("buckets = %+v, want one hosted bucket", e.Buckets)
	}
	if !e.HasHosted() {
		t.Error("an env with a hosted bucket is hosted")
	}
	group, err := buildHostedGroup("prod", e)
	if err != nil || group == nil {
		t.Fatalf("buildHostedGroup: %v (group %v)", err, group)
	}
	if len(group.Services) != 1 {
		t.Fatalf("services = %+v, want the bucket", group.Services)
	}
	s := group.Services[0]
	if s.Name != "uploads" || s.Hosted == nil || s.Hosted.Tier != deploytarget.HostedTierBucket || s.Hosted.Bucket == nil {
		t.Fatalf("service = %+v, want a bucket-tier uploads", s)
	}
	want := deployv1alpha1.ManagedBucketSpec{PublicRead: true, CORSOrigins: []string{"https://partner.example.com"}, DeletionPolicy: deployv1alpha1.DeletionPolicyRetain}
	if got := *s.Hosted.Bucket; got.PublicRead != want.PublicRead || got.DeletionPolicy != want.DeletionPolicy || len(got.CORSOrigins) != 1 || got.CORSOrigins[0] != want.CORSOrigins[0] {
		t.Errorf("bucket spec = %+v, want %+v", got, want)
	}
	if _, err := deploytarget.PreflightHosted(*group); err != nil {
		t.Errorf("a bucket-only hosted env must be admissible: %v", err)
	}
}
