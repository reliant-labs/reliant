package dashspec

import (
	"encoding/json"
	"strings"
	"testing"
)

const oneCount = `"series":[{"label":"n","agg":"count"}]`

func TestValidationAcceptsDisplayStylesAndTextPanels(t *testing.T) {
	ok := []struct{ name, src string }{
		{"area line", onePanel(`"kind":"line","signal":"traces","display":{"style":"area"},` + twoCounts)},
		{"stacked area", onePanel(`"kind":"line","signal":"traces","groupBy":["service.name"],"display":{"style":"area","stack":true,"topN":8},` + oneCount)},
		{"stacked bars over time", onePanel(`"kind":"line","signal":"logs","display":{"style":"bars","stack":true},` + twoCounts)},
		{"line with a top-N", onePanel(`"kind":"line","signal":"traces","groupBy":["span.name"],"display":{"topN":5},` + oneCount)},
		{"explicit default line", onePanel(`"kind":"line","signal":"traces","display":{"style":"line"},` + oneCount)},
		{"horizontal bars", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"horizontal":true},` + twoCounts)},
		{"stacked horizontal bars", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"bar","stack":true,"horizontal":true},` + twoCounts)},
		{"pie", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"pie"},` + oneCount)},
		{"donut with a top-N", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"donut","topN":6},` + oneCount)},
		{"gauge with a max", onePanel(`"kind":"number","signal":"traces","display":{"style":"gauge","max":500},` + oneCount)},
		{"gauge with a range", onePanel(`"kind":"number","signal":"traces","display":{"style":"gauge","min":-10,"max":10},` + oneCount)},
		{"percent gauge with no max", onePanel(`"kind":"number","signal":"traces","unit":"percent","formula":"A / B","display":{"style":"gauge"},` + twoCounts)},
		{"stat", onePanel(`"kind":"number","signal":"traces","display":{"style":"stat"},` + oneCount)},
		{"sql funnel", onePanel(`"kind":"sql","sql":"SELECT step, users FROM t","display":{"style":"funnel"}`)},
		{"sql stacked area", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"area","stack":true}`)},
		{"text", onePanel(`"kind":"text","markdown":"## Delivery\nSee [the runbook](https://example.com).","width":24,"height":2`)},
		{"text in a section", `{"id":"x","title":"X","sections":[{"id":"s","title":"S"}],"panels":[{"id":"p","title":"P","section":"s","kind":"text","markdown":"hi"}]}`},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDashboard("f.json", []byte(tc.src)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
}

func TestValidationRefusesMisusedDisplayAndTextPanels(t *testing.T) {
	bad := []struct{ name, src, path, want string }{
		{"display on a table", onePanel(`"kind":"table","signal":"traces","display":{"style":"bar"},` + oneCount), "display", `a table panel is drawn one way and takes no "display"`},
		{"display on a funnel", onePanel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"]},"display":{"style":"bar"}`), "display", `takes no "display"`},
		{"display not an object", onePanel(`"kind":"line","signal":"traces","display":"area",` + oneCount), "display", "must be an object"},
		{"unknown display field", onePanel(`"kind":"line","signal":"traces","display":{"legend":"table"},` + oneCount), "display.legend", `unknown field "legend"`},
		{"pie on a line", onePanel(`"kind":"line","signal":"traces","display":{"style":"pie"},` + oneCount), "display.style", `"pie" is not one of: line, area, bars`},
		{"area on a bar", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"area"},` + oneCount), "display.style", `"area" is not one of: bar, pie, donut`},
		{"funnel on a line", onePanel(`"kind":"line","signal":"traces","display":{"style":"funnel"},` + oneCount), "display.style", "is not one of"},
		{"stacked lines", onePanel(`"kind":"line","signal":"traces","display":{"stack":true},` + twoCounts), "display.stack", "stacked lines hide"},
		{"stacked pie", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"pie","stack":true},` + oneCount), "display.stack", "no bars to stack"},
		{"stacked number", onePanel(`"kind":"number","signal":"traces","display":{"stack":true},` + oneCount), "display.stack", "nothing to stack"},
		{"stack not a bool", onePanel(`"kind":"line","signal":"traces","display":{"style":"area","stack":"yes"},` + oneCount), "display.stack", "must be true or false"},
		{"horizontal line", onePanel(`"kind":"line","signal":"traces","display":{"horizontal":true},` + oneCount), "display.horizontal", "only a bar panel drawn as bars"},
		{"horizontal donut", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"donut","horizontal":true},` + oneCount), "display.horizontal", "only a bar panel drawn as bars"},
		{"gauge without a max", onePanel(`"kind":"number","signal":"traces","display":{"style":"gauge"},` + oneCount), "display", `a gauge needs "max"`},
		{"gauge with min over max", onePanel(`"kind":"number","signal":"traces","display":{"style":"gauge","min":5,"max":1},` + oneCount), "display.min", "must be below its max"},
		{"gauge with a negative max", onePanel(`"kind":"number","signal":"traces","display":{"style":"gauge","max":-1},` + oneCount), "display.max", "must be above its min"},
		{"max on a stat", onePanel(`"kind":"number","signal":"traces","display":{"max":5},` + oneCount), "display.max", `only a gauge takes "max"`},
		{"min on a line", onePanel(`"kind":"line","signal":"traces","display":{"min":0},` + oneCount), "display.min", `only a gauge takes "min"`},
		{"topN on bars", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"topN":3},` + oneCount), "display.topN", `keeps its top groups with "limit"`},
		{"topN on a number", onePanel(`"kind":"number","signal":"traces","display":{"topN":3},` + oneCount), "display.topN", "has none to fold"},
		{"topN on a sql funnel", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"funnel","topN":3}`), "display.topN", "has none to fold"},
		{"zero topN", onePanel(`"kind":"line","signal":"traces","display":{"topN":0},` + oneCount), "display.topN", "from 1 to 50"},
		{"topN over the cap", onePanel(`"kind":"line","signal":"traces","display":{"topN":51},` + oneCount), "display.topN", "from 1 to 50"},
		{"pie of two series", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"pie"},` + twoCounts), "series", "takes exactly 1 series, has 2"},
		{"markdown on a line", onePanel(`"kind":"line","signal":"traces","markdown":"hi",` + oneCount), "markdown", `only a text panel takes "markdown"`},
		{"text without markdown", onePanel(`"kind":"text"`), "", `a text panel needs "markdown"`},
		{"empty markdown", onePanel(`"kind":"text","markdown":"  "`), "markdown", "must not be empty"},
		{"markdown not a string", onePanel(`"kind":"text","markdown":7`), "markdown", "must be a string"},
		{"markdown too long", onePanel(`"kind":"text","markdown":"` + strings.Repeat("x", MaxMarkdownBytes+1) + `"`), "markdown", "over the 10000"},
		{"text with a signal", onePanel(`"kind":"text","signal":"traces","markdown":"hi"`), "signal", `a text panel takes no "signal"`},
		{"text with series", onePanel(`"kind":"text","markdown":"hi",` + oneCount), "series", `a text panel takes no "series"`},
		{"text with a display", onePanel(`"kind":"text","markdown":"hi","display":{"style":"line"}`), "display", `a text panel takes no "display"`},
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

