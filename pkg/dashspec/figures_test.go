package dashspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// The figure and ranking presentation (product-analytics Overview): a bar
// list, a KPI card, which way a figure is better, a funnel's steps layout, a
// drill link and a Needs-attention panel. Each changes the picture only.

const figureSQL = `"kind":"sql","sql":"SELECT count() AS n FROM product_events"`

// dash is a dashboard of the given panel objects.
func dash(panels ...string) string {
	return `{"id":"x","title":"X","panels":[` + strings.Join(panels, ",") + `]}`
}

func TestValidationAcceptsFigureAndRankingDisplay(t *testing.T) {
	ok := []struct{ name, src string }{
		{"sql bar list", onePanel(`"kind":"sql","sql":"SELECT route, count() AS n FROM t GROUP BY route","display":{"style":"barlist","bar":"n","share":true,"units":{"n":"count","Conversion %":"percent"}}`)},
		{"sql bar list with a per-row trend", onePanel(`"kind":"sql","sql":"SELECT route, count() AS n FROM t GROUP BY route","trendSql":"SELECT toDate(ts) AS day, route, count() FROM t GROUP BY day, route","display":{"style":"barlist"}`)},
		{"bar panel as a bar list on a series", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"barlist","bar":"all"},` + twoCounts)},
		{"sql KPI with units", onePanel(figureSQL + `,"trendSql":"SELECT toDate(ts) AS day, count() FROM t GROUP BY day","display":{"style":"kpi","good":"up","units":{"DAU":"count"}}`)},
		{"number KPI", onePanel(`"kind":"number","signal":"traces","display":{"style":"kpi","good":"down"},` + twoCounts)},
		{"good on a default sql figure", onePanel(figureSQL + `,"display":{"good":"down","color":"slate"}`)},
		{"good on a number", onePanel(`"kind":"number","signal":"traces","display":{"good":"up"},` + oneCount)},
		{"sql funnel in steps", onePanel(`"kind":"sql","sql":"SELECT step, n FROM t","display":{"style":"funnel","layout":"steps"}`)},
		{"funnel kind in steps", onePanel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"]},"display":{"layout":"steps"}`)},
		{"drill", onePanel(figureSQL + `,"drill":{"dashboard":"users","label":"Users","panel":"active"}`)},
		{"drill on a console kind", onePanel(`"kind":"issues","signal":"traces","drill":{"dashboard":"frontend"}`)},
		{"attention watching panels", dash(
			`{"id":"a","title":"A",`+figureSQL+`,"display":{"good":"up"}}`,
			`{"id":"b","title":"B","kind":"number","signal":"traces","display":{"good":"down"},`+oneCount+`}`,
			`{"id":"w","title":"Needs attention","kind":"attention","attention":{"watch":["a","b"],"threshold":0.05}}`)},
		{"attention watching every good figure", dash(
			`{"id":"a","title":"A",`+figureSQL+`,"display":{"good":"up"}}`,
			`{"id":"w","title":"Needs attention","kind":"attention"}`)},
		{"attention with its own figures", dash(
			`{"id":"w","title":"Needs attention","kind":"attention","attention":{"figures":[{"id":"f","title":"Bounce rate",` + figureSQL + `,"unit":"percent","display":{"good":"down"}}]}}`)},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDashboard("f.json", []byte(tc.src)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
}

