package obsdash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
	"github.com/reliant-labs/forge/pkg/release"
)

// The dashboards a release carries (console v2 contract K4).
//
// A release bundle holds, under observability/dashboards/, one renderer-neutral
// dashboard file per dashboard, so the Reliant console can draw exactly what
// the project declared, at the release that is running:
//
//	<project>-<id>_gen.json   a dashboard forge generates (dashboard-spec_gen.json,
//	                          plus the custom-metrics dashboard when the build
//	                          produced a metrics schema)
//	<name>.json               a dashboard the project owns
//
// WHAT THE PROJECT OWNS. deploy/observability/dashboards/ is shared by two
// formats: HyperDX tile JSON (what the local HyperDX provisioner loads, and
// what forge's own <project>-<id>_gen.json files there are) and the
// renderer-neutral spec the console draws (a "panels" array). Only the second
// can be served by the console, so:
//
//   - a project file with "panels" ships, and a file of the same name as a
//     generated dashboard REPLACES it ("shop-overview.json" replaces
//     "shop-overview_gen.json");
//   - a project file with "tiles" is HyperDX-only: it stays in the directory
//     for the local stack, ships in no bundle, and replaces nothing — the
//     generated dashboard of the same name still ships;
//   - anything else is an error, reported with the line and column.

// BundleDashboardSource says who authored a bundled dashboard.
type BundleDashboardSource string

// The sources of a bundled dashboard.
const (
	SourceGenerated BundleDashboardSource = "gen"
	SourceUser      BundleDashboardSource = "user"
)

// BundleDashboard is one file of the bundle's observability/dashboards/.
type BundleDashboard struct {
	// Name is the file name without ".json", and without "_gen" for a
	// generated one: the key a project file overrides by.
	Name   string
	Source BundleDashboardSource
	// Path is the file's path inside the bundle.
	Path string
	// Dashboard is the parsed content; Data is the exact bytes shipped.
	Dashboard Dashboard
	Data      []byte
}

// BundleSet is everything the bundle carries under observability/.
type BundleSet struct {
	Dashboards []BundleDashboard
	// MetricsSchema is the raw forge.metrics/v1 document, or nil.
	MetricsSchema []byte
	// Notes are things worth telling the person cutting the release (a
	// HyperDX-only file that was not shipped).
	Notes []string
}

