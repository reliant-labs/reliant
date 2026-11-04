package dashspec

import (
	"strings"
	"testing"
	"time"
)

// ── validation ──────────────────────────────────────────────────────────────

func TestValidationFailuresNameFileLineAndColumn(t *testing.T) {
	src := `{
  "id": "checkout",
  "title": "Checkout",
  "bogus": 1,
  "panels": [
    {"id": "a", "title": "A", "kind": "line", "signal": "traces",
     "series": [{"label": "p", "agg": "quantile", "quantile": 1.5, "field": "span.duration"}]},
    {"id": "a", "title": "B", "kind": "pie", "signal": "traces"},
    {"id": "c", "title": "C", "kind": "line", "signal": "metrics",
     "series": [{"label": "m", "agg": "sum", "field": "orders_created"}]},
    {"id": "d", "title": "D", "kind": "number", "signal": "traces",
     "where": [{"field": "metric.attr.x", "op": "eq", "value": "1"}],
     "series": [{"label": "n", "agg": "count"}]}
  ]
}
`
	_, err := ParseDashboard("deploy/observability/dashboards/checkout.json", []byte(src))
	if err == nil {
		t.Fatal("an invalid dashboard validated")
	}
	if len(ProblemsOf(err)) < 6 {
		t.Fatalf("err carries %d problems, want at least 6:\n%v", len(ProblemsOf(err)), err)
	}
	want := map[string]string{
		"bogus":                        "4:3:",
		"panels[0].series[0].quantile": "7:",
		"panels[1].id":                 "8:",
		"panels[1].kind":               "8:",
		"panels[2].series[0]":          "10:",
		"panels[3].where[0].field":     "12:",
	}
	got := err.Error()
	for path, pos := range want {
		line := ""
		for _, l := range strings.Split(got, "\n") {
			if strings.Contains(l, ": "+path+": ") {
				line = l
			}
		}
		if line == "" {
			t.Errorf("no problem reported at %s; all:\n%s", path, got)
			continue
		}
		if !strings.HasPrefix(line, "deploy/observability/dashboards/checkout.json:"+pos) {
			t.Errorf("%s reported as %q, want a position starting %s", path, line, pos)
		}
	}
	for _, frag := range []string{
		`unknown field "bogus"`, "level in (0, 1]", `duplicate panel id "a"`, `"pie" is not one of`,
		`needs "metricType"`, `field "metric.attr.x" is not valid on traces`,
	} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in:\n%s", frag, got)
		}
	}
}

