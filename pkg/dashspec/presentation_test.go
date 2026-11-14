package dashspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// The presentation fields a renderer draws richer charts from: a heatmap over
// a sql panel's result, a figure's sparkline statement, a default comparison,
// section descriptions and palette colours. Each changes the picture only.

func TestValidationAcceptsPresentationFields(t *testing.T) {
	ok := []struct{ name, src string }{
		{"sql heatmap", onePanel(`"kind":"sql","sql":"SELECT cohort, w1, w2 FROM t","unit":"percent","display":{"style":"heatmap","color":"violet"}`)},
		{"sql figure with a trend", onePanel(`"kind":"sql","sql":"SELECT count() AS n FROM t","trendSql":"SELECT toDate(Timestamp) AS day, count() AS n FROM t GROUP BY day ORDER BY day","display":{"color":"cyan"}`)},
		{"trend with a line style", onePanel(`"kind":"sql","sql":"SELECT 1","trendSql":"SELECT 1","display":{"style":"area"}`)},
		{"series colours on a sql chart", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"bars","stack":true,"colors":{"Signed in":"blue","Anonymous":"slate"}}`)},
		{"series colours on a line", onePanel(`"kind":"line","signal":"traces","display":{"colors":{"errors":"danger","all":"blue"}},` + twoCounts)},
		{"slice colours on a pie", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"pie","colors":{"GET /":"amber"}},` + oneCount)},
		{"one colour on a number", onePanel(`"kind":"number","signal":"traces","display":{"color":"fuchsia"},` + oneCount)},
		{"every palette key", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"colors":{"a":"blue","b":"violet","c":"cyan","d":"amber","e":"fuchsia","f":"slate","g":"danger"}}`)},
		{"default compare period", `{"id":"x","title":"X","defaultCompare":"period","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`},
		{"default compare release", `{"id":"x","title":"X","defaultCompare":"release","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`},
		{"section description", `{"id":"x","title":"X","sections":[{"id":"s","title":"S","description":"Who comes back."}],"panels":[{"id":"p","title":"P","section":"s","kind":"sql","sql":"SELECT 1"}]}`},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDashboard("f.json", []byte(tc.src)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
}

func TestValidationRefusesMisusedPresentationFields(t *testing.T) {
	sql := func(fields string) string { return onePanel(`"kind":"sql","sql":"SELECT 1",` + fields) }
	bad := []struct{ name, src, path, want string }{
		{"heatmap on a line panel", onePanel(`"kind":"line","signal":"traces","display":{"style":"heatmap"},` + oneCount), "panels[0].display.style", `"heatmap" is not one of: line, area, bars`},
		{"stacked heatmap", sql(`"display":{"style":"heatmap","stack":true}`), "panels[0].display.stack", "a heatmap has nothing to stack"},
		{"stacked funnel", sql(`"display":{"style":"funnel","stack":true}`), "panels[0].display.stack", "a funnel has nothing to stack"},
		{"topN on a heatmap", sql(`"display":{"style":"heatmap","topN":3}`), "panels[0].display.topN", "has none to fold"},
		{"series colours on a heatmap", sql(`"display":{"style":"heatmap","colors":{"W1":"blue"}}`), "panels[0].display.colors", `drawn in one colour; set "color"`},
		{"series colours on a funnel", sql(`"display":{"style":"funnel","colors":{"a":"blue"}}`), "panels[0].display.colors", `drawn in one colour`},
		{"raw hex colour", sql(`"display":{"color":"#ff0000"}`), "panels[0].display.color", `"#ff0000" is not one of: blue, violet`},
		{"raw hex series colour", sql(`"display":{"colors":{"a":"#ff0000"}}`), "panels[0].display.colors.a", `is not one of`},
		{"green is not a palette key", sql(`"display":{"colors":{"a":"green"}}`), "panels[0].display.colors.a", `"green" is not one of`},
		{"colour not a string", sql(`"display":{"colors":{"a":1}}`), "panels[0].display.colors.a", "must be a string"},
		{"colours not an object", sql(`"display":{"colors":["blue"]}`), "panels[0].display.colors", "must be an object"},
		{"empty series name", sql(`"display":{"colors":{" ":"blue"}}`), "panels[0].display.colors", "must not be empty"},
		{"long series name", sql(`"display":{"colors":{"` + strings.Repeat("x", maxColorKeyBytes+1) + `":"blue"}}`), "panels[0].display.colors", "longer than 200 bytes"},
		{"too many colours", sql(`"display":{"colors":{` + manyColors(MaxColors+1) + `}}`), "panels[0].display.colors", "over the 50 allowed"},
		{"trend on a line panel", onePanel(`"kind":"line","signal":"traces","trendSql":"SELECT 1",` + oneCount), "panels[0].trendSql", `only a sql panel takes "trendSql"`},
		{"trend on a text panel", onePanel(`"kind":"text","markdown":"hi","trendSql":"SELECT 1"`), "panels[0].trendSql", `a text panel takes no "trendSql"`},
		{"trend that writes", sql(`"trendSql":"DROP TABLE product_events"`), "panels[0].trendSql", "SELECT"},
		{"empty trend", sql(`"trendSql":"  "`), "panels[0].trendSql", "must not be empty"},
		{"trend not a string", sql(`"trendSql":7`), "panels[0].trendSql", "must be a string"},
		{"trend on a funnel", sql(`"trendSql":"SELECT 1","display":{"style":"funnel"}`), "panels[0].trendSql", "drawn as a funnel has none"},
		{"trend on a heatmap", sql(`"trendSql":"SELECT 1","display":{"style":"heatmap"}`), "panels[0].trendSql", "drawn as a heatmap has none"},
		{"unknown compare", `{"id":"x","title":"X","defaultCompare":"week","panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`, "defaultCompare", `"week" is not one of: period, release`},
		{"long section description", `{"id":"x","title":"X","sections":[{"id":"s","title":"S","description":"` + strings.Repeat("x", MaxSectionDescriptionBytes+1) + `"}],"panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`, "sections[0].description", "longer than 500 bytes"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			if err == nil {
				t.Fatalf("validated; want %q at %q", tc.want, tc.path)
			}
			for _, p := range ProblemsOf(err) {
				if p.Path == tc.path && strings.Contains(p.Message, tc.want) {
					return
				}
			}
			t.Errorf("no problem %q at %s; got:\n%v", tc.want, tc.path, err)
		})
	}
}