func TestValidationRefusesMisusedFigureAndRankingDisplay(t *testing.T) {
	good := `{"id":"a","title":"A",` + figureSQL + `,"display":{"good":"up"}}`
	bad := []struct{ name, src, path, want string }{
		{"good sideways", onePanel(figureSQL + `,"display":{"good":"sideways"}`), "display.good", "is not one of: up, down"},
		{"good on a line", onePanel(`"kind":"line","signal":"traces","display":{"good":"up"},` + oneCount), "display.good", "only a number or sql panel"},
		{"good on a heatmap", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"heatmap","good":"up"}`), "display.good", "a heatmap draws no figure"},
		{"good on a bar list", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"barlist","good":"up"}`), "display.good", "a barlist draws no figure"},
		{"bar off a bar list", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"bar":"n"}`), "display.bar", `only a panel drawn as "barlist"`},
		{"empty bar", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"barlist","bar":""}`), "display.bar", "must not be empty"},
		{"bar naming no series", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.name"],"display":{"style":"barlist","bar":"nope"},` + twoCounts), "display.bar", "not one of the panel's series labels"},
		{"share off a bar list", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"bars","share":true}`), "display.share", `only a panel drawn as "barlist"`},
		{"share not a flag", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"barlist","share":"yes"}`), "display.share", "true or false"},
		{"units on a line", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"units":{"n":"count"}}`), "display.units", "own units"},
		{"unknown unit", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"kpi","units":{"n":"furlongs"}}`), "display.units.n", "is not one of"},
		{"stacked bar list", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"barlist","stack":true}`), "display.stack", "a barlist has nothing to stack"},
		{"top-N on a KPI", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"kpi","topN":3}`), "display.topN", "none to fold"},
		{"steps off a funnel", onePanel(`"kind":"sql","sql":"SELECT 1","display":{"style":"bars","layout":"steps"}`), "display.layout", "only a funnel lays out steps"},
		{"unknown layout", onePanel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"]},"display":{"layout":"pyramid"}`), "display.layout", "is not one of: steps"},
		{"KPI on a line", onePanel(`"kind":"line","signal":"traces","display":{"style":"kpi"},` + oneCount), "display.style", "is not one of: line, area, bars"},
		{"drill with no dashboard", onePanel(figureSQL + `,"drill":{"label":"Users"}`), "drill", `missing required field "dashboard"`},
		{"drill to a bad id", onePanel(figureSQL + `,"drill":{"dashboard":"../users"}`), "drill.dashboard", "not a valid dashboard id"},
		{"drill with an unknown field", onePanel(figureSQL + `,"drill":{"dashboard":"users","url":"https://x"}`), "drill.url", "unknown field"},
		{"attention off its kind", onePanel(figureSQL + `,"attention":{"watch":["a"]}`), "attention", "only an attention panel"},
		{"attention with a query", onePanel(`"kind":"attention","sql":"SELECT 1"`), "sql", `takes no "sql"`},
		{"attention with a display", onePanel(`"kind":"attention","display":{"good":"up"}`), "display", `takes no "display"`},
		{"attention over the threshold", onePanel(`"kind":"attention","attention":{"threshold":5}`), "attention.threshold", "at most 1"},
		{"attention with a non-sql figure", onePanel(`"kind":"attention","attention":{"figures":[{"id":"f","title":"F","kind":"number","signal":"traces",` + oneCount + `}]}`),
			"attention.figures[0].kind", "is a sql panel"},
		{"attention figure with no good", onePanel(`"kind":"attention","attention":{"figures":[{"id":"f","title":"F",` + figureSQL + `}]}`),
			"attention.figures[0]", "which way is good"},
		{"attention figure with a bad statement", onePanel(`"kind":"attention","attention":{"figures":[{"id":"f","title":"F","kind":"sql","sql":"DROP TABLE t","display":{"good":"up"}}]}`),
			"attention.figures[0].sql", ""},
		{"attention watching nothing", onePanel(`"kind":"attention"`), "", "watches nothing"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) { wantProblem(t, tc.src, "panels[0]", tc.path, tc.want) })
	}
	// Watch is checked against the dashboard's panels.
	for _, tc := range []struct{ name, src, at, path, want string }{
		{"watching a missing panel", dash(good, `{"id":"w","title":"W","kind":"attention","attention":{"watch":["a","gone"]}}`),
			"panels[1]", "attention.watch[1]", `no panel "gone"`},
		{"watching a neutral panel", dash(`{"id":"n","title":"N",`+figureSQL+`}`, good, `{"id":"w","title":"W","kind":"attention","attention":{"watch":["n"]}}`),
			"panels[2]", "attention.watch[0]", "does not say which way is good"},
		{"a figure repeating a panel id", dash(good, `{"id":"w","title":"W","kind":"attention","attention":{"figures":[{"id":"a","title":"A",`+figureSQL+`,"display":{"good":"up"}}]}}`),
			"panels[1]", "attention.figures[0].id", `duplicate panel id "a"`},
		// Every id a figure reads joins the dashboard's set, so two figures
		// of one panel, or of two panels, cannot share one either, and a
		// panel after them cannot take one.
		{"two figures sharing an id", dash(good, `{"id":"w","title":"W","kind":"attention","attention":{"figures":[`+
			`{"id":"f","title":"F",`+figureSQL+`,"display":{"good":"up"}},{"id":"f","title":"G",`+figureSQL+`,"display":{"good":"up"}}]}}`),
			"panels[1]", "attention.figures[1].id", `duplicate panel id "f"`},
		{"figures of two panels sharing an id", dash(good,
			`{"id":"w","title":"W","kind":"attention","attention":{"figures":[{"id":"f","title":"F",`+figureSQL+`,"display":{"good":"up"}}]}}`,
			`{"id":"v","title":"V","kind":"attention","attention":{"figures":[{"id":"f","title":"G",`+figureSQL+`,"display":{"good":"up"}}]}}`),
			"panels[2]", "attention.figures[0].id", `duplicate panel id "f"`},
		{"a panel after a figure taking its id", dash(good,
			`{"id":"w","title":"W","kind":"attention","attention":{"figures":[{"id":"f","title":"F",`+figureSQL+`,"display":{"good":"up"}}]}}`,
			`{"id":"f","title":"F2",`+figureSQL+`}`),
			"panels[2]", "id", `duplicate panel id "f"`},
	} {
		t.Run(tc.name, func(t *testing.T) { wantProblem(t, tc.src, tc.at, tc.path, tc.want) })
	}
}

