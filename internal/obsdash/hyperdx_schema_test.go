package obsdash

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file pins the rendered JSON to HyperDX PinnedHyperDX. The rules below
// are transcribed from DashboardWithoutIdSchema in
// @hyperdx/common-utils@2.40.0 (packages/common-utils/src/types.ts, tag
// @hyperdx/api@2.40.0) and from the provisioner's reader
// (packages/api/src/tasks/provisionDashboards/index.ts). They are a
// hand-written mirror, so TestRenderedDashboardsPassTheRealSchema runs the
// real zod schema from the ClickStack image to keep the mirror honest.

var (
	displayTypes = map[string]bool{"line": true, "stacked_bar": true, "table": true, "pie": true, "bar": true, "number": true, "search": true, "heatmap": true, "markdown": true, "event_patterns": true}
	aggFns       = map[string]bool{"avg": true, "count": true, "count_distinct": true, "last_value": true, "max": true, "min": true, "quantile": true, "sum": true, "any": true, "none": true, "increase": true}
	intervalRe   = regexp.MustCompile(`^\d+ (second|minute|hour|day)$`)
	varNameRe    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
)

const (
	maxTiles   = 500 // DASHBOARD_MAX_TILES
	gridWidth  = 24
	sourceKind = "Logs|Traces|Metrics"
)

func validateHyperDXDashboard(raw []byte) []string {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	var d map[string]json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil {
		return []string{"not a JSON object: " + err.Error()}
	}
	// Strict keys: DashboardSchema is a plain z.object, which strips unknown
	// keys silently. A stray key is a typo that would be dropped on load.
	allowed := map[string]bool{"name": true, "tiles": true, "tags": true, "filters": true, "savedQuery": true, "savedQueryLanguage": true, "savedFilterValues": true, "savedDateRange": true, "containers": true}
	for k := range d {
		if !allowed[k] {
			bad("unknown dashboard key %q", k)
		}
	}
	var name string
	if json.Unmarshal(d["name"], &name) != nil || name == "" {
		bad("name must be a non-empty string")
	}
	var tags []string
	if json.Unmarshal(d["tags"], &tags) != nil {
		bad("tags must be an array of strings")
	}

	var tiles []struct {
		ID     string          `json:"id"`
		X      *int            `json:"x"`
		Y      *int            `json:"y"`
		W      *int            `json:"w"`
		H      *int            `json:"h"`
		Config json.RawMessage `json:"config"`
	}
	if json.Unmarshal(d["tiles"], &tiles) != nil {
		return append(errs, "tiles must be an array")
	}
	if len(tiles) == 0 || len(tiles) > maxTiles {
		bad("tiles = %d, want 1..%d", len(tiles), maxTiles)
	}
	ids := map[string]bool{}
	for i, t := range tiles {
		at := fmt.Sprintf("tiles[%d]", i)
		if t.ID == "" || ids[t.ID] {
			bad("%s: id %q is empty or duplicated", at, t.ID)
		}
		ids[t.ID] = true
		if t.X == nil || t.Y == nil || t.W == nil || t.H == nil {
			bad("%s: x, y, w and h are all required", at)
			continue
		}
		if *t.X < 0 || *t.W <= 0 || *t.X+*t.W > gridWidth || *t.H <= 0 {
			bad("%s: x=%d w=%d h=%d leaves the %d-column grid", at, *t.X, *t.W, *t.H, gridWidth)
		}
		var c map[string]json.RawMessage
		if json.Unmarshal(t.Config, &c) != nil {
			bad("%s: config must be an object", at)
			continue
		}
		var src, dt string
		_ = json.Unmarshal(c["source"], &src)
		_ = json.Unmarshal(c["displayType"], &dt)
		if !regexp.MustCompile("^(" + sourceKind + ")$").MatchString(src) {
			bad("%s: source %q is not one of the pre-created sources (%s)", at, src, sourceKind)
		}
		if !displayTypes[dt] {
			bad("%s: displayType %q", at, dt)
		}
		var gran string
		if g, ok := c["granularity"]; ok && json.Unmarshal(g, &gran) == nil && gran != "auto" && !intervalRe.MatchString(gran) {
			bad("%s: granularity %q", at, gran)
		}
		var wl string
		if json.Unmarshal(c["whereLanguage"], &wl) != nil || (wl != "sql" && wl != "lucene") {
			bad("%s: whereLanguage must be sql or lucene", at)
		}
		if _, ok := c["where"]; !ok {
			bad("%s: where is required (it may be empty)", at)
		}
		if dt == "search" {
			var sel string
			if json.Unmarshal(c["select"], &sel) != nil {
				bad("%s: a search tile's select is a column list string", at)
			}
			continue
		}
		var sel []map[string]any
		if json.Unmarshal(c["select"], &sel) != nil || len(sel) == 0 {
			bad("%s: select must be a non-empty array", at)
		}
		for j, s := range sel {
			fn, _ := s["aggFn"].(string)
			if !aggFns[fn] {
				bad("%s.select[%d]: aggFn %q", at, j, fn)
			}
			if _, ok := s["valueExpression"].(string); !ok {
				bad("%s.select[%d]: valueExpression is required (empty for count)", at, j)
			}
			if fn == "quantile" {
				if lv, ok := s["level"].(float64); !ok || lv <= 0 || lv >= 1 {
					bad("%s.select[%d]: quantile needs level in (0,1)", at, j)
				}
			}
			if src == "Metrics" {
				if s["metricName"] == nil || s["metricType"] == nil {
					bad("%s.select[%d]: a metrics series needs metricName and metricType", at, j)
				}
			} else if s["metricName"] != nil {
				bad("%s.select[%d]: metricName on a non-metrics source", at, j)
			}
		}
		if rt, ok := c["seriesReturnType"]; ok {
			if string(rt) != `"ratio"` && string(rt) != `"column"` {
				bad("%s: seriesReturnType %s", at, rt)
			}
			if string(rt) == `"ratio"` && len(sel) != 2 {
				bad("%s: a ratio needs exactly two series", at)
			}
		}
	}

	var filters []map[string]any
	if f, ok := d["filters"]; ok && json.Unmarshal(f, &filters) != nil {
		bad("filters must be an array")
	}
	seen := map[string]bool{}
	for i, f := range filters {
		at := fmt.Sprintf("filters[%d]", i)
		if f["type"] != "QUERY_EXPRESSION" {
			bad("%s: type %v", at, f["type"])
		}
		for _, k := range []string{"id", "name", "expression", "source"} {
			if s, _ := f[k].(string); s == "" {
				bad("%s: %s is required", at, k)
			}
		}
		if v, ok := f["variableName"].(string); ok && !varNameRe.MatchString(v) {
			bad("%s: variableName %q", at, v)
		}
		// validateDashboardFilterOptionUniqueness: one filter per expression
		// and source.
		key := fmt.Sprint(f["expression"], "|", f["source"])
		if seen[key] {
			bad("%s: duplicate filter on %s", at, key)
		}
		seen[key] = true
	}
	return errs
}

