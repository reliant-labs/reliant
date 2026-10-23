package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// cutHostedFixtureRelease cuts a release pinning every hosted artifact of the
// golden hosted env, with NO promotion bound — the state of a first deploy.
func cutHostedFixtureRelease(t *testing.T, dir string, e *KCLEntities, version string) {
	t.Helper()
	group, err := buildHostedGroup("prod", e)
	if err != nil || group == nil {
		t.Fatalf("buildHostedGroup: %v", err)
	}
	artifacts := map[string]release.Artifact{}
	for _, svc := range group.Services {
		w := svc.Hosted
		if w != nil && (w.Tier == deploytarget.HostedTierWorkload || w.Tier == deploytarget.HostedTierStatic) {
			artifacts[deploytarget.HostedArtifactKey(svc)] = release.Artifact{
				Kind: release.KindOCI, Mode: release.ModeShared,
				Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)},
			}
		}
	}
	if _, err := testReleases(t, dir).Cut(context.Background(), release.Release{Version: version, Artifacts: artifacts}); err != nil {
		t.Fatalf("cut release: %v", err)
	}
}

// A first hosted deploy has a release but no promotion. The bundle is pinned
// by the release it names, so it is never sealed over placeholder digests.
func TestHostedBundleObjectsPinToTheNamedReleaseBeforeAnyPromotion(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	dir := t.TempDir()
	cutHostedFixtureRelease(t, dir, e, "v0.1.0")

	_, placeholder, err := hostedBundleObjects(context.Background(), dir, "prod", e)
	if err != nil || !placeholder {
		t.Fatalf("without a named release: placeholder=%v err=%v; want placeholder", placeholder, err)
	}
	ctx := withHostedPinRelease(context.Background(), "v0.1.0")
	objs, placeholder, err := hostedBundleObjects(ctx, dir, "prod", e)
	if err != nil {
		t.Fatalf("hostedBundleObjects: %v", err)
	}
	if placeholder || len(objs) == 0 {
		t.Fatalf("pinned by v0.1.0: placeholder=%v objects=%d; want a real pin", placeholder, len(objs))
	}
}

// Deploying a named release that has no bundle yet projects and records one.
func TestEnsureHostedReleaseBundleWritesWhenMissing(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	dir := t.TempDir()
	cutHostedFixtureRelease(t, dir, e, "v0.1.0")
	ledger := testLedger(t, dir)
	ledger.Hosted = true

	var got []bundleBuildInputs
	prev := writeBundlesFn
	writeBundlesFn = func(_ context.Context, _ string, _ []string, in bundleBuildInputs) ([]bundleWriteOutcome, error) {
		got = append(got, in)
		return nil, nil
	}
	t.Cleanup(func() { writeBundlesFn = prev })

	ensureHostedReleaseBundle(context.Background(), dir, "prod", "v0.1.0", ledger, false, io.Discard)
	if len(got) != 1 || got[0].Release != "v0.1.0" || len(got[0].Pins.Images) == 0 {
		t.Fatalf("writeBundles calls = %+v; want one for v0.1.0 carrying the release's pins", got)
	}

	ensureHostedReleaseBundle(context.Background(), dir, "prod", "v9", ledger, false, io.Discard)
	if len(got) != 1 {
		t.Fatalf("an uncut release must not write a bundle; calls = %d", len(got))
	}
}

// A hosted deploy plans against the bundle forge recorded in this same
// command: the machine ledger never holds a hosted env's bundle.
func TestHostedDeployPlansAgainstTheBundleJustRecorded(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	dir := t.TempDir()
	cutHostedFixtureRelease(t, dir, e, "v1.4.0")
	ledger := testLedger(t, dir)
	ledger.Hosted = true

	plan := buildTestPlan(t)
	var bundle map[string]any
	if err := json.Unmarshal([]byte(cpBundleFixture), &bundle); err != nil {
		t.Fatal(err)
	}
	digest, _ := bundle["digest"].(string)
	fake := &fakeDSOTCaller{replies: map[string]any{
		procGetBundle:  map[string]any{"bundle": bundle},
		procPlanDeploy: map[string]any{"plan": planToWireFixture(plan)},
	}, reply: func(proc string, _ map[string]any) (any, bool) {
		if strings.HasSuffix(proc, "/ListEnvironments") {
			return map[string]any{"environments": []map[string]string{{"id": "env_prod", "name": "prod"}}}, true
		}
		return nil, false
	}}
	prevStore := hostedRecordStoreForDeploy
	hostedRecordStoreForDeploy = func(context.Context, string, string) (hostedRecordStore, error) {
		return hostedRecordStoreFor(fake, "proj"), nil
	}
	prevWrite := writeBundlesFn
	writeBundlesFn = func(_ context.Context, _ string, _ []string, in bundleBuildInputs) ([]bundleWriteOutcome, error) {
		return []bundleWriteOutcome{{Env: "prod", Digest: digest, Pushed: true, Recorded: true}}, nil
	}
	prevCharts := observeDeployCharts
	observeDeployCharts = func(context.Context, string, string, []string) release.ChartPlan { return release.ChartPlan{} }
	t.Cleanup(func() {
		hostedRecordStoreForDeploy, writeBundlesFn, observeDeployCharts = prevStore, prevWrite, prevCharts
	})

	ensureHostedReleaseBundle(context.Background(), dir, "prod", "v1.4.0", ledger, false, io.Discard)
	got := planForDeploy(context.Background(), dir, "prod", "v1.4.0", nil, ledger, io.Discard)
	if got == nil {
		t.Fatal("no plan computed for a hosted deploy whose bundle was just recorded")
	}
	if got.Digest != plan.Digest {
		t.Errorf("plan digest = %q, want %q", got.Digest, plan.Digest)
	}
	wantFields(t, fake.body(t, procGetBundle), map[string]any{"digest": digest})
	wantFields(t, fake.body(t, procPlanDeploy), map[string]any{"bundleId": "bnd_1"})
}

// A bundle in the MACHINE ledger says nothing about a hosted env: it was
// rendered at cut time, usually under a different declaration, so its digest
// is not the control plane's. ensureHostedReleaseBundle used to treat it as
// "already recorded" and skip, so the plan looked up a digest the control
// plane had never seen and the first hosted deploy of the release was blind.
func TestEnsureHostedReleaseBundleWritesEvenWhenTheMachineLedgerHoldsOne(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	dir := t.TempDir()
	cutHostedFixtureRelease(t, dir, e, "v0.1.0")
	ledger := testLedger(t, dir)
	ledger.Hosted = true
	if _, _, err := testRecordStore(t, dir).RecordBundle(context.Background(), release.BundleRecord{
		Env: "prod", Release: "v0.1.0", Digest: sha("machine-only-bundle"),
		Reference: "ghcr.io/acme/b@" + sha("machine-only-bundle"), ConfigDigest: sha("cfg"), Shape: testShape(),
	}, testBundleBlobs()); err != nil {
		t.Fatalf("seed the machine ledger's bundle: %v", err)
	}

	calls := 0
	prev := writeBundlesFn
	writeBundlesFn = func(context.Context, string, []string, bundleBuildInputs) ([]bundleWriteOutcome, error) {
		calls++
		return nil, nil
	}
	t.Cleanup(func() { writeBundlesFn = prev })

	ensureHostedReleaseBundle(context.Background(), dir, "prod", "v0.1.0", ledger, false, io.Discard)
	if calls != 1 {
		t.Fatalf("writeBundles calls = %d; a machine-ledger bundle must not stand in for the control plane's, want 1", calls)
	}
}