func TestDisplayAndMarkdownRoundTripAndStayOutOfOtherPanels(t *testing.T) {
	src := onePanel(`"kind":"number","signal":"traces","display":{"style":"gauge","min":0,"max":250},` + oneCount)
	d, err := ParseDashboard("f.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	disp := d.Panels[0].Display
	if disp == nil || disp.Style != StyleGauge || disp.Min == nil || *disp.Min != 0 || disp.Max == nil || *disp.Max != 250 {
		t.Fatalf("decoded display %+v", disp)
	}
	out, err := json.Marshal(d.Panels[0])
	if err != nil {
		t.Fatal(err)
	}
	// A min of zero is a value the author wrote, not an absent one.
	if !strings.Contains(string(out), `"display":{"style":"gauge","min":0,"max":250}`) {
		t.Errorf("marshalled %s", out)
	}

	text, err := ParseDashboard("f.json", []byte(onePanel(`"kind":"text","markdown":"# Hi"`)))
	if err != nil {
		t.Fatal(err)
	}
	if p := text.Panels[0]; p.Kind != KindText || p.Markdown != "# Hi" || p.Signal != "" {
		t.Fatalf("decoded %+v", p)
	}

	plain, err := json.Marshal(Panel{ID: "p", Title: "P", Kind: KindLine, Signal: Traces})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"display"`, `"markdown"`} {
		if strings.Contains(string(plain), field) {
			t.Errorf("a panel that sets no %s marshals one: %s", field, plain)
		}
	}
}

func TestDisplayStylesNameEachKindsDefaultFirst(t *testing.T) {
	for kind, want := range map[PanelKind]DisplayStyle{KindLine: StyleLine, KindBar: StyleBar, KindNumber: StyleStat, KindSQL: StyleLine} {
		if got := DisplayStyles(kind); len(got) == 0 || got[0] != want {
			t.Errorf("DisplayStyles(%s) = %v, want %s first", kind, got, want)
		}
	}
	for _, kind := range []PanelKind{KindTable, KindSearch, KindHeatmap, KindIssues, KindSessions, KindFunnel, KindPostgres, KindText} {
		if got := DisplayStyles(kind); got != nil {
			t.Errorf("DisplayStyles(%s) = %v, want none", kind, got)
		}
	}
	// The copy is the caller's: changing it changes nothing here.
	DisplayStyles(KindLine)[0] = "x"
	if DisplayStyles(KindLine)[0] != StyleLine {
		t.Error("DisplayStyles returned the package's own slice")
	}
}

func TestTextPanelIsAConsoleKind(t *testing.T) {
	if !IsConsoleKind(KindText) {
		t.Error("HyperDX must skip a text panel")
	}
}
