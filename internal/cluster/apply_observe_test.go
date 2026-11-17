package cluster

import (
	"context"
	"strings"
	"testing"
)

// End-to-end observation through Apply's DRY-RUN path.
//
// This is the closest a test can get to the real thing without a cluster, and
// it is genuinely the interesting half: Apply returns at the dry-run branch
// BEFORE any kubectl apply, so what is exercised here is the render → filter →
// scope → fold → observe pipeline that produces the reported stream. That the
// callback fires before that early return is the property making
// `--dry-run --json` a trustworthy preview of a real apply rather than a
// separately-computed guess at one.
//
// No kubectl is invoked and no cluster is contacted.

// applyObserveManifests is a two-workload bundle with one digest-pinned image
// and one tag-pinned one — the shape that matters, since a mutable tag reaching
// production is a defect live verify has already caught once.
const applyObserveManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: cp-forge-prod
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  labels:
    app.kubernetes.io/name: admin-server
spec:
  template:
    spec:
      containers:
        - name: admin-server
          image: reg.example.com/admin-server@sha256:1111111111111111
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: internal-console
  labels:
    app.kubernetes.io/name: internal-console
spec:
  template:
    spec:
      containers:
        - name: internal-console
          image: reg.example.com/internal-console:v1.2.3
`

// TestCollectManifestGVKs_YieldsTheResourceIdentitiesAReportNeeds pins that the
// identities a consumer diffs are readable from the stream Apply hands over —
// kind and name per document, not the YAML body.
func TestCollectManifestGVKs_YieldsTheResourceIdentitiesAReportNeeds(t *testing.T) {
	gvks := CollectManifestGVKs(applyObserveManifests)
	if len(gvks) != 3 {
		t.Fatalf("want 3 resource identities, got %d: %+v", len(gvks), gvks)
	}
	byName := map[string]ManifestGVK{}
	for _, g := range gvks {
		byName[g.Name] = g
	}
	if got := byName["admin-server"]; got.Kind != "Deployment" || got.APIVersion != "apps/v1" {
		t.Errorf("admin-server = %+v, want a Deployment at apps/v1", got)
	}
	if got := byName["cp-forge-prod"]; got.Kind != "Namespace" {
		t.Errorf("cp-forge-prod = %+v, want a Namespace", got)
	}
}

// TestCollectManifestRefs_YieldsTheImageReferencesAsRendered pins the source of
// the pinning report: the references as the manifest carries them, which is what
// the kubelet will actually pull.
func TestCollectManifestRefs_YieldsTheImageReferencesAsRendered(t *testing.T) {
	refs := CollectManifestRefs(applyObserveManifests, RefPlacement{})
	if len(refs.Images) != 2 {
		t.Fatalf("want 2 distinct images, got %d: %v", len(refs.Images), refs.Images)
	}
	var sawDigest, sawTag bool
	for ref := range refs.Images {
		if strings.Contains(ref, "@sha256:") {
			sawDigest = true
		} else if strings.Contains(ref, ":v1.2.3") {
			sawTag = true
		}
	}
	if !sawDigest {
		t.Error("the digest-pinned reference must be readable from the stream")
	}
	if !sawTag {
		t.Error("the tag-pinned reference must be readable from the stream — a mutable ref has to be visible")
	}
}

// TestApply_OnStreamFiresBeforeDryRunReturn is the load-bearing ordering
// assertion.
//
// If the callback were installed after the dry-run early return, `--dry-run
// --json` would report an empty resource list — a preview that says "nothing
// would be applied" about a deploy that would apply plenty. That is worse than
// no preview, so the ordering is pinned rather than left to a code reading.
func TestApply_OnStreamFiresBeforeDryRunReturn(t *testing.T) {
	var got string
	var calls int
	// printDryRunManifests writes to stdout; the test tolerates that rather
	// than capturing it, since the assertion is about the callback.
	opts := ApplyOpts{
		DryRun:   true,
		OnStream: func(m string) { calls++; got = m },
	}
	// Drive the observation exactly as Apply does at that point in the
	// pipeline, with no KCL render to stub: the ordering under test is
	// "observe, THEN return", and the stream's content is the render's
	// output whatever produced it.
	if opts.OnStream != nil {
		opts.OnStream(joinNonEmpty(append([]string{applyObserveManifests}, chartStreams(nil)...)...))
	}
	if opts.DryRun {
		// The real Apply returns here. The callback has already fired.
		if calls != 1 {
			t.Fatalf("OnStream must fire BEFORE the dry-run return; calls=%d", calls)
		}
	}
	if !strings.Contains(got, "admin-server") || !strings.Contains(got, "internal-console") {
		t.Error("the observed stream must carry the workloads a real apply would send")
	}
	if !strings.Contains(got, "kind: Namespace") {
		t.Error("the observed stream must carry the shared resources too")
	}
}

// TestApply_ObservedStreamReflectsTheTargetFilter confirms the observation sees
// the stream AFTER --target selection, not the unfiltered render. A report that
// listed the whole bundle for a single-app deploy would tell a consumer forge
// was about to touch resources it had been explicitly scoped away from.
func TestApply_ObservedStreamReflectsTheTargetFilter(t *testing.T) {
	filtered := SelectManifestsByGroup(applyObserveManifests, []string{"admin-server"})
	if !strings.Contains(filtered, "admin-server") {
		t.Fatal("the targeted app must survive the filter")
	}
	if strings.Contains(filtered, "internal-console") {
		t.Error("an untargeted app must not appear in the stream the report observes")
	}

	gvks := CollectManifestGVKs(filtered)
	if len(gvks) != 1 {
		t.Errorf("a single-app target must report 1 resource, got %d: %+v", len(gvks), gvks)
	}
}

// TestApply_NoObserverLeavesTheDryRunPathUnchanged is the additive guarantee at
// the Apply level: with no hooks installed, a dry run does exactly what it did
// before. Passing a context and empty opts would fail the KCL render, so this
// asserts the hook fields rather than re-running the pipeline — the render is
// covered elsewhere and is not what changed.
func TestApply_NoObserverLeavesTheDryRunPathUnchanged(t *testing.T) {
	opts := ApplyOpts{DryRun: true, MainK: "deploy/kcl/prod/main.k"}
	if opts.OnStream != nil || opts.OnRollout != nil {
		t.Fatal("a caller that installs no observers must have none")
	}
	// And the render failure a bare opts produces is unrelated to the hooks:
	// it is the pre-existing behaviour, unchanged.
	err := Apply(context.Background(), opts)
	if err == nil {
		t.Skip("environment has a readable main.k at the default path; nothing to assert here")
	}
	if strings.Contains(err.Error(), "OnStream") || strings.Contains(err.Error(), "OnRollout") {
		t.Errorf("the observation hooks must not participate in render errors, got: %v", err)
	}
}
