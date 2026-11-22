package cli

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/release"
)

// filterParamMarks are what a dashboard that needs a filter-param-serving
// control plane carries: a declared param, or a statement reading one.
var filterParamMarks = []string{"{f_", "filterParams"}

// dashboardIDsOf are the ids of the dashboards among bundle files.
func dashboardIDsOf(t *testing.T, files []bundle.ObservabilityFile) []string {
	t.Helper()
	var ids []string
	for _, f := range files {
		if id, _ := needsFilterParamServer(f); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// A default `forge generate`, and the bundle built from it for any control
// plane that has not named release.CapabilityServesDashboardFilterParams,
// carries no filter param and neither card dashboard: a forge release must
// never write a bundle that a control plane in prod, or its rollback target,
// cannot serve. Only a control plane that names the capability gets them.
func TestDefaultBundleCarriesNoFilterParams(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml": "name: sample\nmodule_path: example.com/sample\n",
		"go.mod":     "module example.com/sample\n\ngo 1.25\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := obsdash.Write(dir, obsdash.Input{Project: "sample", Frontends: []string{"web"}}, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	// What generate wrote: nothing reads a filter param.
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range filterParamMarks {
			if strings.Contains(string(raw), m) {
				t.Errorf("forge generate wrote %s, which carries %q", path, m)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	site := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Type: "vite-spa", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}}}
	// A fixed creation time, so nothing here depends on the wall clock.
	all, err := bundleObservabilityFiles(dir, bundleBuildInputs{StrictObservability: true, Now: time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC), errOut: io.Discard}, site)
	if err != nil {
		t.Fatal(err)
	}
	// The set before the gate has the cards, so the gate is what keeps them out.
	if ids := dashboardIDsOf(t, all); !slices.Contains(ids, "traffic") || !slices.Contains(ids, "uptime") {
		t.Fatalf("the bundle set has no card dashboards to withhold: %v", ids)
	}

	without := slices.DeleteFunc(release.BundleDocFeatures(), func(f string) bool { return f == release.CapabilityServesDashboardFilterParams })
	for name, reader := range map[string]*release.BundleReader{
		"the machine ledger (nil reader)":                 nil,
		"a control plane that predates GetCapabilities":   {},
		"a control plane pinned to this forge (doc list)": {Features: without},
	} {
		files, dropped := withholdUnservedDashboards(all, reader)
		for _, f := range files {
			for _, m := range filterParamMarks {
				if strings.Contains(string(f.Data), m) {
					t.Errorf("%s: the bundle's %s carries %q", name, f.Path, m)
				}
			}
		}
		if ids := dashboardIDsOf(t, files); len(ids) != 0 {
			t.Errorf("%s: the bundle carries %v", name, ids)
		}
		slices.Sort(dropped)
		if !slices.Equal(dropped, []string{"traffic", "uptime"}) {
			t.Errorf("%s: withheld %v, want the two card dashboards", name, dropped)
		}
		if len(files) != len(all)-2 {
			t.Errorf("%s: withheld %d files, want only the 2 card dashboards", name, len(all)-len(files))
		}
	}

	served, dropped := withholdUnservedDashboards(all, &release.BundleReader{Features: append(without, release.CapabilityServesDashboardFilterParams)})
	if len(served) != len(all) || dropped != nil {
		t.Errorf("a control plane that serves filter params got %d of %d files (withheld %v)", len(served), len(all), dropped)
	}
}

// A dashboard the project owns that reads a filter param is withheld the same
// way, wherever the read is: a panel, its trend or subtitle, or an attention
// figure. One that reads none, or only a toggle, ships.
func TestWithholdsAnyDashboardReadingFilterParams(t *testing.T) {
	file := func(name, json string) bundle.ObservabilityFile {
		return bundle.ObservabilityFile{Path: release.BundleDashboardsPath + "/" + name + ".json", Data: []byte(json)}
	}
	files := []bundle.ObservabilityFile{
		file("plain", `{"id":"plain","title":"P","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`),
		file("toggle", `{"id":"toggle","title":"T","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT {include_internal:Bool}"}]}`),
		file("declared", `{"id":"declared","title":"D","filterParams":[{"id":"f_x","label":"X"}],"panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`),
		file("trend", `{"id":"trend","title":"T","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1","trendSql":"SELECT now(), {f_x:String} = ''"}]}`),
		file("figure", `{"id":"figure","title":"F","panels":[{"id":"a","title":"A","kind":"attention","attention":{"figures":[{"id":"f","title":"F","kind":"sql","sql":"SELECT {f_x:String} = ''"}]}}]}`),
		file("literal", `{"id":"literal","title":"L","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT '{f_x:String}'"}]}`),
		{Path: release.BundleMetricsSchemaPath, Data: []byte(`{"f_x": "{f_x:String}"}`)},
	}
	kept, dropped := withholdUnservedDashboards(files, nil)
	if want := []string{"declared", "trend", "figure"}; !slices.Equal(dropped, want) {
		t.Errorf("withheld %v, want %v", dropped, want)
	}
	if len(kept) != 4 {
		t.Errorf("kept %d files, want plain, toggle, literal and the metrics schema", len(kept))
	}
}