func TestRenderedDashboardsMatchThePinnedFormat(t *testing.T) {
	files, err := RenderHyperDX(Build(fixture()))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("nothing rendered")
	}
	for _, f := range files {
		for _, e := range validateHyperDXDashboard(f.Content) {
			t.Errorf("%s: %s", f.File, e)
		}
		if !strings.HasSuffix(f.File, "_gen.json") {
			t.Errorf("%s: generated dashboards end in _gen.json", f.File)
		}
	}
}

func TestValidatorRejectsWhatTheSchemaRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"no name":         `{"name":"","tags":[],"tiles":[]}`,
		"off the grid":    `{"name":"a","tags":[],"tiles":[{"id":"1","x":20,"y":0,"w":8,"h":4,"config":{"source":"Traces","displayType":"number","whereLanguage":"sql","where":"","select":[{"aggFn":"count","valueExpression":""}]}}]}`,
		"bad aggFn":       `{"name":"a","tags":[],"tiles":[{"id":"1","x":0,"y":0,"w":8,"h":4,"config":{"source":"Traces","displayType":"number","whereLanguage":"sql","where":"","select":[{"aggFn":"median","valueExpression":""}]}}]}`,
		"unknown source":  `{"name":"a","tags":[],"tiles":[{"id":"1","x":0,"y":0,"w":8,"h":4,"config":{"source":"Spans","displayType":"number","whereLanguage":"sql","where":"","select":[{"aggFn":"count","valueExpression":""}]}}]}`,
		"unknown key":     `{"name":"a","tags":[],"tiles":[{"id":"1","x":0,"y":0,"w":8,"h":4,"config":{"source":"Traces","displayType":"number","whereLanguage":"sql","where":"","select":[{"aggFn":"count","valueExpression":""}]}}],"alerts":[]}`,
		"quantile no lvl": `{"name":"a","tags":[],"tiles":[{"id":"1","x":0,"y":0,"w":8,"h":4,"config":{"source":"Traces","displayType":"number","whereLanguage":"sql","where":"","select":[{"aggFn":"quantile","valueExpression":"Duration"}]}}]}`,
	} {
		if len(validateHyperDXDashboard([]byte(doc))) == 0 {
			t.Errorf("%s: the validator accepted it", name)
		}
	}
}

// TestRenderedDashboardsPassTheRealSchema runs the provisioner's own reader
// (readDashboardFiles: the real zod schema, tile-colour healing and filter
// uniqueness) from the pinned ClickStack image over the rendered files. It is
// the authority the hand-written validator above only mirrors.
func TestRenderedDashboardsPassTheRealSchema(t *testing.T) {
	if testing.Short() || os.Getenv("RUN_CLICKSTACK_CONTRACT") != "1" {
		t.Skip("boots the ClickStack image (~2 GB); set RUN_CLICKSTACK_CONTRACT=1 outside -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable")
	}
	files, err := RenderHyperDX(Build(fixture()))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.File), f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const script = `const { readDashboardFiles } = require('/app/packages/api/build/tasks/provisionDashboards');
const ds = readDashboardFiles('/dash');
console.log('PARSED=' + ds.length + ' TILES=' + ds.reduce((n, d) => n + d.tiles.length, 0));`
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "node", "-v", dir+":/dash:ro",
		"mirror.gcr.io/clickhouse/clickstack-local:"+PinnedHyperDX, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("running the real reader: %v\n%s", err, out)
	}
	want := fmt.Sprintf("PARSED=%d ", len(files))
	if !strings.Contains(string(out), want) || strings.Contains(string(out), "Skipping invalid") {
		t.Fatalf("the real schema rejected a rendered file (want %q):\n%s", want, out)
	}
}
