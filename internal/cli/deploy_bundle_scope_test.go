package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// bundleScopeEntities is control-plane prod's shape in miniature: cluster
// workloads and a database, plus a frontend shipped to Firebase Hosting.
func bundleScopeEntities() *KCLEntities {
	return &KCLEntities{
		Workloads: []WorkloadEntity{{Name: "admin-server"}, {Name: "otel-gateway"}},
		Databases: []DatabaseEntity{{Name: "control-plane-db"}},
		Frontends: []FrontendEntity{
			{Name: "reliant-web", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}},
			{Name: "docs", Runtime: FrontendRuntime{Type: FrontendRuntimeBucket}},
			// A frontend served FROM the cluster renders cluster objects.
			{Name: "admin-web", Runtime: FrontendRuntime{Type: FrontendRuntimeBuildOnly}},
			// A shipped frontend whose name is also a workload's.
			{Name: "admin-server", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}},
		},
	}
}

// 2026-10-19, control-plane prod: `forge env deploy prod <v> --frontends-only`
// from a checkout newer than the release refused with "release <v>'s bundle
// for env prod is already recorded, and this checkout renders DIFFERENT
// objects" — sixteen workloads the deploy would never touch. A deploy that
// applies no bundle object must not render, record or compare the bundle, and
// must not plan against it.
func TestFrontendsOnlyReleaseDeployDoesNotRenderOrCompareTheBundle(t *testing.T) {
	dir := t.TempDir()
	ledger := testLedger(t, dir)
	ledger.Hosted = true
	if _, err := testReleases(t, dir).Cut(context.Background(), release.Release{Version: "v1", Artifacts: map[string]release.Artifact{
		"ghcr.io/acme/admin-server": {Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)}},
	}}); err != nil {
		t.Fatalf("cut: %v", err)
	}

	prevRender := renderEntitiesForScope
	renderEntitiesForScope = func(context.Context, string, string) (*KCLEntities, error) { return bundleScopeEntities(), nil }
	writes := 0
	prevWrite := writeBundlesFn
	// What a newer checkout's render did: differ from the bundle of record.
	writeBundlesFn = func(context.Context, string, []string, bundleBuildInputs) ([]bundleWriteOutcome, error) {
		writes++
		return nil, &releaseBundleDiffersError{msg: "release v1's bundle for env prod is already recorded, and this checkout renders DIFFERENT objects"}
	}
	t.Cleanup(func() { renderEntitiesForScope, writeBundlesFn = prevRender, prevWrite })

	scope, err := resolveDeployScope(context.Background(), dir, "prod", nil, true)
	if err != nil {
		t.Fatalf("resolveDeployScope: %v", err)
	}
	plan, err := releaseBundleAndPlan(context.Background(), releaseBundleStep{ProjectDir: dir, Env: "prod", Version: "v1", Ledger: ledger, ErrOut: io.Discard}, scope)
	if err != nil {
		t.Fatalf("--frontends-only was refused over the bundle it does not apply: %v", err)
	}
	if writes != 0 || plan != nil {
		t.Fatalf("--frontends-only rendered the bundle %d time(s) and planned %v; want neither", writes, plan)
	}

	// The whole-env deploy of the same release still meets the refusal.
	whole, _ := resolveDeployScope(context.Background(), dir, "prod", nil, false)
	_, err = releaseBundleAndPlan(context.Background(), releaseBundleStep{ProjectDir: dir, Env: "prod", Version: "v1", Ledger: ledger, ErrOut: io.Discard}, whole)
	var differs *releaseBundleDiffersError
	if !errors.As(err, &differs) || writes != 1 {
		t.Fatalf("an unscoped release deploy must still render and compare the bundle: writes=%d err=%v", writes, err)
	}
}

// A --target naming only frontends shipped to a static host applies no
// bundle object either; any target that renders cluster objects keeps the
// check.
func TestTargetScopeAppliesBundleUnlessItNamesOnlyShippedFrontends(t *testing.T) {
	prev := renderEntitiesForScope
	renderEntitiesForScope = func(context.Context, string, string) (*KCLEntities, error) { return bundleScopeEntities(), nil }
	t.Cleanup(func() { renderEntitiesForScope = prev })

	for _, tc := range []struct {
		targets []string
		want    bool
	}{
		{[]string{"reliant-web"}, false},
		{[]string{"reliant-web", "docs"}, false},
		{[]string{"admin-server"}, true},                // a workload, and a frontend's name too
		{[]string{"reliant-web", "otel-gateway"}, true}, // one cluster workload is enough
		{[]string{"admin-web"}, true},                   // not shipped to a static host
		{[]string{"control-plane-db"}, true},
		{[]string{"something-raw"}, true}, // unknown: may name a raw manifest group
	} {
		scope, err := resolveDeployScope(context.Background(), t.TempDir(), "prod", tc.targets, false)
		if err != nil {
			t.Fatalf("%v: %v", tc.targets, err)
		}
		if scope.AppliesBundle != tc.want {
			t.Errorf("--target %v: AppliesBundle = %v, want %v", tc.targets, scope.AppliesBundle, tc.want)
		}
	}

	// A declaration that does not render is assumed to apply bundle objects.
	renderEntitiesForScope = func(context.Context, string, string) (*KCLEntities, error) { return nil, errors.New("boom") }
	if scope, err := resolveDeployScope(context.Background(), t.TempDir(), "prod", []string{"reliant-web"}, false); err != nil || !scope.AppliesBundle {
		t.Fatalf("unrendered declaration: scope=%+v err=%v; want AppliesBundle and no error", scope, err)
	}
}
