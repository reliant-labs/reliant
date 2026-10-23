package dashspec

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func onePanel(fields string) string {
	return `{"id":"x","title":"X","panels":[{"id":"p","title":"P",` + fields + `}]}`
}

const twoCounts = `"series":[{"label":"errors","agg":"count"},{"label":"all","agg":"count"}]`

func TestValidationAcceptsBarHeatmapSQLFormulaAndLimit(t *testing.T) {
	ok := []struct{ name, src string }{
		{"formula on a line", onePanel(`"kind":"line","signal":"traces","formula":"A / B * 100",` + twoCounts)},
		{"formula on a number", onePanel(`"kind":"number","signal":"traces","formula":"(A - B) / 2",` + twoCounts)},
		{"formula over one series", onePanel(`"kind":"line","signal":"traces","formula":"A * 8","series":[{"label":"bytes","agg":"sum","field":"span.attr.size"}]`)},
		{"bar", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],` + twoCounts)},
		{"bar with a limit", onePanel(`"kind":"bar","signal":"logs","groupBy":["service.name"],"limit":5,"series":[{"label":"n","agg":"count"}]`)},
		{"bar over metrics", onePanel(`"kind":"bar","signal":"metrics","groupBy":["metric.attr.route"],"series":[{"label":"m","agg":"sum","field":"requests","metricType":"sum"}]`)},
		{"table with a limit", onePanel(`"kind":"table","signal":"traces","groupBy":["span.name"],"limit":200,` + twoCounts)},
		{"heatmap of latency", onePanel(`"kind":"heatmap","signal":"traces","unit":"ms","series":[{"label":"requests","agg":"count","field":"span.duration"}]`)},
		{"heatmap of a log attribute", onePanel(`"kind":"heatmap","signal":"logs","where":[{"field":"service.name","op":"eq","value":"api"}],"series":[{"label":"n","agg":"count","field":"log.attr.bytes","where":[{"field":"log.severity","op":"eq","value":"error"}]}]`)},
		{"sql with no signal", onePanel(`"kind":"sql","sql":"SELECT ServiceName, count() AS n FROM otel_traces WHERE Timestamp >= {from:DateTime64(9)} GROUP BY ServiceName"`)},
		{"sql with a signal and a unit", onePanel(`"kind":"sql","signal":"logs","unit":"count","sql":"WITH x AS (SELECT 1) SELECT * FROM x"`)},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDashboard("f.json", []byte(tc.src)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
}

func TestValidationRefusesMisusedBarHeatmapSQLFormulaAndLimit(t *testing.T) {
	bad := []struct{ name, src, path, want string }{
		{"formula on a table", onePanel(`"kind":"table","signal":"traces","formula":"A / B",` + twoCounts), "formula", `applies to a line or number panel, not "table"`},
		{"formula with a ratio", onePanel(`"kind":"line","signal":"traces","ratio":true,"formula":"A / B",` + twoCounts), "formula", "not both"},
		{"formula naming a missing series", onePanel(`"kind":"line","signal":"traces","formula":"A / C",` + twoCounts), "formula", "C names series 3, but the panel has 2"},
		{"formula with no series reference", onePanel(`"kind":"line","signal":"traces","formula":"100",` + twoCounts), "formula", "references no series"},
		{"formula syntax", onePanel(`"kind":"line","signal":"traces","formula":"A / (B",` + twoCounts), "formula", `missing ")"`},
		{"formula by label", onePanel(`"kind":"line","signal":"traces","formula":"errors / all",` + twoCounts), "formula", "unexpected"},
		{"empty formula", onePanel(`"kind":"line","signal":"traces","formula":"",` + twoCounts), "formula", "must not be empty"},
		{"formula not a string", onePanel(`"kind":"line","signal":"traces","formula":7,` + twoCounts), "formula", "must be a string"},
		{"limit on a line", onePanel(`"kind":"line","signal":"traces","limit":5,` + twoCounts), "limit", "only a table or bar panel"},
		{"zero limit", onePanel(`"kind":"table","signal":"traces","groupBy":["span.name"],"limit":0,` + twoCounts), "limit", "from 1 to 200"},
		{"limit over the cap", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"limit":201,` + twoCounts), "limit", "from 1 to 200"},
		{"fractional limit", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"limit":2.5,` + twoCounts), "limit", "whole number"},
		{"bar without a group", onePanel(`"kind":"bar","signal":"traces",` + twoCounts), "", `needs a "groupBy"`},
		{"bar with a ratio", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"ratio":true,` + twoCounts), "ratio", "applies to a line panel"},
		{"heatmap over metrics", onePanel(`"kind":"heatmap","signal":"metrics","series":[{"label":"m","agg":"sum","field":"requests","metricType":"sum"}]`), "signal", "buckets the values of individual events"},
		{"heatmap with two series", onePanel(`"kind":"heatmap","signal":"traces","series":[{"label":"a","agg":"count","field":"span.duration"},{"label":"b","agg":"count","field":"span.duration"}]`), "series", "exactly 1 series"},
		{"heatmap of an average", onePanel(`"kind":"heatmap","signal":"traces","series":[{"label":"a","agg":"avg","field":"span.duration"}]`), "series[0].agg", "counts events"},
		{"heatmap with no field", onePanel(`"kind":"heatmap","signal":"traces","series":[{"label":"a","agg":"count"}]`), "series[0]", "numeric value it buckets"},
		{"heatmap of a name", onePanel(`"kind":"heatmap","signal":"traces","series":[{"label":"a","agg":"count","field":"span.name"}]`), "series[0].field", "is not numeric"},
		{"heatmap with a group", onePanel(`"kind":"heatmap","signal":"traces","groupBy":["span.name"],"series":[{"label":"a","agg":"count","field":"span.duration"}]`), "groupBy", `takes no "groupBy"`},
		{"sql without a statement", onePanel(`"kind":"sql"`), "", `needs "sql"`},
		{"sql that writes", onePanel(`"kind":"sql","sql":"INSERT INTO otel_logs SELECT * FROM otel_logs"`), "sql", "must start with SELECT"},
		{"sql with DDL inside", onePanel(`"kind":"sql","sql":"SELECT 1; DROP TABLE otel_logs"`), "sql", "more than one statement"},
		{"sql reading a url", onePanel(`"kind":"sql","sql":"SELECT * FROM url('http://x', CSV)"`), "sql", "reads outside the owner"},
		{"sql with settings", onePanel(`"kind":"sql","sql":"SELECT 1 SETTINGS max_execution_time = 999"`), "sql", "SETTINGS are applied"},
		{"sql with series", onePanel(`"kind":"sql","sql":"SELECT 1","series":[{"label":"a","agg":"count"}]`), "series", `a sql panel takes no "series"`},
		{"sql with where", onePanel(`"kind":"sql","sql":"SELECT 1","where":[{"field":"service.name","op":"eq","value":"a"}]`), "where", `takes no "where"`},
		{"sql with a bad signal", onePanel(`"kind":"sql","signal":"profiles","sql":"SELECT 1"`), "signal", "is not one of"},
		{"sql too long", onePanel(`"kind":"sql","sql":"SELECT '` + strings.Repeat("x", MaxSQLBytes) + `'"`), "sql", "longer than"},
		{"sql on a table", onePanel(`"kind":"table","signal":"traces","sql":"SELECT 1",` + twoCounts), "sql", `only a sql panel takes "sql"`},
		{"sql on issues", onePanel(`"kind":"issues","signal":"traces","sql":"SELECT 1"`), "sql", `takes no "sql"`},
		{"limit on a search", onePanel(`"kind":"search","signal":"logs","columns":["timestamp"],"limit":5`), "limit", `takes no "limit"`},
		{"signal still required off sql", onePanel(`"kind":"line",` + twoCounts), "", `missing required field "signal"`},
		{"empty attribute key", onePanel(`"kind":"number","signal":"traces","where":[{"field":"span.attr.","op":"exists"}],` + twoCounts), "where[0].field", "attribute key of 1 to 128"},
		{"long attribute key", onePanel(`"kind":"number","signal":"traces","groupBy":["resource.attr.` + strings.Repeat("k", 129) + `"],` + twoCounts), "groupBy[0]", "attribute key of 1 to 128"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			if err == nil {
				t.Fatalf("validated; want %q at %q", tc.want, tc.path)
			}
			path := "panels[0]"
			if tc.path != "" {
				path += "." + tc.path
			}
			for _, p := range ProblemsOf(err) {
				if p.Path == path && strings.Contains(p.Message, tc.want) {
					return
				}
			}
			t.Errorf("no problem %q at %s; got:\n%v", tc.want, path, err)
		})
	}
}

