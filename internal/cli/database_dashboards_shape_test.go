package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderDashboards renders a one-database env with the given `dashboards = ...`
// expression ("" for none) through the seam `forge env shape` uses.
func renderDashboards(t *testing.T, decl string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	if decl != "" {
		decl = "dashboards = " + decl
	}
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"dbdash\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        ` + decl + `
    }]
})
`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

// KCL's defaults and release.DefaultDatabaseDashboards are held separately
// (kcl/schema.k, pkg/release, pkg/deploy/v1alpha1): `DatabaseDashboards
// {enabled = True}` must resolve to the same limits in all three, or the
// control plane enforces numbers the author never saw.
func TestDatabaseDashboardsDefaultsAgreeWithKCL(t *testing.T) {
	e := renderDashboards(t, `forge.DatabaseDashboards {enabled = True}`)
	got := shapeDatabasesOf(e)
	want := release.DefaultDatabaseDashboards()
	if len(got) != 1 || got[0].Name != "orders" || got[0].Dashboards == nil || *got[0].Dashboards != want {
		t.Fatalf("KCL defaults project to %+v, Go defaults are %+v", got, want)
	}
	spec := e.Databases[0].Spec.Dashboards
	if spec == nil || !spec.Enabled {
		t.Fatalf("the record's spec has no enabled dashboards: %+v", spec)
	}
	if w := (deployv1alpha1.DatabaseDashboards{Enabled: true}).WithDefaults(); *spec != w {
		t.Errorf("KCL spec %+v, Go spec defaults %+v", *spec, w)
	}
}

// Absent and explicitly-off both record nothing: the shape fails closed.
func TestDatabaseDashboardsAbsentAndOffRecordNothing(t *testing.T) {
	for name, decl := range map[string]string{
		"absent":      "",
		"empty":       `forge.DatabaseDashboards {}`,
		"enabled=off": `forge.DatabaseDashboards {enabled = False, row_cap = 5}`,
	} {
		e := renderDashboards(t, decl)
		if got := shapeDatabasesOf(e); got != nil {
			t.Errorf("%s: shape records %+v, want nothing", name, got)
		}
		if s := (release.Shape{Kind: release.EnvSelfManaged, Databases: shapeDatabasesOf(e)}); s.DashboardsFor("orders").Enabled {
			t.Errorf("%s: dashboards read as enabled", name)
		}
	}
}

// Declared limits make it through KCL (a duration string) into the shape
// (milliseconds), and through the shape's own validation.
func TestDatabaseDashboardsDeclaredLimitsReachTheShape(t *testing.T) {
	e := renderDashboards(t, `forge.DatabaseDashboards {enabled = True, row_cap = 250, timeout = "3s", max_concurrency = 1}`)
	got := shapeDatabasesOf(e)
	want := []release.ShapeDatabase{{Name: "orders", Dashboards: &release.ShapeDatabaseDashboards{Enabled: true, RowCap: 250, TimeoutMs: 3000, MaxConcurrency: 1}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shape databases = %+v, want %+v", got[0].Dashboards, want[0].Dashboards)
	}
	if err := (release.Shape{Kind: release.EnvSelfManaged, Databases: got}).Validate(); err != nil {
		t.Errorf("projected shape is invalid: %v", err)
	}
}

func TestShapeDatabasesOfNilSafe(t *testing.T) {
	if shapeDatabasesOf(nil) != nil || shapeDatabasesOf(&KCLEntities{}) != nil {
		t.Error("a nil or database-less env projects databases")
	}
	e := &KCLEntities{Databases: []DatabaseEntity{
		{Name: "plain"},
		{Name: "on", Spec: deployv1alpha1.ManagedDatabaseSpec{Dashboards: &deployv1alpha1.DatabaseDashboards{Enabled: true, MaxConcurrency: 3}}},
		{Name: "off", Spec: deployv1alpha1.ManagedDatabaseSpec{Dashboards: &deployv1alpha1.DatabaseDashboards{}}},
	}}
	got := shapeDatabasesOf(e)
	if len(got) != 1 || got[0].Name != "on" || got[0].Dashboards.MaxConcurrency != 3 || got[0].Dashboards.RowCap != 1000 {
		t.Errorf("projected %+v, want only `on`, defaults filled", got)
	}
}
