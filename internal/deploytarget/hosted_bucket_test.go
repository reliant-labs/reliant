package deploytarget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// bucketGroup is hostedGroup plus a hosted bucket "uploads" that the api
// reads through bucketRef.
func bucketGroup() ServiceGroup {
	g := hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{})
	api := g.Services[0].Hosted.Workload
	api.Env = append(api.Env,
		v1alpha1.EnvVar{Name: "UPLOADS_BUCKET", BucketRef: &v1alpha1.BucketRef{Name: "uploads", Key: v1alpha1.BucketKeyBucket}},
		v1alpha1.EnvVar{Name: "UPLOADS_SECRET_ACCESS_KEY", BucketRef: &v1alpha1.BucketRef{Name: "uploads", Key: v1alpha1.BucketKeySecretAccessKey}},
	)
	g.Services = append(g.Services, ResolvedService{Name: "uploads", Hosted: &HostedWorkload{Tier: HostedTierBucket,
		Bucket: &v1alpha1.ManagedBucketSpec{CORSOrigins: []string{"https://partner.example.com"}}}})
	return g
}

// A hosted bucket rides the bundle as a forge.dev ManagedBucket: its spec with
// the defaults filled (retain), and like every record no namespace, no
// identity and no status.
func TestHostedRecordsPublishAManagedBucket(t *testing.T) {
	recs, err := HostedRecords(bucketGroup())
	if err != nil {
		t.Fatal(err)
	}
	var doc string
	for _, r := range recs {
		if r.Name == "uploads" {
			if r.Kind != "ManagedBucket" {
				t.Fatalf("uploads record kind = %q, want ManagedBucket", r.Kind)
			}
			doc = string(r.YAML)
		}
	}
	if doc == "" {
		t.Fatalf("no ManagedBucket record among %d", len(recs))
	}
	for _, want := range []string{"apiVersion: forge.dev/v1alpha1", "kind: ManagedBucket", "deletionPolicy: retain", "- https://partner.example.com"} {
		if !strings.Contains(doc, want) {
			t.Errorf("ManagedBucket record lacks %q:\n%s", want, doc)
		}
	}
	for _, forbidden := range []string{"namespace:", "status:", "forge.dev/org-id"} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("ManagedBucket record carries %q:\n%s", forbidden, doc)
		}
	}
}

// A bucketRef naming no ManagedBucket the env declares reads a Secret nothing
// will ever write, so the plan refuses it before anything is published.
func TestHostedRefusesABucketRefToAnUndeclaredBucket(t *testing.T) {
	g := bucketGroup()
	g.Services = g.Services[:2] // drop the bucket
	_, err := planHosted(g)
	if err == nil || !strings.Contains(err.Error(), `references a bucket the env does not declare (bucketRef "uploads"`) ||
		!strings.Contains(err.Error(), "forge-bucket-uploads") {
		t.Fatalf("planHosted = %v, want the dangling bucketRef refused", err)
	}
	if _, err := PreflightHosted(g); err == nil || !strings.Contains(err.Error(), "references a bucket the env does not declare") {
		t.Fatalf("PreflightHosted = %v, want the same refusal", err)
	}
}

// The plan validates the bucket like every other spec: an invalid one is
// refused before any RPC.
func TestHostedRefusesAnInvalidBucket(t *testing.T) {
	for name, mutate := range map[string]func(*ServiceGroup){
		"origin with a path": func(g *ServiceGroup) { g.Services[2].Hosted.Bucket.CORSOrigins = []string{"https://a.example.com/x"} },
		"deletion policy":    func(g *ServiceGroup) { g.Services[2].Hosted.Bucket.DeletionPolicy = "purge" },
		"name too long":      func(g *ServiceGroup) { g.Services[2].Name = strings.Repeat("u", v1alpha1.MaxBucketNameLength+1) },
		"no spec":            func(g *ServiceGroup) { g.Services[2].Hosted.Bucket = nil },
	} {
		t.Run(name, func(t *testing.T) {
			g := bucketGroup()
			mutate(&g)
			if _, err := planHosted(g); err == nil || !strings.Contains(err.Error(), "refusing to publish anything") {
				t.Fatalf("planHosted = %v, want a refusal", err)
			}
		})
	}
}

// A bucket is never release-bound: no artifact, and a database-and-bucket
// env needs no release.
func TestHostedBucketIsNotReleaseBound(t *testing.T) {
	b := &HostedWorkload{Tier: HostedTierBucket, Artifact: "uploads", Bucket: &v1alpha1.ManagedBucketSpec{}}
	if got := hostedArtifactOf("uploads", b); got != "" {
		t.Errorf("hostedArtifactOf(bucket) = %q, want \"\" (never release-bound)", got)
	}
	g := ServiceGroup{Services: []ResolvedService{{Name: "uploads", Hosted: b}}}
	if hostedGroupHasPinnedArtifact(g) {
		t.Error("a bucket-only group must need no release")
	}
}

// The control plane keeps no deployment row for a ManagedBucket, so the
// readiness wait observes only the workload and the database. Waiting on the
// bucket would wait for a status nobody reports, so it is reported not_waited.
func TestHostedDeployDoesNotWaitOnABucket(t *testing.T) {
	cp := &fakeCP{status: readyStatus(digestA)}
	var outcomes []cluster.RolloutObservation
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		RecordBundle: func(context.Context, string) (string, error) { return "", nil },
		OnRollout:    func(o cluster.RolloutObservation) { outcomes = append(outcomes, o) }}
	if err := p.Deploy(context.Background(), bucketGroup()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("rollout outcomes = %+v, want the api and orders ready and the bucket not waited", outcomes)
	}
	for _, o := range outcomes {
		want := cluster.RolloutStateReady
		if o.Name == "uploads" {
			// Reported, never silently absent: nothing observed it.
			want = cluster.RolloutStateNotWaited
			if o.Kind != "ManagedBucket" {
				t.Errorf("bucket outcome %+v, want kind ManagedBucket", o)
			}
		}
		if o.State != want {
			t.Errorf("outcome %+v, want %v", o, want)
		}
	}
}