// wantProblem fails unless src has a problem at panel+"."+path whose message
// contains want.
func wantProblem(t *testing.T, src, panel, path, want string) {
	t.Helper()
	_, err := ParseDashboard("f.json", []byte(src))
	if err == nil {
		t.Fatalf("validated; want %q at %s.%s", want, panel, path)
	}
	full := panel
	if path != "" {
		full += "." + path
	}
	for _, p := range ProblemsOf(err) {
		if p.Path == full && strings.Contains(p.Message, want) {
			return
		}
	}
	t.Errorf("no problem %q at %s; got:\n%v", want, full, err)
}

func TestFigureDisplayRoundTripsAndStaysComparable(t *testing.T) {
	src := dash(
		`{"id":"a","title":"A",`+figureSQL+`,"display":{"style":"kpi","good":"up","units":{"DAU":"count"}},"drill":{"dashboard":"users","label":"Users"}}`,
		`{"id":"r","title":"R","kind":"sql","sql":"SELECT 1","display":{"style":"barlist","bar":"n","share":true}}`,
		`{"id":"f","title":"F","kind":"sql","sql":"SELECT 1","display":{"style":"funnel","layout":"steps"}}`,
		`{"id":"w","title":"W","kind":"attention","attention":{"watch":["a"],"threshold":0.05,"figures":[{"id":"b","title":"B",`+figureSQL+`,"display":{"good":"down"}}]}}`)
	d, err := ParseDashboard("f.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	a, r, f, w := d.Panels[0], d.Panels[1], d.Panels[2], d.Panels[3]
	if a.Display.Style != StyleKPI || a.Display.Good != GoodUp || a.Drill == nil || a.Drill.Dashboard != "users" {
		t.Errorf("kpi panel decoded as %+v, drill %+v", a.Display, a.Drill)
	}
	if u, ok := a.Display.Units.Unit("DAU"); !ok || u != UnitCount {
		t.Errorf("units decoded as %v", a.Display.Units)
	}
	if r.Display.Style != StyleBarList || r.Display.Bar != "n" || !r.Display.Share || f.Display.Layout != LayoutSteps {
		t.Errorf("bar list %+v, funnel %+v", r.Display, f.Display)
	}
	if w.Attention == nil || len(w.Attention.Figures) != 1 || w.Attention.Figures[0].Display.Good != GoodDown || w.Attention.Threshold != DefaultAttentionThreshold {
		t.Errorf("attention decoded as %+v", w.Attention)
	}
	if !IsConsoleKind(KindAttention) {
		t.Error("an attention panel is drawn by HyperDX")
	}
	out, err := json.Marshal(d.Panels)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"good":"up"`, `"units":{"DAU":"count"}`, `"drill":{"dashboard":"users","label":"Users"}`, `"layout":"steps"`, `"bar":"n","share":true`, `"attention":{"watch":["a"]`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("re-encoded panels lack %s:\n%s", want, out)
		}
	}
	// Display stays comparable: Units is a pointer, as Colors is.
	if *a.Display == (Display{}) {
		t.Error("a decoded display compares equal to the zero display")
	}
}

// An attention panel with no attention object watches every figure with
// display.good, and one whose attention is not an object is a problem, not a
// crash.
func TestAttentionWithoutItsObjectIsJudgedNotCrashed(t *testing.T) {
	good := `{"id":"a","title":"A",` + figureSQL + `,"display":{"good":"up"}}`
	if _, err := ParseDashboard("f.json", []byte(dash(good, `{"id":"w","title":"W","kind":"attention"}`))); err != nil {
		t.Errorf("an attention panel watching every good figure: %v", err)
	}
	wantProblem(t, dash(`{"id":"w","title":"W","kind":"attention"}`), "panels[0]", "", "watches nothing")
	wantProblem(t, dash(good, `{"id":"w","title":"W","kind":"attention","attention":"all"}`), "panels[1]", "attention", "must be an object")
}
