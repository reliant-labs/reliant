package obsdash

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/pkg/release"
)

func goldenDashboard(t *testing.T, name string, d Dashboard) {
	t.Helper()
	sp := Spec{Version: SpecVersion, Project: "shop", Dashboards: []Dashboard{d}, SavedSearches: []SavedSearch{}}
	files, err := RenderHyperDX(sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].File != name+"_gen.json" {
		t.Fatalf("files = %+v", files)
	}
	golden(t, name+"_gen.json", files[0].Content)
	if errs := validateHyperDXDashboard(files[0].Content); len(errs) > 0 {
		t.Errorf("%s fails the HyperDX schema mirror:\n%s", name, strings.Join(errs, "\n"))
	}
	b, _ := marshalDashboardFile(d)
	golden(t, name+"_gen.spec.json", b)
	if _, err := ParseDashboard(name, b); err != nil {
		t.Errorf("%s does not validate as a spec dashboard:\n%v", name, err)
	}
}

func TestWorkloadsDashboardGolden(t *testing.T) {
	d := BuildWorkloads("shop")
	goldenDashboard(t, "shop-workloads", d)

	panels := map[string]Panel{}
	for _, p := range d.Panels {
		panels[p.ID] = p
	}
	// The customer-metadata surface of owner decision 4: usage against requests
	// and limits, restarts and why, the versions that ran, and events.
	for id, metric := range map[string]string{
		"cpu-vs-request":    "k8s.container.cpu_request_utilization",
		"cpu-vs-limit":      "k8s.container.cpu_limit_utilization",
		"memory-vs-request": "k8s.container.memory_request_utilization",
		"memory-vs-limit":   "k8s.container.memory_limit_utilization",
		"restarts":          "k8s.container.restarts",
	} {
		p, ok := panels[id]
		if !ok || len(p.Series) != 1 || p.Series[0].Field != metric || p.Signal != Metrics {
			t.Errorf("panel %s = %+v, want a metrics series over %s", id, p, metric)
		}
	}
	if p := panels["restarts-by-exit-reason"]; len(p.GroupBy) != 1 || p.GroupBy[0] != "metric.attr.k8s.container.last_terminated_reason" {
		t.Errorf("restarts-by-exit-reason groups by %v", p.GroupBy)
	}
	if p := panels["cpu-by-image-tag"]; len(p.GroupBy) != 1 || p.GroupBy[0] != "resource.attr.container.image.tag" {
		t.Errorf("cpu-by-image-tag groups by %v", p.GroupBy)
	}
	for _, id := range []string{"events", "container-terminations"} {
		if p := panels[id]; p.Kind != KindSearch || p.Signal != Logs {
			t.Errorf("%s = %+v, want a log search", id, p)
		}
	}
	if len(d.Annotations) != 0 {
		t.Error("BuildWorkloads declares no annotations itself; Build adds them to every dashboard with a time series")
	}
}

func TestReleasesDashboardGolden(t *testing.T) {
	d := withReleaseMarkers(BuildReleases("shop"))
	goldenDashboard(t, "shop-releases", d)

	if len(d.Annotations) != 1 || d.Annotations[0].Source != AnnotationReleases {
		t.Fatalf("annotations = %+v, want the releases source", d.Annotations)
	}
	var lines, table int
	for _, p := range d.Panels {
		switch p.Kind {
		case KindLine:
			lines++
			if len(p.GroupBy) != 1 || p.GroupBy[0] != "resource.attr.service.version" {
				t.Errorf("%s groups by %v, want service.version", p.ID, p.GroupBy)
			}
		case KindTable:
			table++
			if len(p.GroupBy) != 1 || p.GroupBy[0] != "resource.attr.service.version" {
				t.Errorf("%s groups by %v, want service.version", p.ID, p.GroupBy)
			}
		}
	}
	if lines < 4 || table != 1 {
		t.Errorf("releases dashboard has %d line panels and %d comparison tables", lines, table)
	}
}

// withCustomMetrics is the project's spec plus the Custom metrics dashboard, as
// a release bundle carries them.
func withCustomMetrics(t *testing.T) Spec {
	t.Helper()
	sp := Build(fixture())
	d, ok := BuildCustomMetrics("shop", customSchema(t))
	if !ok {
		t.Fatal("no custom-metrics dashboard for a schema that declares metrics")
	}
	sp.Dashboards = append(sp.Dashboards, d)
	return sp
}