func TestNewPanelFieldsRoundTripAndKeepExistingOutput(t *testing.T) {
	src := onePanel(`"kind":"sql","sql":"SELECT 1"`)
	d, err := ParseDashboard("f.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	p := d.Panels[0]
	if p.Kind != KindSQL || p.SQL != "SELECT 1" || p.Signal != "" {
		t.Fatalf("decoded %+v", p)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"signal"`) {
		t.Errorf("a sql panel with no signal marshals one: %s", out)
	}
	// A panel that uses none of the new fields marshals exactly as before.
	old := Panel{ID: "p", Title: "P", Kind: KindLine, Signal: Traces, Series: []Series{{Label: "a", Agg: AggCount}}}
	got, _ := json.Marshal(old)
	if want := `{"id":"p","title":"P","kind":"line","signal":"traces","series":[{"label":"a","agg":"count"}]}`; string(got) != want {
		t.Errorf("marshal changed:\n got %s\nwant %s", got, want)
	}
	bar := Panel{ID: "b", Title: "B", Kind: KindBar, Signal: Traces, GroupBy: []string{"span.name"}, Limit: 5, Formula: "", Series: old.Series}
	got, _ = json.Marshal(bar)
	if !strings.Contains(string(got), `"limit":5`) {
		t.Errorf("limit not marshalled: %s", got)
	}
}

func TestFormulaEvaluates(t *testing.T) {
	cases := []struct {
		expr string
		vals []float64
		want float64
	}{
		{"A / B * 100", []float64{3, 4}, 75},
		{"A/B*100", []float64{3, 4}, 75},
		{"A + B * C", []float64{1, 2, 3}, 7},
		{"(A + B) * C", []float64{1, 2, 3}, 9},
		{"A - B - C", []float64{10, 3, 2}, 5},
		{"A / B / C", []float64{12, 3, 2}, 2},
		{"-A + 1", []float64{4}, -3},
		{"--A", []float64{4}, 4},
		{"A * -1", []float64{4}, -4},
		{"A * 1e3 + .5", []float64{2}, 2000.5},
		{"A / B", []float64{1, 0}, 0},
		{"(A - B) / B * 100", []float64{5, 0}, 0},
		{"A / (B - B)", []float64{1, 2}, 0},
		{"J", []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 9},
	}
	for _, tc := range cases {
		f, err := ParseFormula(tc.expr, len(tc.vals))
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got := f.Eval(tc.vals); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%q over %v = %v, want %v", tc.expr, tc.vals, got, tc.want)
		}
	}
	f, _ := ParseFormula("A * B", 2)
	if got := f.Eval([]float64{math.MaxFloat64, 10}); got != 0 {
		t.Errorf("an overflowing product = %v, want 0", got)
	}
}

func TestFormulaStringIsCanonicalAndReparsesToTheSameValue(t *testing.T) {
	cases := map[string]string{
		"A/B*100":         "A / B * 100",
		"((A))":           "A",
		"A-(B-C)":         "A - (B - C)",
		"A-(B+C)":         "A - (B + C)",
		"(A-B)-C":         "A - B - C",
		"A/(B*C)":         "A / (B * C)",
		"(A+B)*(C-1)":     "(A + B) * (C - 1)",
		"-(A+B)":          "-(A + B)",
		"-A*B":            "-A * B",
		"  A  +  2.50 ":   "A + 2.50",
		"A*(B/C)":         "A * (B / C)",
		"(A + B) + C - 1": "A + B + C - 1",
	}
	vals := []float64{7, 3, 2}
	for in, want := range cases {
		f, err := ParseFormula(in, 3)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := f.String(); got != want {
			t.Errorf("%q.String() = %q, want %q", in, got, want)
		}
		g, err := ParseFormula(f.String(), 3)
		if err != nil {
			t.Fatalf("%q does not reparse: %v", f.String(), err)
		}
		if f.Eval(vals) != g.Eval(vals) {
			t.Errorf("%q and its canonical %q evaluate differently", in, f.String())
		}
	}
}

func TestFormulaRefusals(t *testing.T) {
	cases := map[string]string{
		"":          "empty",
		"   ":       "empty",
		"A +":       "ends where",
		"A B":       "unexpected",
		"AB":        "one capital letter",
		"a / b":     "unexpected",
		"K":         "K names series 11",
		"A % B":     "unexpected",
		"1e999 * A": "not a number",
		"1..2 + A":  "not a number",
		"(A":        `missing ")"`,
		"A)":        "unexpected",
		"2 * 3":     "references no series",
		strings.Repeat("(", 40) + "A" + strings.Repeat(")", 40): "nests deeper",
		"A" + strings.Repeat(" + A", 100):                       "longer than 256",
	}
	for expr, want := range cases {
		_, err := ParseFormula(expr, 10)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseFormula(%q) = %v, want an error containing %q", expr, err, want)
		}
	}
}

func TestFormulaRefsAndSeriesRef(t *testing.T) {
	f, err := ParseFormula("C / A * 100 + C", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Refs(); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("Refs = %v, want [0 2]", got)
	}
	if SeriesRef(0) != "A" || SeriesRef(9) != "J" {
		t.Errorf("SeriesRef(0), SeriesRef(9) = %q, %q", SeriesRef(0), SeriesRef(9))
	}
}

func TestCheckSQLIsTheAlertRuleGuard(t *testing.T) {
	if err := CheckSQL("SELECT count() FROM otel_logs WHERE Body = 'drop table'"); err != nil {
		t.Errorf("a string literal containing a keyword was refused: %v", err)
	}
	for sql, want := range map[string]string{
		"":                           "empty",
		"ALTER TABLE x DELETE":       "must start with SELECT",
		"SELECT * FROM system.users": "system database",
		"SELECT 1 FORMAT JSON":       "FORMAT",
	} {
		err := CheckSQL(sql)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckSQL(%q) = %v, want %q", sql, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "alerting:") {
			t.Errorf("CheckSQL(%q) leaks the alerting prefix: %v", sql, err)
		}
	}
}