// BundleFiles returns the set as bundle files, in path order.
func BundleFiles(s BundleSet) []BundleFile {
	var out []BundleFile
	for _, d := range s.Dashboards {
		out = append(out, BundleFile{Path: d.Path, Data: d.Data})
	}
	if len(s.MetricsSchema) > 0 {
		out = append(out, BundleFile{Path: release.BundleMetricsSchemaPath, Data: s.MetricsSchema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// BundleFile is a path and its bytes.
type BundleFile struct {
	Path string
	Data []byte
}

// BundleSetInput is what CollectBundleSet reads.
type BundleSetInput struct {
	// ProjectDir is the project root; the project's name is the one
	// dashboard-spec_gen.json records.
	ProjectDir string
	// MetricsSchema is the raw metrics schema the build produced, or nil.
	MetricsSchema []byte
	// Resolved is the selected environment's pure assembly. Non-nil replaces
	// disk generated dashboards, including stale process parts; owned overrides
	// still apply through the same stable filename/ID path.
	Resolved *Spec
}

// CollectBundleSet assembles and validates the observability files of a bundle.
// It returns nothing for a project that has neither generated dashboards nor a
// metrics schema, and an error (a *ProblemsError for a bad dashboard file) when
// a file the project owns is not a valid dashboard.
func CollectBundleSet(in BundleSetInput) (BundleSet, error) {
	var set BundleSet

	generated, err := generatedDashboards(in)
	if err != nil {
		return BundleSet{}, err
	}
	owned, notes, err := ownedDashboards(in.ProjectDir)
	if err != nil {
		return BundleSet{}, err
	}
	set.Notes = notes

	byName := map[string]BundleDashboard{}
	for _, d := range generated {
		byName[d.Name] = d
	}
	for _, d := range owned {
		byName[d.Name] = d // a project file wins
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	ids := map[string]string{}
	for _, n := range names {
		d := byName[n]
		if prev, dup := ids[d.Dashboard.ID]; dup {
			return BundleSet{}, fmt.Errorf("dashboard id %q is declared by both %s and %s; the console keys a dashboard on its id, so each must be unique",
				d.Dashboard.ID, prev, d.Path)
		}
		ids[d.Dashboard.ID] = d.Path
		set.Dashboards = append(set.Dashboards, d)
	}

	if len(in.MetricsSchema) > 0 {
		if _, err := metrics.ParseSchema(in.MetricsSchema); err != nil {
			return BundleSet{}, fmt.Errorf("metrics schema: %w", err)
		}
		set.MetricsSchema = in.MetricsSchema
	}
	return set, nil
}

// generatedDashboards are the dashboards forge generates: the ones in the
// spec forge generate wrote (ReadSpec) and the custom-metrics
// dashboard, which needs the built binary's schema and so cannot be in it.
func generatedDashboards(in BundleSetInput) ([]BundleDashboard, error) {
	var dashboards []Dashboard
	project := ""

	var sp Spec
	ok := in.Resolved != nil
	if ok {
		sp = *in.Resolved
	} else {
		var err error
		sp, ok, err = ReadSpec(in.ProjectDir)
		if err != nil {
			return nil, err
		}
	}
	if ok {
		if sp.Project != "" {
			project = sp.Project
		}
		dashboards = append(dashboards, sp.Dashboards...)
	}

	if len(in.MetricsSchema) > 0 {
		schema, err := metrics.ParseSchema(in.MetricsSchema)
		if err != nil {
			return nil, fmt.Errorf("metrics schema: %w", err)
		}
		if project == "" {
			project = schema.Service
		}
		if d, ok := BuildCustomMetrics(project, schema); ok {
			dashboards = append(dashboards, d)
		}
	}

	out := make([]BundleDashboard, 0, len(dashboards))
	for _, d := range dashboards {
		file := DashboardFile(project, d)
		data, err := marshalDashboardFile(d)
		if err != nil {
			return nil, err
		}
		// What forge generates must pass what forge asks of a project: a
		// failure here is a generator bug, and is caught at release cut
		// rather than in a customer's console.
		if _, err := ParseDashboard(file, data); err != nil {
			return nil, fmt.Errorf("generated dashboard is invalid (a forge bug):\n%w", err)
		}
		out = append(out, BundleDashboard{
			Name: strings.TrimSuffix(file, genSuffix), Source: SourceGenerated,
			Path: release.BundleDashboardsPath + "/" + file, Dashboard: d, Data: data,
		})
	}
	return out, nil
}

// ownedDashboards reads the project's own spec-form dashboards.
func ownedDashboards(projectDir string) ([]BundleDashboard, []string, error) {
	dir := filepath.Join(projectDir, filepath.FromSlash(DashboardsDir))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var (
		out   []BundleDashboard
		notes []string
		errs  []string
	)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasSuffix(n, genSuffix) {
			continue
		}
		rel := DashboardsDir + "/" + n
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, nil, err
		}
		if LooksLikeHyperDXDashboard(data) {
			notes = append(notes, fmt.Sprintf("%s is a HyperDX dashboard: it stays in the local stack and is not shipped to the console", rel))
			continue
		}
		d, perr := ParseDashboard(rel, data)
		if perr != nil {
			errs = append(errs, perr.Error())
			continue
		}
		out = append(out, BundleDashboard{
			Name: strings.TrimSuffix(n, ".json"), Source: SourceUser,
			Path: release.BundleDashboardsPath + "/" + n, Dashboard: d, Data: data,
		})
	}
	if len(errs) > 0 {
		return nil, nil, errors.New(strings.Join(errs, "\n"))
	}
	return out, notes, nil
}

// dashboardFile is a dashboard with the spec version it was written for.
type dashboardFile struct {
	Version string `json:"version"`
	Dashboard
}

func marshalDashboardFile(d Dashboard) ([]byte, error) {
	b, err := json.MarshalIndent(dashboardFile{Version: SpecVersion, Dashboard: d}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(bytes.TrimRight(b, "\n"), '\n'), nil
}
