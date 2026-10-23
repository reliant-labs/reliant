package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/release"
)

func seedGeneratedDashboards(t *testing.T, dir, project string) {
	t.Helper()
	in := obsdash.Input{
		Project:  project,
		Services: []obsdash.Service{{Name: "orders", Package: "orders.v1", ProtoService: "OrdersService", Methods: []obsdash.Method{{Name: "GetOrder"}}}},
	}
	if _, err := obsdash.Write(dir, in, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
}

func fetchBundleFiles(t *testing.T, dir string, digest string) map[string]string {
	t.Helper()
	store := testStore(t, dir)
	layout, err := bundle.NewLocalLayout(store.OCIDir())
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := bundle.Fetch(context.Background(), layout, digest)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	out := t.TempDir()
	paths, err := bundle.Unpack(bytes.NewReader(fetched.Manifests), out)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, p := range paths {
		if b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(p))); err == nil {
			files[p] = string(b)
		}
	}
	return files
}

func TestEnvBuildBundle_CarriesTheDashboardsAndTheMetricsSchema(t *testing.T) {
	dir := newLedgerTestProject(t, "obs-bundle-project")
	t.Chdir(dir)
	stubEnvShape(t, "obs-bundle-project")
	seedGeneratedDashboards(t, dir, "obs-bundle-project")

	schema := `{"schema_version":"forge.metrics/v1","service":"obs-bundle-project","version":"v1","metrics":[` +
		`{"name":"orders_created","type":"counter","unit":"{order}","description":"d","attributes":[],"stability":"stable","since":"v1"}]}` + "\n"
	schemaPath := filepath.Join(dir, "bin", filepath.FromSlash(MetricsSchemaArtifact))
	if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dashboard the project owns, overriding a generated one.
	owned := `{"id":"overview","title":"Mine","panels":[{"id":"p","title":"P","kind":"number","signal":"traces","series":[{"label":"n","agg":"count"}]}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, obsdash.DashboardsDir, "obs-bundle-project-overview.json"), []byte(owned), 0o644); err != nil {
		t.Fatal(err)
	}

	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Pins: release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now: bundleTestNow, errOut: io.Discard, StrictObservability: true,
	})
	if err != nil {
		t.Fatalf("writeEnvBundles: %v", err)
	}
	files := fetchBundleFiles(t, dir, written[0].Digest)

	if got := files[release.BundleMetricsSchemaPath]; got != schema {
		t.Errorf("metrics schema = %q, want the build's bytes", got)
	}
	if got := files["observability/dashboards/obs-bundle-project-overview.json"]; got != owned {
		t.Errorf("the project's overview did not ship byte for byte: %q", got)
	}
	if _, ok := files["observability/dashboards/obs-bundle-project-overview_gen.json"]; ok {
		t.Error("the generated overview shipped beside the file that overrides it")
	}
	for _, want := range []string{
		"observability/dashboards/obs-bundle-project-service-orders_gen.json",
		"observability/dashboards/obs-bundle-project-workloads_gen.json",
		"observability/dashboards/obs-bundle-project-releases_gen.json",
		"observability/dashboards/obs-bundle-project-custom-metrics_gen.json",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("bundle lacks %s", want)
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "manifests/") && !strings.HasSuffix(name, ".yaml") {
			t.Errorf("%s is under the applied tree and is not a manifest", name)
		}
	}
}

func TestEnvBuildBundle_AnInvalidOwnedDashboardFailsTheCutWithItsLocation(t *testing.T) {
	dir := newLedgerTestProject(t, "obs-bad-project")
	t.Chdir(dir)
	stubEnvShape(t, "obs-bad-project")
	seedGeneratedDashboards(t, dir, "obs-bad-project")
	bad := "{\n  \"id\": \"broken\",\n  \"title\": \"B\",\n  \"panels\": [\n    {\"id\": \"p\", \"title\": \"P\", \"kind\": \"cube\", \"signal\": \"traces\"}\n  ]\n}\n"
	if err := os.WriteFile(filepath.Join(dir, obsdash.DashboardsDir, "broken.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	in := bundleBuildInputs{
		Release: "v1.4.0", Pins: release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now: bundleTestNow, errOut: io.Discard, StrictObservability: true,
	}
	_, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, in)
	if err == nil || !strings.Contains(err.Error(), "deploy/observability/dashboards/broken.json:5:") {
		t.Fatalf("err = %v, want a cut failure naming broken.json:5:<col>", err)
	}
	if store := testStore(t, dir); func() int { b, _ := store.Bundles("prod"); return len(b) }() != 0 {
		t.Error("a bundle was recorded for a release whose dashboards do not validate")
	}

	// A deploy that builds nothing only warns: it must still ship a release
	// that was cut before the working tree broke.
	var warn bytes.Buffer
	in.StrictObservability, in.errOut = false, &warn
	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, in)
	if err != nil {
		t.Fatalf("non-strict write failed: %v", err)
	}
	if !strings.Contains(warn.String(), "broken.json:5:") {
		t.Errorf("no warning naming the file: %q", warn.String())
	}
	files := fetchBundleFiles(t, dir, written[0].Digest)
	for name := range files {
		if strings.HasPrefix(name, "observability/") {
			t.Errorf("the degraded bundle carries %s", name)
		}
	}
}

func TestObserveDashboardsCheckListsWhatWouldShip(t *testing.T) {
	dir := newLedgerTestProject(t, "obs-check-project")
	t.Chdir(dir)
	seedGeneratedDashboards(t, dir, "obs-check-project")
	var out bytes.Buffer
	if err := runObsDashboardsCheck(&out, "bin"); err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, want := range []string{"observability/dashboards/obs-check-project-overview_gen.json", "would ship"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