func TestNoGeneratedDashboardDrawsAHeatmap(t *testing.T) {
	files, err := RenderHyperDX(withCustomMetrics(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(string(f.Content), `"displayType": "heatmap"`) {
			t.Errorf("%s draws a heatmap; owner decision 7 skips them", f.File)
		}
	}
}

func TestReleaseMarkersAreDeclaredWhereATimeSeriesExists(t *testing.T) {
	for _, d := range withCustomMetrics(t).Dashboards {
		hasLine := false
		for _, p := range d.Panels {
			hasLine = hasLine || p.Kind == KindLine
		}
		got := len(d.Annotations) == 1 && d.Annotations[0].Source == AnnotationReleases
		if hasLine != got {
			t.Errorf("%s: has a line panel = %v, declares release markers = %v", d.ID, hasLine, got)
		}
	}
}

func TestBuildAddsTheWorkloadsAndReleasesDashboards(t *testing.T) {
	var ids []string
	for _, d := range Build(fixture()).Dashboards {
		ids = append(ids, d.ID)
	}
	if got, want := strings.Join(ids, ","), "overview,service-billing,service-orders,workloads,releases,frontend"; got != want {
		t.Fatalf("dashboards = %s, want %s", got, want)
	}
}

func TestHyperDXRendererIgnoresAnnotations(t *testing.T) {
	d := BuildReleases("shop")
	a, err := RenderHyperDX(Spec{Project: "shop", Dashboards: []Dashboard{d}})
	if err != nil {
		t.Fatal(err)
	}
	d.Annotations = []Annotation{{Source: AnnotationReleases}}
	b, err := RenderHyperDX(Spec{Project: "shop", Dashboards: []Dashboard{d}})
	if err != nil {
		t.Fatal(err)
	}
	if string(a[0].Content) != string(b[0].Content) {
		t.Error("an annotation changed the HyperDX rendering; HyperDX has no such concept")
	}
}

// ── bundle set ──────────────────────────────────────────────────────────────

func writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Write(dir, fixture(), &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeOwned(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, DashboardsDir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const ownedOverview = `{
  "id": "overview",
  "title": "Mine",
  "panels": [
    {"id": "p1", "title": "Orders", "kind": "number", "signal": "traces",
     "series": [{"label": "n", "agg": "count"}]}
  ]
}
`

func collect(t *testing.T, dir string, schema []byte) BundleSet {
	t.Helper()
	set, err := CollectBundleSet(BundleSetInput{ProjectDir: dir, MetricsSchema: schema})
	if err != nil {
		t.Fatalf("CollectBundleSet: %v", err)
	}
	return set
}

func paths(set BundleSet) []string {
	var out []string
	for _, f := range BundleFiles(set) {
		out = append(out, f.Path)
	}
	return out
}

func TestBundleSetCarriesEveryGeneratedDashboard(t *testing.T) {
	dir := writeProject(t)
	set := collect(t, dir, nil)
	got := strings.Join(paths(set), "\n")
	for _, want := range []string{
		"observability/dashboards/shop-overview_gen.json",
		"observability/dashboards/shop-service-orders_gen.json",
		"observability/dashboards/shop-service-billing_gen.json",
		"observability/dashboards/shop-workloads_gen.json",
		"observability/dashboards/shop-releases_gen.json",
		"observability/dashboards/shop-frontend_gen.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bundle lacks %s; has:\n%s", want, got)
		}
	}
	for _, d := range set.Dashboards {
		if d.Source != SourceGenerated {
			t.Errorf("%s source = %s, want gen", d.Path, d.Source)
		}
		if !strings.HasPrefix(d.Path, release.BundleDashboardsPath+"/") {
			t.Errorf("%s is outside %s", d.Path, release.BundleDashboardsPath)
		}
	}
}

func TestBundleSetDrawsCustomMetricsFromTheSchema(t *testing.T) {
	dir := writeProject(t)
	schema, _ := json.Marshal(customSchema(t))

	without := strings.Join(paths(collect(t, dir, nil)), "\n")
	if strings.Contains(without, "custom-metrics") {
		t.Error("a custom-metrics dashboard appeared without a schema")
	}

	set := collect(t, dir, schema)
	got := strings.Join(paths(set), "\n")
	if !strings.Contains(got, "observability/dashboards/shop-custom-metrics_gen.json") {
		t.Fatalf("no custom-metrics dashboard in:\n%s", got)
	}
	if !strings.Contains(got, release.BundleMetricsSchemaPath) {
		t.Errorf("the metrics schema is not in the bundle:\n%s", got)
	}
	if string(set.MetricsSchema) != string(schema) {
		t.Error("the shipped schema is not the bytes the build produced")
	}
}

func TestAUserFileOfTheSameNameReplacesTheGeneratedOne(t *testing.T) {
	dir := writeProject(t)
	writeOwned(t, dir, "shop-overview.json", ownedOverview)

	set := collect(t, dir, nil)
	var found *BundleDashboard
	for i, d := range set.Dashboards {
		if d.Name == "shop-overview" {
			if found != nil {
				t.Fatalf("shop-overview ships twice: %s and %s", found.Path, d.Path)
			}
			found = &set.Dashboards[i]
		}
	}
	if found == nil {
		t.Fatal("shop-overview is missing")
	}
	if found.Source != SourceUser || found.Dashboard.Title != "Mine" || found.Path != "observability/dashboards/shop-overview.json" {
		t.Errorf("shipped %+v, want the user's file", found)
	}
	if string(found.Data) != ownedOverview {
		t.Error("the user's file was not shipped byte for byte")
	}
	if strings.Contains(strings.Join(paths(set), "\n"), "shop-overview_gen.json") {
		t.Error("the generated twin shipped beside the file that overrides it")
	}
}

func TestAUserFileWithANewNameShipsBesideTheGeneratedSet(t *testing.T) {
	dir := writeProject(t)
	writeOwned(t, dir, "checkout.json", strings.Replace(ownedOverview, `"overview"`, `"checkout"`, 1))
	set := collect(t, dir, nil)
	got := strings.Join(paths(set), "\n")
	if !strings.Contains(got, "observability/dashboards/checkout.json") || !strings.Contains(got, "shop-overview_gen.json") {
		t.Errorf("want checkout.json and the generated set, got:\n%s", got)
	}
}

func TestAHyperDXFileStaysLocalAndOverridesNothing(t *testing.T) {
	dir := writeProject(t)
	writeOwned(t, dir, "shop-overview.json", `{"name":"x","tags":[],"tiles":[{"id":"a"}]}`)
	set := collect(t, dir, nil)
	got := strings.Join(paths(set), "\n")
	if !strings.Contains(got, "shop-overview_gen.json") {
		t.Errorf("a HyperDX file replaced the generated dashboard in the bundle:\n%s", got)
	}
	if len(set.Notes) != 1 || !strings.Contains(set.Notes[0], "HyperDX") {
		t.Errorf("notes = %v, want one HyperDX note", set.Notes)
	}
}

func TestADashboardIdIsUniqueAcrossTheBundle(t *testing.T) {
	dir := writeProject(t)
	writeOwned(t, dir, "other.json", ownedOverview) // id "overview" again
	_, err := CollectBundleSet(BundleSetInput{ProjectDir: dir})
	if err == nil || !strings.Contains(err.Error(), `dashboard id "overview"`) {
		t.Fatalf("err = %v, want a duplicate-id error", err)
	}
}

func TestBundleSetIsEmptyForAProjectWithNothingToShip(t *testing.T) {
	set := collect(t, t.TempDir(), nil)
	if len(BundleFiles(set)) != 0 {
		t.Errorf("files = %v", paths(set))
	}
}

func TestBundleSetIsDeterministic(t *testing.T) {
	dir := writeProject(t)
	schema, _ := json.Marshal(customSchema(t))
	a, b := BundleFiles(collect(t, dir, schema)), BundleFiles(collect(t, dir, schema))
	if len(a) != len(b) {
		t.Fatal("different file sets")
	}
	for i := range a {
		if a[i].Path != b[i].Path || string(a[i].Data) != string(b[i].Data) {
			t.Errorf("%s differs between two collections", a[i].Path)
		}
	}
}

func TestEveryGeneratedDashboardPassesTheValidator(t *testing.T) {
	for _, d := range withCustomMetrics(t).Dashboards {
		b, _ := marshalDashboardFile(d)
		if _, err := ParseDashboard(d.ID, b); err != nil {
			t.Errorf("%s: %v", d.ID, err)
		}
	}
}

func TestAnInvalidUserFileFailsTheBundleSetWithItsLocation(t *testing.T) {
	dir := writeProject(t)
	writeOwned(t, dir, "broken.json", "{\n  \"id\": \"broken\",\n  \"title\": \"B\",\n  \"panels\": [\n    {\"id\": \"p\", \"title\": \"P\", \"kind\": \"cube\", \"signal\": \"traces\"}\n  ]\n}\n")
	_, err := CollectBundleSet(BundleSetInput{ProjectDir: dir})
	if err == nil {
		t.Fatal("an invalid file was accepted")
	}
	if !strings.Contains(err.Error(), "deploy/observability/dashboards/broken.json:5:") {
		t.Errorf("err = %v, want broken.json:5:<col>", err)
	}
}

func TestAnAnnotatedUserDashboardRoundTrips(t *testing.T) {
	src := `{
  "id": "mine", "title": "Mine",
  "annotations": [{"source": "releases", "label": "Deploys"}],
  "panels": [{"id": "p", "title": "P", "kind": "line", "signal": "traces", "series": [{"label": "n", "agg": "count"}]}]
}`
	d, err := ParseDashboard("mine.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Annotations) != 1 || d.Annotations[0].Source != AnnotationReleases || d.Annotations[0].Label != "Deploys" {
		t.Errorf("annotations = %+v", d.Annotations)
	}
}

func TestHostedOnlyDashboardsAreNotWrittenLocally(t *testing.T) {
	dir := writeProject(t)
	for _, name := range []string{"shop-workloads_gen.json", "shop-releases_gen.json"} {
		if _, err := os.Stat(filepath.Join(dir, DashboardsDir, name)); err == nil {
			t.Errorf("%s was written to the project: it has nothing to draw under forge env up and is constant library text", name)
		}
	}
	for _, name := range []string{"shop-overview_gen.json", "shop-service-orders_gen.json"} {
		if _, err := os.Stat(filepath.Join(dir, DashboardsDir, name)); err != nil {
			t.Errorf("%s is missing: %v", name, err)
		}
	}
	// ...but they are in the spec, and so in the bundle.
	raw, err := os.ReadFile(filepath.Join(dir, ProjectSpecFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`"id": "workloads"`, `"id": "releases"`} {
		if !strings.Contains(string(raw), id) {
			t.Errorf("%s is not in %s", id, ProjectSpecFile)
		}
	}
}
