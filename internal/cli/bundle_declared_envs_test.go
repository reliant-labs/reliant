package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// stubDeclaredEnvRender makes the catalog's renders answer from a table, so
// these tests need no KCL project — only the deploy/kcl/<env>/main.k files the
// directory scan reads.
func stubDeclaredEnvRender(t *testing.T, render func(ctx context.Context, env string) (*KCLEntities, error)) {
	t.Helper()
	prev := declaredEnvRenderFn
	declaredEnvRenderFn = func(ctx context.Context, _, env string) (*KCLEntities, error) { return render(ctx, env) }
	t.Cleanup(func() { declaredEnvRenderFn = prev })
}

func TestComputeDeclaredEnvCatalog(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []string{"prod", "dev", "broken", "Weird Name", "site"} {
		declareEnvDir(t, dir, env)
	}
	stubDeclaredEnvRender(t, func(_ context.Context, env string) (*KCLEntities, error) {
		switch env {
		case "prod":
			return &KCLEntities{
				ControlPlane: &ControlPlaneEntity{},
				Workloads:    []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "gke"}}}},
			}, nil
		case "dev":
			return &KCLEntities{
				Lifecycle: "local",
				Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCompose}}},
			}, nil
		case "site":
			// A lifecycle outside the vocabulary reads as unset.
			return &KCLEntities{Lifecycle: "someday"}, nil
		}
		return nil, errors.New("open /Users/me/proj/deploy/kcl/broken/main.k: syntax error")
	})

	var notes bytes.Buffer
	got := computeDeclaredEnvCatalog(context.Background(), dir, &notes)
	want := []release.DeclaredEnv{
		{Name: "broken", RenderError: release.DeclaredEnvRenderFailed},
		{Name: "dev", Lifecycle: "local", Destination: "compose"},
		{Name: "prod", Destination: "cluster", ControlPlaneKind: string(release.EnvSelfManaged)},
		{Name: "site", Destination: "host"},
	}
	if len(got) != len(want) {
		t.Fatalf("catalog = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("catalog[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if err := release.ValidateDeclaredEnvs(got); err != nil {
		t.Fatalf("the computed catalog must validate: %v", err)
	}
	if !strings.Contains(notes.String(), "Weird Name") {
		t.Errorf("a directory that is not an env name must be noted, not silently dropped:\n%s", notes.String())
	}
	for _, e := range got {
		if strings.Contains(e.RenderError, "/") {
			t.Errorf("a render error must be a code, never the error text: %q", e.RenderError)
		}
	}
}

// A render that does not finish within the budget is listed by name as
// render_timeout; the build does not wait for it.
func TestComputeDeclaredEnvCatalog_Timeout(t *testing.T) {
	dir := t.TempDir()
	declareEnvDir(t, dir, "fast")
	declareEnvDir(t, dir, "slow")
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	stubDeclaredEnvRender(t, func(_ context.Context, env string) (*KCLEntities, error) {
		if env == "slow" {
			<-unblock // ignores ctx, as a native render does
		}
		return &KCLEntities{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := computeDeclaredEnvCatalog(ctx, dir, &bytes.Buffer{})
	if time.Since(start) > 10*time.Second {
		t.Fatal("the catalog waited on a render past its budget")
	}
	if len(got) != 2 || got[0].Name != "fast" || got[0].RenderError != "" || got[1].Name != "slow" || got[1].RenderError != "render_timeout" {
		t.Fatalf("catalog = %+v", got)
	}
}

func TestComputeDeclaredEnvCatalog_NoEnvs(t *testing.T) {
	if got := computeDeclaredEnvCatalog(context.Background(), t.TempDir(), &bytes.Buffer{}); got != nil {
		t.Fatalf("a project with no envs has no catalog, got %+v", got)
	}
}

func TestLazyDeclaredEnvCatalogComputesOnce(t *testing.T) {
	var calls atomic.Int32
	prev := declaredEnvCatalogFn
	declaredEnvCatalogFn = func(context.Context, string, io.Writer) []release.DeclaredEnv {
		calls.Add(1)
		return []release.DeclaredEnv{{Name: "prod"}}
	}
	t.Cleanup(func() { declaredEnvCatalogFn = prev })

	get := lazyDeclaredEnvCatalog(context.Background(), t.TempDir(), &bytes.Buffer{})
	if calls.Load() != 0 {
		t.Fatal("the catalog must not be computed until a bundle needs it")
	}
	for i := 0; i < 3; i++ {
		if got := get(); len(got) != 1 {
			t.Fatalf("catalog = %+v", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("one build renders every env at most once; computed %d times", calls.Load())
	}
}

func TestSealsDeclaredEnvs(t *testing.T) {
	all := &release.BundleReader{Features: release.BundleDocFeatures()}
	old := &release.BundleReader{Features: []string{release.BundleFeatureObjectSummaries}}
	hosted, files := envLedger{Hosted: true}, envLedger{}
	for name, tc := range map[string]struct {
		release string
		ledger  envLedger
		reader  *release.BundleReader
		want    bool
	}{
		"released, hosted, reader names it": {"v1", hosted, all, true},
		"unreleased":                        {"", hosted, all, false},
		"file ledger (local digests stay)":  {"v1", files, nil, false},
		"control plane predating it":        {"v1", hosted, old, false},
	} {
		if got := sealsDeclaredEnvs(tc.release, tc.ledger, tc.reader); got != tc.want {
			t.Errorf("%s: sealsDeclaredEnvs = %v, want %v", name, got, tc.want)
		}
	}
}

func TestWithShapeWorkloadUnits(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "gke"}}},
		{Name: "worker", DeliveryUnit: "jobs", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "gke"}}},
		{Name: "web", Runtime: RuntimeEntity{Type: RuntimeHosted}},
	}}

	if ws := withShapeWorkloadUnits(shapeWorkloadsOf(e), e, unitSplit{}); ws[0].Unit != "" || ws[1].Unit != "" {
		t.Fatalf("an env with no unit split records no unit (its shape must not change): %+v", ws)
	}

	split := unitSplit{Hub: []string{"jobs"}, Forge: []string{release.DefaultDeliveryUnit}, Source: unitSplitDeclared}
	ws := withShapeWorkloadUnits(shapeWorkloadsOf(e), e, split)
	if ws[0].Unit != release.DefaultDeliveryUnit || ws[1].Unit != "jobs" {
		t.Errorf("cluster workloads must name their unit (default when unset): %+v", ws)
	}
	if ws[2].Unit != "" {
		t.Errorf("a hosted workload is in no delivery unit: %+v", ws[2])
	}
}