func TestSyntaxErrorsAreLocated(t *testing.T) {
	_, err := ParseDashboard("d.json", []byte("{\n  \"id\": \"x\",\n  \"title\": \n}\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "d.json:4:") {
		t.Fatalf("err = %v, want a syntax error at line 4", err)
	}
	_, err = ParseDashboard("d.json", []byte(`[]`))
	if err == nil || !strings.Contains(err.Error(), "one JSON object") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidationRejectsWhatTheEnginesWouldRefuse(t *testing.T) {
	panel := func(extra string) string {
		return `{"id":"x","title":"X","panels":[{"id":"p","title":"P",` + extra + `}]}`
	}
	cases := []struct{ name, src, want string }{
		{"no panels", `{"id":"x","title":"X","panels":[]}`, "at least one panel"},
		{"bad id", `{"id":"has space","title":"X","panels":[{"id":"p","title":"P","kind":"number","signal":"traces","series":[{"label":"n","agg":"count"}]}]}`, "not a valid id"},
		{"too many series", panel(`"kind":"line","signal":"traces","series":[` + strings.Repeat(`{"label":"a","agg":"count"},`, 10) + `{"label":"z","agg":"count"}]`), "1 to 10 series"},
		{"ratio needs two", panel(`"kind":"line","signal":"traces","ratio":true,"series":[{"label":"a","agg":"count"}]`), "exactly 2 series"},
		{"ratio on a table", panel(`"kind":"table","signal":"traces","ratio":true,"series":[{"label":"a","agg":"count"},{"label":"b","agg":"count"}]`), "ratio applies to a line"},
		{"search with series", panel(`"kind":"search","signal":"logs","columns":["timestamp"],"series":[{"label":"a","agg":"count"}]`), "takes no series"},
		{"search without columns", panel(`"kind":"search","signal":"logs"`), "needs columns"},
		{"bad regex", panel(`"kind":"number","signal":"traces","where":[{"field":"span.name","op":"match","value":"(["}],"series":[{"label":"a","agg":"count"}]`), "RE2"},
		{"gte non number", panel(`"kind":"number","signal":"traces","where":[{"field":"span.duration","op":"gte","value":"slow"}],"series":[{"label":"a","agg":"count"}]`), "number"},
		{"in without values", panel(`"kind":"number","signal":"traces","where":[{"field":"span.name","op":"in"}],"series":[{"label":"a","agg":"count"}]`), "non-empty"},
		{"metric quantile non histogram", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"quantile","quantile":0.9,"field":"m","metricType":"gauge"}]`), "histogram"},
		{"metric count on a sum", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"count","field":"m","metricType":"sum"}]`), "histogram, exp_histogram or summary metricType"},
		{"metric count on a gauge", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"count","field":"m","metricType":"gauge"}]`), "histogram, exp_histogram or summary metricType"},
		{"metric count_distinct", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"count_distinct","field":"m","metricType":"histogram"}]`), "not defined over a metric"},
		{"metric rate on a gauge", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"rate","field":"m","metricType":"gauge"}]`), "not a gauge"},
		{"metric increase on a gauge", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"increase","field":"m","metricType":"gauge"}]`), "not a gauge"},
		{"rate over traces", panel(`"kind":"line","signal":"traces","series":[{"label":"a","agg":"rate","field":"span.duration"}]`), "defined for metrics"},
		{"increase over logs", panel(`"kind":"line","signal":"logs","series":[{"label":"a","agg":"increase","field":"log.body"}]`), "defined for metrics"},
		{"min over traces", panel(`"kind":"line","signal":"traces","series":[{"label":"a","agg":"min","field":"span.duration"}]`), "defined for metrics"},
		{"quantile of 0", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"quantile","quantile":0,"field":"m","metricType":"histogram"}]`), "level in (0, 1]"},
		{"quantile over 1", panel(`"kind":"line","signal":"metrics","series":[{"label":"a","agg":"quantile","quantile":1.5,"field":"m","metricType":"histogram"}]`), "level in (0, 1]"},
		{"sum without field", panel(`"kind":"number","signal":"traces","series":[{"label":"a","agg":"sum"}]`), "needs a \"field\""},
		{"width out of grid", panel(`"kind":"number","signal":"traces","width":30,"series":[{"label":"a","agg":"count"}]`), "grid columns"},
		{"too many group by", panel(`"kind":"line","signal":"traces","groupBy":["service.name","span.name","span.kind","span.status"],"series":[{"label":"a","agg":"count"}]`), "over the 3 allowed"},
		{"unknown annotation", `{"id":"x","title":"X","annotations":[{"source":"deploys"}],"panels":[{"id":"p","title":"P","kind":"number","signal":"traces","series":[{"label":"a","agg":"count"}]}]}`, "is not one of: releases"},
		{"wrong spec version", `{"version":"forge.dashboards/v9","id":"x","title":"X","panels":[{"id":"p","title":"P","kind":"number","signal":"traces","series":[{"label":"a","agg":"count"}]}]}`, "spec version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			if err == nil {
				t.Fatalf("validated; want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestValidationKnowsTheConsoleKindsAndRejectsUnknownOnes(t *testing.T) {
	panel := func(extra string) string {
		return `{"id":"x","title":"X","panels":[{"id":"p","title":"P",` + extra + `}]}`
	}
	ok := []struct{ name, src string }{
		{"issues", panel(`"kind":"issues","signal":"traces"`)},
		{"issues with a scope", panel(`"kind":"issues","signal":"traces","where":[{"field":"resource.attr.rum.sessionId","op":"exists"}]`)},
		{"sessions", panel(`"kind":"sessions","signal":"traces"`)},
		{"sessions with errors", panel(`"kind":"sessions","signal":"traces","hasError":true`)},
		{"funnel", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"]}`)},
		{"funnel full", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b","c"],"window":"7d","by":"user"}`)},
		{"graded series", panel(`"kind":"table","signal":"traces","series":[{"label":"lcp","agg":"quantile","quantile":0.75,"field":"span.attr.lcp","thresholds":{"good":2500,"poor":4000}}]`)},
	}
	for _, tc := range ok {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			if _, err := ParseDashboard("f.json", []byte(tc.src)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}

	bad := []struct{ name, src, want string }{
		{"unknown kind", panel(`"kind":"replay","signal":"traces"`), `"replay" is not one of`},
		{"pie is unknown", panel(`"kind":"pie","signal":"traces"`), `"pie" is not one of`},
		{"issues with series", panel(`"kind":"issues","signal":"traces","series":[{"label":"a","agg":"count"}]`), `takes no "series"`},
		{"sessions with columns", panel(`"kind":"sessions","signal":"traces","columns":["timestamp"]`), `takes no "columns"`},
		{"funnel with group by", panel(`"kind":"funnel","signal":"traces","groupBy":["service.name"],"funnel":{"steps":["a","b"]}`), `takes no "groupBy"`},
		{"issues on logs", panel(`"kind":"issues","signal":"logs"`), "reads browser spans"},
		{"funnel without a definition", panel(`"kind":"funnel","signal":"traces"`), `needs a "funnel"`},
		{"funnel on a line", panel(`"kind":"line","signal":"traces","funnel":{"steps":["a","b"]},"series":[{"label":"a","agg":"count"}]`), `only a funnel panel takes "funnel"`},
		{"funnel on issues", panel(`"kind":"issues","signal":"traces","funnel":{"steps":["a","b"]}`), `only a funnel panel takes "funnel"`},
		{"hasError on issues", panel(`"kind":"issues","signal":"traces","hasError":true`), `only a sessions panel takes "hasError"`},
		{"hasError on a table", panel(`"kind":"table","signal":"traces","hasError":true,"series":[{"label":"a","agg":"count"}]`), `only a sessions panel takes "hasError"`},
		{"hasError not a bool", panel(`"kind":"sessions","signal":"traces","hasError":"yes"`), "must be true or false"},
		{"one step", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a"]}`), "2 to 10 steps"},
		{"eleven steps", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b","c","d","e","f","g","h","i","j","k"]}`), "2 to 10 steps"},
		{"repeated step", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b","a"]}`), `repeats an earlier step`},
		{"empty step", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a"," "]}`), "must not be empty"},
		{"step not a string", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a",7]}`), "must be a string"},
		{"bad window", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"],"window":"1 hour"}`), "not a window"},
		{"zero window", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"],"window":"0h"}`), "not a window"},
		{"bad by", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"],"by":"device"}`), `"device" is not one of: session, user`},
		{"unknown funnel field", panel(`"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"],"order":"any"}`), `unknown field "order"`},
		{"thresholds without poor", panel(`"kind":"table","signal":"traces","series":[{"label":"a","agg":"count","thresholds":{"good":1}}]`), `need both "good" and "poor"`},
		{"thresholds inverted", panel(`"kind":"table","signal":"traces","series":[{"label":"a","agg":"count","thresholds":{"good":9,"poor":3}}]`), "need 0 <= good < poor"},
		{"unknown thresholds field", panel(`"kind":"table","signal":"traces","series":[{"label":"a","agg":"count","thresholds":{"good":1,"poor":2,"ok":3}}]`), `unknown field "ok"`},
	}
	for _, tc := range bad {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			if err == nil {
				t.Fatalf("validated; want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestEveryPanelKindTheSpecDeclaresIsKnownToTheValidator(t *testing.T) {
	// A kind added to spec.go but not to validate.go would be a panel the
	// console is sent and the validator rejects in a user's file.
	for _, k := range []PanelKind{KindNumber, KindLine, KindTable, KindSearch, KindBar, KindHeatmap, KindSQL, KindIssues, KindSessions, KindFunnel, KindPostgres, KindText} {
		var src string
		switch k {
		case KindText:
			src = `"kind":"text","markdown":"# Notes"`
		case KindBar:
			src = `"kind":"bar","signal":"traces","groupBy":["span.name"],"series":[{"label":"a","agg":"count"}]`
		case KindHeatmap:
			src = `"kind":"heatmap","signal":"traces","series":[{"label":"a","agg":"count","field":"span.duration"}]`
		case KindSQL:
			src = `"kind":"sql","sql":"SELECT count() FROM otel_traces"`
		case KindPostgres:
			src = `"kind":"postgres","postgres":{"database":"app","sql":"SELECT count(*) FROM users"}`
		case KindSearch:
			src = `"kind":"search","signal":"logs","columns":["timestamp"]`
		case KindFunnel:
			src = `"kind":"funnel","signal":"traces","funnel":{"steps":["a","b"]}`
		case KindIssues, KindSessions:
			src = `"kind":"` + string(k) + `","signal":"traces"`
		default:
			src = `"kind":"` + string(k) + `","signal":"traces","series":[{"label":"a","agg":"count"}]`
		}
		doc := `{"id":"x","title":"X","panels":[{"id":"p","title":"P",` + src + `}]}`
		if _, err := ParseDashboard("f.json", []byte(doc)); err != nil {
			t.Errorf("kind %q: %v", k, err)
		}
	}
}

func TestMetricSeriesAcceptsEveryStoredKind(t *testing.T) {
	panel := func(series string) string {
		return `{"id":"x","title":"X","panels":[{"id":"p","title":"P","kind":"line","signal":"metrics","series":[` + series + `]}]}`
	}
	for _, kind := range []string{"gauge", "sum", "histogram", "exp_histogram", "summary"} {
		agg := `"avg"`
		if kind == "sum" {
			agg = `"rate"`
		}
		src := panel(`{"label":"a","agg":` + agg + `,"field":"m","metricType":"` + kind + `"}`)
		if _, err := ParseDashboard("f.json", []byte(src)); err != nil {
			t.Errorf("metricType %q refused: %v", kind, err)
		}
	}
	// What the explorer saves for a distribution kind validates: any quantile,
	// a count of observations and their rate.
	for _, kind := range []string{"histogram", "exp_histogram", "summary"} {
		for _, series := range []string{
			`{"label":"a","agg":"quantile","quantile":0.9,"field":"m","metricType":"` + kind + `"}`,
			`{"label":"a","agg":"count","field":"m","metricType":"` + kind + `"}`,
			`{"label":"a","agg":"rate","field":"m","metricType":"` + kind + `"}`,
		} {
			if _, err := ParseDashboard("f.json", []byte(panel(series))); err != nil {
				t.Errorf("%s: %v", series, err)
			}
		}
	}
	for name, c := range map[string]struct{ series, want string }{
		"an unknown kind":            {`{"label":"a","agg":"avg","field":"m","metricType":"exponential"}`, "is not one of"},
		"a quantile of an exp gauge": {`{"label":"a","agg":"quantile","quantile":0.5,"field":"m","metricType":"gauge"}`, "exp_histogram"},
		"a count of a sum":           {`{"label":"a","agg":"count","field":"m","metricType":"sum"}`, "histogram, exp_histogram or summary"},
		"a rate of a gauge":          {`{"label":"a","agg":"rate","field":"m","metricType":"gauge"}`, "not a gauge"},
	} {
		_, err := ParseDashboard("f.json", []byte(panel(c.series)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestDefaultRangeAndMinStep(t *testing.T) {
	sqlPanel := `{"id":"p","title":"P","kind":"sql","sql":"SELECT toDate(Timestamp) AS day, count() AS n FROM product_events GROUP BY day"%s}`
	dash := func(top, panel string) string {
		return `{"id":"x","title":"X"` + top + `,"panels":[` + panel + `]}`
	}
	ok := []struct{ name, src string }{
		{"defaultRange 7d", dash(`,"defaultRange":"7d"`, strings.Replace(sqlPanel, "%s", "", 1))},
		{"defaultRange 30d", dash(`,"defaultRange":"30d"`, strings.Replace(sqlPanel, "%s", "", 1))},
		{"minStep 1d", dash(``, strings.Replace(sqlPanel, "%s", `,"minStep":"1d"`, 1))},
		{"both", dash(`,"defaultRange":"24h"`, strings.Replace(sqlPanel, "%s", `,"minStep":"1h"`, 1))},
	}
	for _, tc := range ok {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			d, err := ParseDashboard("f.json", []byte(tc.src))
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if tc.name == "defaultRange 7d" && d.DefaultRange != "7d" {
				t.Errorf("DefaultRange = %q", d.DefaultRange)
			}
			if tc.name == "minStep 1d" && d.Panels[0].MinStep != "1d" {
				t.Errorf("MinStep = %q", d.Panels[0].MinStep)
			}
		})
	}
	bad := []struct{ name, src, want string }{
		{"defaultRange not a duration", dash(`,"defaultRange":"week"`, strings.Replace(sqlPanel, "%s", "", 1)), "defaultRange: \"week\" is not a duration"},
		{"defaultRange zero", dash(`,"defaultRange":"0d"`, strings.Replace(sqlPanel, "%s", "", 1)), "is not a duration"},
		{"defaultRange too long", dash(`,"defaultRange":"31d"`, strings.Replace(sqlPanel, "%s", "", 1)), "is longer than"},
		{"defaultRange wrong type", dash(`,"defaultRange":7`, strings.Replace(sqlPanel, "%s", "", 1)), "defaultRange"},
		{"minStep not a duration", dash(``, strings.Replace(sqlPanel, "%s", `,"minStep":"daily"`, 1)), "panels[0].minStep"},
		{"minStep on a line panel", dash(``, `{"id":"p","title":"P","kind":"line","signal":"traces","minStep":"1d","series":[{"label":"a","agg":"count"}]}`), "only a sql panel takes \"minStep\""},
		{"minStep on a console panel", dash(``, `{"id":"p","title":"P","kind":"issues","signal":"traces","minStep":"1d"}`), "only a sql panel takes \"minStep\""},
	}
	for _, tc := range bad {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseSpan(t *testing.T) {
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "15m": 15 * time.Minute, "6h": 6 * time.Hour, "7d": 7 * 24 * time.Hour} {
		if got, err := ParseSpan(in); err != nil || got != want {
			t.Errorf("ParseSpan(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "7", "d", "-1d", "1.5d", "1w", "0m", "99999d", "31d"} {
		if _, err := ParseSpan(in); err == nil {
			t.Errorf("ParseSpan(%q) succeeded", in)
		}
	}
}
