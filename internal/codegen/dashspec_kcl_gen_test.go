package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/pkg/dashspec"
)

// TestDashspecKCLIsCurrent regenerates kcl/dashspec/dashspec_gen.k from
// pkg/dashspec and fails if the committed file differs. With
// FORGE_UPDATE_GENERATED=1 it rewrites the file instead.
//
// It runs under -short: the generator parses pkg/dashspec's source in this
// process (no `go list`), so every input is a file the test cache sees.
func TestDashspecKCLIsCurrent(t *testing.T) {
	root := forgeRepoRoot(t)
	m, err := LoadDashspecModel(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GenerateDashspecKCL(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, DashspecKCLPath)
	if os.Getenv("FORGE_UPDATE_GENERATED") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with FORGE_UPDATE_GENERATED=1)", DashspecKCLPath, err)
	}
	if string(want) != got {
		t.Fatalf("%s is stale; run: FORGE_UPDATE_GENERATED=1 go test ./internal/codegen -run TestDashspecKCLIsCurrent", DashspecKCLPath)
	}
}

// TestDashspecKCLMatchesGoFieldForField parses the GENERATED KCL and checks
// every schema's field set against reflection over the real Go types, a
// different route from the source parse the generator reads. A generator that
// dropped, renamed or invented a field fails here, and so does a struct the
// walk failed to reach.
func TestDashspecKCLMatchesGoFieldForField(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(forgeRepoRoot(t), DashspecKCLPath))
	if err != nil {
		t.Fatal(err)
	}
	kclFields := parseKCLSchemaFields(string(src))

	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		var goFields []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" || !f.IsExported() {
				continue
			}
			goFields = append(goFields, name)
			got := kclFields[typ.Name()]
			optional := strings.Contains(opts, "omitempty")
			if !containsString(got, name) {
				continue // reported below with the whole set
			}
			if wantLine := "    " + name + map[bool]string{true: "?", false: ""}[optional] + ": "; !strings.Contains(string(src), wantLine) {
				t.Errorf("%s.%s: want it declared %q (omitempty=%v)", typ.Name(), name, strings.TrimSpace(wantLine), optional)
			}
			walk(f.Type)
		}
		sort.Strings(goFields)
		got := append([]string(nil), kclFields[typ.Name()]...)
		sort.Strings(got)
		if !reflect.DeepEqual(goFields, got) {
			t.Errorf("schema %s: KCL fields %v, Go JSON fields %v", typ.Name(), got, goFields)
		}
	}
	walk(reflect.TypeOf(dashspec.Dashboard{}))
	if len(seen) != len(kclFields) {
		var names []string
		for n := range kclFields {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("KCL declares %d schemas %v; Go reaches %d structs from Dashboard", len(kclFields), names, len(seen))
	}
}