func manyColors(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `"s` + strings.Repeat("x", i) + `":"blue"`
	}
	return strings.Join(parts, ",")
}

func TestPresentationFieldsRoundTripAndStayOutWhenUnset(t *testing.T) {
	src := `{"id":"x","title":"X","defaultCompare":"period","sections":[{"id":"s","title":"S","description":"D"}],"panels":[` +
		`{"id":"p","title":"P","section":"s","kind":"sql","sql":"SELECT 1","trendSql":"SELECT 2","display":{"color":"violet","colors":{"a":"blue"}}}]}`
	d, err := ParseDashboard("f.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	p := d.Panels[0]
	if d.DefaultCompare != ComparePeriod || d.Sections[0].Description != "D" || p.TrendSQL != "SELECT 2" ||
		p.Display == nil || p.Display.Color != ColorViolet || p.Display.Colors == nil || (*p.Display.Colors)["a"] != ColorBlue {
		t.Fatalf("decoded %+v / %+v", d, p)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"defaultCompare":"period"`, `"description":"D"`, `"trendSql":"SELECT 2"`, `"color":"violet"`, `"colors":{"a":"blue"}`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("marshalled %s, want %s", out, want)
		}
	}

	plain, err := json.Marshal(Dashboard{ID: "x", Title: "X", Sections: []Section{{ID: "s", Title: "S"}}, Panels: []Panel{{ID: "p", Title: "P", Kind: KindSQL, SQL: "SELECT 1", Display: &Display{Style: StyleArea}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"defaultCompare"`, `"description"`, `"trendSql"`, `"color"`, `"colors"`} {
		if strings.Contains(string(plain), field) {
			t.Errorf("a dashboard that sets no %s marshals one: %s", field, plain)
		}
	}
}

// TestDisplayStaysComparable pins the compatibility Display.Colors is a
// pointer for: Display was comparable before it had colours, and forge/pkg is
// semver at major 1, so a consumer's == on it must keep compiling.
func TestDisplayStaysComparable(t *testing.T) {
	if (Display{Style: StyleArea}) == (Display{}) {
		t.Fatal("Displays of different styles compare equal")
	}
	var none *SeriesColors
	if c, ok := none.Color("a"); ok || c != "" {
		t.Errorf("nil SeriesColors named %q", c)
	}
	if c, ok := (&SeriesColors{"a": ColorAmber}).Color("a"); !ok || c != ColorAmber {
		t.Errorf("SeriesColors{a: amber}.Color(a) = %q, %v", c, ok)
	}
}

func TestChartColorsAreThePaletteKeysOnce(t *testing.T) {
	seen := map[ChartColor]bool{}
	for _, c := range ChartColors {
		if seen[c] || c == "" || strings.ContainsAny(string(c), "#( ") {
			t.Errorf("palette key %q is empty, repeated or a raw colour", c)
		}
		seen[c] = true
	}
	if len(ChartColors) != 7 || ChartColors[len(ChartColors)-1] != ColorDanger {
		t.Errorf("ChartColors = %v: six categorical hues, then danger", ChartColors)
	}
}

func TestSQLTakesAHeatmapStyle(t *testing.T) {
	styles := DisplayStyles(KindSQL)
	if styles[0] != StyleLine || styles[len(styles)-1] != StyleHeatmap {
		t.Errorf("DisplayStyles(sql) = %v, want line first and heatmap among them", styles)
	}
}
