package obsquery

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPanelValidateBarHeatmapSQLFormulaAndLimit(t *testing.T) {
	count := []PanelSeries{{Label: "n", Agg: "count"}}
	two := []PanelSeries{{Label: "err", Agg: "count"}, {Label: "all", Agg: "count"}}
	heat := []PanelSeries{{Label: "n", Agg: "count", Field: "span.duration"}}
	ok := map[string]Panel{
		"bar":                {Kind: "bar", Signal: "traces", Series: count, GroupBy: []string{"span.name"}},
		"bar with a limit":   {Kind: "bar", Signal: "logs", Series: count, GroupBy: []string{"service.name"}, Limit: 5},
		"table with a limit": {Kind: "table", Signal: "traces", Series: count, GroupBy: []string{"span.name"}, Limit: 200},
		"heatmap":            {Kind: "heatmap", Signal: "traces", Series: heat},
		"formula line":       {Kind: "line", Signal: "traces", Series: two, Formula: "A / B * 100"},
		"formula number":     {Kind: "number", Signal: "logs", Series: two, Formula: "(A - B) / 2"},
		"sql":                {Kind: "sql", SQL: "SELECT count() FROM otel_logs"},
		"sql with a signal":  {Kind: "sql", Signal: "logs", SQL: "SELECT 1"},
	}
	for name, p := range ok {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]struct {
		p    Panel
		want string
	}{
		"bar without a group":     {Panel{Kind: "bar", Signal: "traces", Series: count}, "needs a group_by"},
		"bar ratio":               {Panel{Kind: "bar", Signal: "traces", Series: two, GroupBy: []string{"span.name"}, Ratio: true}, "ratio applies"},
		"limit on a line":         {Panel{Kind: "line", Signal: "traces", Series: count, Limit: 3}, "only a table or bar"},
		"limit over the cap":      {Panel{Kind: "table", Signal: "traces", Series: count, Limit: 201}, "limit must be"},
		"formula on a table":      {Panel{Kind: "table", Signal: "traces", Series: two, Formula: "A / B"}, "line and number"},
		"formula and ratio":       {Panel{Kind: "line", Signal: "traces", Series: two, Formula: "A / B", Ratio: true}, "not both"},
		"formula missing series":  {Panel{Kind: "line", Signal: "traces", Series: two, Formula: "A / C"}, "C names series 3"},
		"heatmap over metrics":    {Panel{Kind: "heatmap", Signal: "metrics", Series: heat}, "traces or logs"},
		"heatmap of an avg":       {Panel{Kind: "heatmap", Signal: "traces", Series: []PanelSeries{{Label: "n", Agg: "avg", Field: "span.duration"}}}, "agg count"},
		"heatmap grouped":         {Panel{Kind: "heatmap", Signal: "traces", Series: heat, GroupBy: []string{"span.name"}}, "no group_by"},
		"sql with series":         {Panel{Kind: "sql", SQL: "SELECT 1", Series: count}, "takes only its sql"},
		"sql with where":          {Panel{Kind: "sql", SQL: "SELECT 1", Where: []PanelPredicate{{Field: "service.name", Op: "eq", Value: "a"}}}, "takes only its sql"},
		"sql empty":               {Panel{Kind: "sql"}, "needs sql"},
		"sql insert":              {Panel{Kind: "sql", SQL: "INSERT INTO otel_logs SELECT * FROM otel_logs"}, "must start with SELECT"},
		"sql alter":               {Panel{Kind: "sql", SQL: "SELECT 1; ALTER TABLE otel_logs DELETE WHERE 1"}, "more than one statement"},
		"sql system":              {Panel{Kind: "sql", SQL: "SELECT * FROM system.users"}, "system"},
		"sql remote":              {Panel{Kind: "sql", SQL: "SELECT * FROM remote('h', t_org_b.otel_logs)"}, "reads outside"},
		"sql bad signal":          {Panel{Kind: "sql", SQL: "SELECT 1", Signal: "profiles"}, "signal"},
		"sql on a line":           {Panel{Kind: "line", Signal: "traces", Series: count, SQL: "SELECT 1"}, "only a sql panel"},
		"formula on a funnel":     {Panel{Kind: "funnel", Formula: "A", Funnel: &PanelFunnel{Steps: []string{"a", "b"}}}, "takes no series"},
		"sql on sessions":         {Panel{Kind: "sessions", SQL: "SELECT 1"}, "takes no series"},
		"unknown kind still says": {Panel{Kind: "pie", Signal: "traces", Series: count}, "bar, heatmap, sql"},
	}
	for name, tc := range bad {
		err := tc.p.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestBarAndTableKeepTheirLimit(t *testing.T) {
	count := []PanelSeries{{Label: "n", Agg: "count"}}
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	for _, tc := range []struct {
		p    Panel
		rows int
	}{
		{tp("bar", count, nil, "span.name"), DefaultBarRows},
		{Panel{ID: "p", Kind: "bar", Signal: "traces", Series: count, GroupBy: []string{"span.name"}, Limit: 3}, 3},
		{tp("table", count, nil, "span.name"), MaxTableRows},
		{Panel{ID: "p", Kind: "table", Signal: "traces", Series: count, GroupBy: []string{"span.name"}, Limit: 25}, 25},
	} {
		st, sh, err := PanelQuery{Scope: sc, Panel: tc.p}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		if sh.Rows != tc.rows || !strings.Contains(st.SQL, "ORDER BY v0 DESC LIMIT "+itoa(tc.rows+1)) {
			t.Errorf("%s limit %d: rows %d\n%s", tc.p.Kind, tc.p.Limit, sh.Rows, st.SQL)
		}
	}
}

// An ungrouped number with a step compiles to one ROLLUP statement: the bucket
// rows are its sparkline and the grouping(bucket) = 1 row is its value.
func TestNumberSparklineIsOneRollupStatement(t *testing.T) {
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	p := tp("number", []PanelSeries{{Label: "p95", Agg: "quantile", Quantile: 0.95, Field: "span.duration"}}, nil)
	step := SparklineStep(time.Hour)
	if step != 120 {
		t.Fatalf("sparkline step over an hour = %d, want 120", step)
	}
	st, sh, err := PanelQuery{Scope: sc, Panel: p, StepSeconds: step}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !sh.Bucketed || !sh.Total || sh.Groups != 0 || sh.Series != 1 {
		t.Errorf("shape = %+v", sh)
	}
	for _, frag := range []string{"toUInt8(grouping(bucket)) AS total", "GROUP BY ROLLUP(bucket)", "ORDER BY total, bucket"} {
		if !strings.Contains(st.SQL, frag) {
			t.Errorf("missing %q:\n%s", frag, st.SQL)
		}
	}
	// No step, no sparkline: the plain one-row statement.
	_, sh, _ = PanelQuery{Scope: sc, Panel: p}.Compile()
	if sh.Bucketed || sh.Total {
		t.Errorf("number with no step: shape = %+v", sh)
	}
	if SparklineStep(time.Second) != 1 {
		t.Error("a sparkline step is at least a second")
	}
}

func TestHeatmapCompile(t *testing.T) {
	h := "x') OR 1=1 --"
	sc := Scope{Database: "t_org_a", Services: []string{h}, Start: t0, End: t0.Add(time.Hour)}
	for _, sig := range []string{"traces", "logs"} {
		field := "span.duration"
		if sig == "logs" {
			field = "log.attr.bytes"
		}
		p := Panel{ID: "p", Kind: "heatmap", Signal: sig, Series: []PanelSeries{{Label: "n", Agg: "count", Field: field,
			Where: []PanelPredicate{{Field: "service.name", Op: "eq", Value: h}}}}}
		st, sh, err := PanelQuery{Scope: sc, Panel: p, StepSeconds: 60}.Compile()
		if err != nil {
			t.Fatalf("%s: %v", sig, err)
		}
		assertParameterized(t, st, h)
		if !sh.Heatmap || !sh.Bucketed {
			t.Errorf("%s: shape = %+v", sig, sh)
		}
		for _, frag := range []string{"floor(log2(v) * 4)", "isFinite(v)", "GROUP BY bucket, vb", "LIMIT " + itoa(MaxHeatmapCells+1)} {
			if !strings.Contains(st.SQL, frag) {
				t.Errorf("%s: missing %q:\n%s", sig, frag, st.SQL)
			}
		}
		if weighted := strings.Contains(st.SQL, "TraceState"); weighted != (sig == "traces") {
			t.Errorf("%s: span weighting = %v", sig, weighted)
		}
	}
	if _, _, err := (PanelQuery{Scope: sc, Panel: Panel{ID: "p", Kind: "heatmap", Signal: "traces",
		Series: []PanelSeries{{Label: "n", Agg: "count", Field: "span.duration"}}}}).Compile(); err == nil {
		t.Error("a heatmap with no step compiled")
	}
}

func TestHeatmapBounds(t *testing.T) {
	if lo, hi := HeatmapBounds(HeatmapZeroBucket); lo != 0 || hi != 0 {
		t.Errorf("zero bucket = [%v, %v)", lo, hi)
	}
	lo, hi := HeatmapBounds(40) // 2^10 .. 2^10.25
	if lo != 1024 || math.Abs(hi-1024*math.Pow(2, 0.25)) > 1e-9 {
		t.Errorf("bucket 40 = [%v, %v)", lo, hi)
	}
	// Adjacent buckets tile the line.
	if _, h := HeatmapBounds(-3); h != func() float64 { l, _ := HeatmapBounds(-2); return l }() {
		t.Error("buckets -3 and -2 do not meet")
	}
}

func TestSQLPanelBindsTheScopeAsParameters(t *testing.T) {
	h := "x'], ['y"
	sc := Scope{Database: "t_org_a", EnvRestricted: true, EnvIDs: []string{"e1", "e2"},
		Envs:     []Env{{ID: "e1", Name: "prod"}, {ID: "e2", Name: "it's\nstaging"}, {ID: "e3", Name: "dev"}},
		Services: []string{h, `back\slash`}, Start: time.Unix(1700000000, 5), End: time.Unix(1700003600, 0)}
	const sql = "SELECT count() FROM otel_logs WHERE Timestamp >= {from:DateTime64(9)}"
	st, err := SQLPanel{Scope: sc, SQL: sql, StepSeconds: 30}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != sql {
		t.Errorf("the statement was changed: %q", st.SQL)
	}
	want := map[string]string{
		"from":     "1700000000.000000005",
		"to":       "1700003600.000000000",
		"step":     "30",
		"env_ids":  "['e1', 'e2']",
		"envs":     `['prod', 'it\'s\nstaging']`,
		"services": `['x\'], [\'y', 'back\\slash']`,
	}
	for k, v := range want {
		if st.Params[k] != v {
			t.Errorf("%s = %q, want %q", k, st.Params[k], v)
		}
	}
	// Unrestricted: every environment; restricted to none: nothing matches.
	sc.EnvRestricted, sc.EnvIDs = false, nil
	st, _ = SQLPanel{Scope: sc, SQL: sql, StepSeconds: 30}.Compile()
	if st.Params["env_ids"] != "[]" || st.Params["envs"] != "[]" {
		t.Errorf("unrestricted env params = %q %q", st.Params["env_ids"], st.Params["envs"])
	}
	sc.EnvRestricted = true
	st, _ = SQLPanel{Scope: sc, SQL: sql, StepSeconds: 30}.Compile()
	if st.Params["env_ids"] != "['(none)']" {
		t.Errorf("restricted to no environment: env_ids = %q", st.Params["env_ids"])
	}
	if got := unixNanoText(-1); got != "-1.999999999" {
		t.Errorf("unixNanoText(-1) = %q", got)
	}
}

func TestSQLPanelRefusals(t *testing.T) {
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	for name, q := range map[string]SQLPanel{
		"writes":           {Scope: sc, SQL: "INSERT INTO otel_logs SELECT * FROM otel_logs", StepSeconds: 60},
		"ddl":              {Scope: sc, SQL: "DROP DATABASE t_org_a", StepSeconds: 60},
		"settings":         {Scope: sc, SQL: "SELECT 1 SETTINGS readonly = 0", StepSeconds: 60},
		"no step":          {Scope: sc, SQL: "SELECT 1"},
		"bad database":     {Scope: Scope{Database: "t_org_a; DROP", Start: t0, End: t0.Add(time.Hour)}, SQL: "SELECT 1", StepSeconds: 60},
		"cluster function": {Scope: sc, SQL: "SELECT * FROM clusterAllReplicas('default', system.one)", StepSeconds: 60},
	} {
		if _, err := q.Compile(); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