// TestDashspecKCLEnumsAreTheValidatorsEnums pins the enum value lists the
// generator read to the Go constants the validator accepts, for the enums a
// dashboard author meets first. A value the generator invented, or one the
// source parse missed, fails here.
func TestDashspecKCLEnumsAreTheValidatorsEnums(t *testing.T) {
	m, err := LoadDashspecModel(forgeRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, e := range m.Enums {
		got[e.Name] = e.Values
	}
	strs := func(xs ...any) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = reflect.ValueOf(x).String()
		}
		return out
	}
	for name, want := range map[string][]string{
		"Signal": strs(dashspec.Traces, dashspec.Metrics, dashspec.Logs),
		"PanelKind": strs(dashspec.KindNumber, dashspec.KindLine, dashspec.KindTable, dashspec.KindSearch, dashspec.KindBar,
			dashspec.KindHeatmap, dashspec.KindSQL, dashspec.KindIssues, dashspec.KindSessions, dashspec.KindFunnel,
			dashspec.KindPostgres, dashspec.KindText, dashspec.KindBrowse, dashspec.KindAttention,
			dashspec.KindReleaseHealth, dashspec.KindPromotions, dashspec.KindDelivery),
		"Agg": strs(dashspec.AggCount, dashspec.AggCountDistinct, dashspec.AggSum, dashspec.AggAvg, dashspec.AggMax,
			dashspec.AggQuantile, dashspec.AggLast, dashspec.AggRate, dashspec.AggIncrease, dashspec.AggMin),
		"MetricType": {dashspec.MetricTypeSum, dashspec.MetricTypeGauge, dashspec.MetricTypeHistogram,
			dashspec.MetricTypeExpHistogram, dashspec.MetricTypeSummary},
		"Scope":        strs(dashspec.ScopeProject, dashspec.ScopeOrg, dashspec.ScopePlatform),
		"Unit":         strs(dashspec.UnitNone, dashspec.UnitCount, dashspec.UnitMillis, dashspec.UnitBytes, dashspec.UnitPercent, dashspec.UnitSeconds),
		"Op":           strs(dashspec.OpEq, dashspec.OpNeq, dashspec.OpIn, dashspec.OpNotIn, dashspec.OpPrefix, dashspec.OpMatch, dashspec.OpExists, dashspec.OpGTE),
		"PostgresDraw": {dashspec.PostgresDrawAuto, dashspec.PostgresDrawLine, dashspec.PostgresDrawNumber, dashspec.PostgresDrawTable, dashspec.PostgresDrawBar},
		"Group":        strs(sliceAny(dashspec.Groups)...),
		"ChartColor":   strs(sliceAny(dashspec.ChartColors)...),
		"BrowseOp":     strs(sliceAny(dashspec.BrowseOps())...),
	} {
		if !reflect.DeepEqual(got[name], want) {
			t.Errorf("enum %s: generated %v, Go %v", name, got[name], want)
		}
	}
}

func sliceAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// TestDashspecKCLRoundTripsThroughParseDashboard renders the KCL fixture
// kcl/tests/positive_dashspec_every_kind.k — a dashboard declared with the
// generated schemas and helpers, one panel of every kind, a histogram p95 and
// a summary quantile among them — and requires dashspec.ParseDashboard to
// accept the JSON it serializes to. The corpus (internal/templates) runs the
// same fixture for its KCL asserts; this is the half KCL cannot check.
func TestDashspecKCLRoundTripsThroughParseDashboard(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL through the native runtime (seconds), whose file reads the test cache cannot see; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := filepath.Join(forgeRepoRoot(t), "kcl", "tests")
	out, err := kclrender.Run(dir, filepath.Join(dir, "positive_dashspec_every_kind.k"), nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode render: %v\n%s", err, out)
	}
	raw, ok := doc["dashboard"]
	if !ok {
		t.Fatalf("the fixture outputs no `dashboard`: %s", out)
	}
	d, err := dashspec.ParseDashboard("positive_dashspec_every_kind.k", raw)
	if err != nil {
		t.Fatalf("ParseDashboard refused the KCL-declared dashboard: %v\n%s", err, raw)
	}
	var emptyMetricTypePreserved bool
	if err := json.Unmarshal(doc["assert_empty_optional_metric_type"], &emptyMetricTypePreserved); err != nil || !emptyMetricTypePreserved {
		t.Fatalf("the trace count must serialize an explicitly empty optional metricType: %v\n%s", err, raw)
	}

	kinds := map[dashspec.PanelKind]bool{}
	var p95, summary bool
	for _, p := range d.Panels {
		kinds[p.Kind] = true
		for _, s := range p.Series {
			if s.Agg == dashspec.AggQuantile && s.Quantile == 0.95 && s.MetricType == dashspec.MetricTypeHistogram {
				p95 = true
			}
			if s.Agg == dashspec.AggQuantile && s.MetricType == dashspec.MetricTypeSummary {
				summary = true
			}
		}
	}
	m, err := LoadDashspecModel(forgeRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Enums {
		if e.Name != "PanelKind" {
			continue
		}
		for _, k := range e.Values {
			if !kinds[dashspec.PanelKind(k)] {
				t.Errorf("the fixture declares no %q panel: every kind must round-trip", k)
			}
		}
	}
	if !p95 {
		t.Error("the fixture declares no histogram p95 series")
	}
	if !summary {
		t.Error("the fixture declares no summary-quantile series")
	}
}
