package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cluster"
)

func TestPlatformPrerequisitesRenderHonorsProjectDir(t *testing.T) {
	clearProjectDir(t)
	root := writeProjectDirFixture(t, "bootstrap-project")
	t.Chdir(t.TempDir())
	if err := cmdutil.SetProjectDir(root); err != nil {
		t.Fatal(err)
	}
	mainK := filepath.Join(root, "deploy", "kcl", "proof", "main.k")
	if err := os.MkdirAll(filepath.Dir(mainK), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainK, []byte("# fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRender := renderPlatformPrerequisiteManifestsFn
	t.Cleanup(func() { renderPlatformPrerequisiteManifestsFn = oldRender })
	called := false
	renderPlatformPrerequisiteManifestsFn = func(_ context.Context, path, imageTag, namespace, env string, _, _ map[string]string) (string, error) {
		called = true
		if path != mainK || env != "proof" || namespace != "bootstrap-project-proof" || imageTag != "dev" {
			t.Fatalf("unexpected render inputs: %q %q %q %q", path, imageTag, namespace, env)
		}
		return "rendered", nil
	}
	got, err := renderPlatformPrerequisitesFn(context.Background(), "proof", &KCLEntities{ImageTag: "dev"})
	if err != nil || got != "rendered" || !called {
		t.Fatalf("render = %q, %v, called=%v", got, err, called)
	}
}

func TestPlatformPrerequisitesBeforeChartsAcrossClusters(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "apply-failure"}[fail], func(t *testing.T) {
			captureChartInstalls(t)
			oldApply := applyPlatformPrerequisitesFn
			t.Cleanup(func() { applyPlatformPrerequisitesFn = oldApply })
			e, clusters := platformTestEnv()
			clusters = append(clusters, ClusterEntity{Name: "b", Context: "k3d-b"}, ClusterEntity{Name: "remote", Context: "remote", Provider: "gke"})
			e.Clusters = clusters
			e.HelmCharts[0].Cluster = "k3d-b"
			e.HelmCharts = append(e.HelmCharts, HelmChartEntity{Name: "remote", Cluster: "remote"})
			renderPlatformPrerequisitesFn = func(context.Context, string, *KCLEntities) (string, error) {
				var docs []string
				for _, target := range []string{"k3d-a", "k3d-b", "remote"} {
					docs = append(docs, "apiVersion: scheduling.k8s.io/v1\nkind: PriorityClass\nmetadata:\n  name: platform-"+target+"\n  labels:\n    forge.dev/cluster: "+target+"\nvalue: 1000")
				}
				docs = append(docs, "apiVersion: example.test/v1\nkind: PriorityClass\nmetadata:\n  name: not-builtin", "apiVersion: scheduling.k8s.io/v1\nkind: PriorityClass\nmetadata:\n  name: shared\nvalue: 100")
				return strings.Join(docs, "\n---\n"), nil
			}
			var events []string
			sentinel := errors.New("admission denied")
			applyPlatformPrerequisitesFn = func(_ context.Context, kctx, stream string) error {
				events = append(events, "pre@"+kctx)
				if !strings.Contains(stream, "name: shared") {
					t.Fatalf("shared class missing on %s: %s", kctx, stream)
				}
				if !strings.Contains(stream, "name: platform-"+kctx) || strings.Contains(stream, "not-builtin") || strings.Contains(stream, "platform-remote") {
					t.Fatalf("wrong routing: %s", stream)
				}
				other := "k3d-a"
				if kctx == other {
					other = "k3d-b"
				}
				if strings.Contains(stream, "platform-"+other) {
					t.Fatalf("foreign prerequisite: %s", stream)
				}
				if fail {
					return sentinel
				}
				return nil
			}
			applyPlatformChartFn = func(_ context.Context, kctx string, spec cluster.HelmChartSpec) error {
				events = append(events, spec.Name+"@"+kctx)
				return nil
			}
			err := ensureEnvPlatformChartsInstalled(context.Background(), "dev", "proj", e, clusters)
			if fail {
				if !errors.Is(err, sentinel) || !reflect.DeepEqual(events, []string{"pre@k3d-a"}) {
					t.Fatalf("failure must stop charts: %v, %v", err, events)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"pre@k3d-a", "pre@k3d-b", "gitops@k3d-a", "envoy-gateway@k3d-a", "cnpg@k3d-b"}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
		})
	}
}
