package obsquery

import (
	"strings"
	"testing"
	"time"
)

func tp(kind string, series []PanelSeries, where []PanelPredicate, group ...string) Panel {
	return Panel{ID: "p", Kind: kind, Signal: "traces", Series: series, Where: where, GroupBy: group}
}

func TestPanelCompileParameterizesEverything(t *testing.T) {
	h := "x') OR 1=1 --"
	for _, sig := range []string{"traces", "logs"} {
		p := Panel{ID: "p", Kind: "line", Signal: sig, GroupBy: []string{"service.name", "resource.attr." + "k"},
			Series: []PanelSeries{
				{Label: "a", Agg: "count", Where: []PanelPredicate{{Field: "service.name", Op: "eq", Value: h}}},
				{Label: "b", Agg: "count_distinct", Field: "resource.attr.rum.sessionId"},
			},
			Where: []PanelPredicate{
				{Field: "service.name", Op: "in", Values: []string{h, "b"}},
				{Field: "resource.attr.k", Op: "match", Value: h},
				{Field: "resource.attr.k", Op: "prefix", Value: h},
				{Field: "resource.attr.k", Op: "exists"},
			}}
		if sig == "traces" {
			p.Series = append(p.Series, PanelSeries{Label: "p95", Agg: "quantile", Quantile: 0.95, Field: "span.duration"})
			p.Where = append(p.Where, PanelPredicate{Field: "span.attr." + "k", Op: "gte", Value: "400"},
				PanelPredicate{Field: "span.kind", Op: "eq", Value: "server"}, PanelPredicate{Field: "span.status", Op: "in", Values: []string{"error", h}})
		} else {
			p.Where = append(p.Where, PanelPredicate{Field: "log.severity", Op: "gte", Value: "warn"}, PanelPredicate{Field: "log.attr.k", Op: "neq", Value: h})
		}
		st, shape, err := PanelQuery{
			Scope: Scope{Database: "t_org_a", EnvRestricted: true, EnvIDs: []string{h}, Services: []string{h}, Start: t0, End: t0.Add(time.Hour)},
			Panel: p, Filters: []PanelPredicate{{Field: "deployment.environment.name", Op: "eq", Value: h}}, StepSeconds: 60,
		}.Compile()
		if err != nil {
			t.Fatalf("%s: %v", sig, err)
		}
		assertParameterized(t, st, h)
		if !shape.Bucketed || shape.Groups != 2 || shape.Series != len(p.Series) {
			t.Errorf("%s: shape = %+v", sig, shape)
		}
	}
}

func TestPanelShapesAndSQL(t *testing.T) {
	count := []PanelSeries{{Label: "n", Agg: "count"}}
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}

	st, sh, err := PanelQuery{Scope: sc, Panel: tp("number", count, nil)}.Compile()
	if err != nil || sh.Bucketed || strings.Contains(st.SQL, "GROUP BY") {
		t.Errorf("number: %v %+v\n%s", err, sh, st.SQL)
	}
	st, _, err = PanelQuery{Scope: sc, Panel: tp("table", count, nil, "span.name")}.Compile()
	if err != nil || !strings.Contains(st.SQL, "GROUP BY g0") || !strings.Contains(st.SQL, "LIMIT 201") {
		t.Errorf("table: %v\n%s", err, st.SQL)
	}
	st, _, err = PanelQuery{Scope: sc, Panel: tp("line", count, nil), StepSeconds: 300}.Compile()
	if err != nil || !strings.Contains(st.SQL, "GROUP BY bucket") || !strings.Contains(st.SQL, "toStartOfInterval") {
		t.Errorf("line: %v\n%s", err, st.SQL)
	}
	// span.kind / span.status match both spellings the pipeline writes
	st, _, _ = PanelQuery{Scope: sc, Panel: tp("number", count, []PanelPredicate{{Field: "span.kind", Op: "eq", Value: "SERVER"}, {Field: "span.status", Op: "eq", Value: "Error"}})}.Compile()
	if !strings.Contains(st.SQL, "replaceOne(SpanKind, 'SPAN_KIND_'") || !strings.Contains(st.SQL, "replaceOne(StatusCode, 'STATUS_CODE_'") {
		t.Errorf("kind/status normalization missing:\n%s", st.SQL)
	}
	for _, v := range st.Params {
		if v != strings.ToLower(v) {
			t.Errorf("kind/status value %q was not lowercased", v)
		}
	}
}

func TestPanelRejections(t *testing.T) {
	count := []PanelSeries{{Label: "n", Agg: "count"}}
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	for name, p := range map[string]Panel{
		"search":              {Kind: "search", Signal: "logs", Series: count},
		"unknown kind":        tp("pie", count, nil),
		"unknown signal":      {Kind: "line", Signal: "events", Series: count},
		"no series":           tp("line", nil, nil),
		"unknown agg":         tp("line", []PanelSeries{{Agg: "median", Field: "span.duration"}}, nil),
		"agg without field":   tp("line", []PanelSeries{{Agg: "avg"}}, nil),
		"bad quantile":        tp("line", []PanelSeries{{Agg: "quantile", Field: "span.duration", Quantile: 2}}, nil),
		"ratio of 3":          {Kind: "line", Signal: "traces", Ratio: true, Series: []PanelSeries{{Agg: "count"}, {Agg: "count"}, {Agg: "count"}}},
		"4 group by":          tp("table", count, nil, "service.name", "span.name", "span.kind", "span.status"),
		"unknown field":       tp("line", count, []PanelPredicate{{Field: "span.nope", Op: "eq", Value: "x"}}),
		"log field on traces": tp("line", count, []PanelPredicate{{Field: "log.body", Op: "eq", Value: "x"}}),
		"unknown op":          tp("line", count, []PanelPredicate{{Field: "span.name", Op: "like", Value: "x"}}),
		"gte non-number":      tp("line", count, []PanelPredicate{{Field: "span.duration", Op: "gte", Value: "fast"}}),
		"empty attr key":      tp("line", count, []PanelPredicate{{Field: "span.attr.", Op: "exists"}}),
		"in without values":   tp("line", count, []PanelPredicate{{Field: "span.name", Op: "in"}}),
		"huge regex":          tp("line", count, []PanelPredicate{{Field: "span.name", Op: "match", Value: strings.Repeat("a", 300)}}),
		"bad severity":        {Kind: "line", Signal: "logs", Series: count, Where: []PanelPredicate{{Field: "log.severity", Op: "eq", Value: "loud"}}},
		"bad group field":     tp("table", count, nil, "span.nope"),
	} {
		_, _, err := PanelQuery{Scope: sc, Panel: p, StepSeconds: 60}.Compile()
		if !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	// step / range guards
	if _, _, err := (PanelQuery{Scope: sc, Panel: tp("line", count, nil), StepSeconds: 1}).Compile(); !isInvalid(err) {
		t.Errorf("1s step over 1h accepted: %v", err)
	}
	long := sc
	long.End = long.Start.Add(8 * 24 * time.Hour)
	if _, _, err := (PanelQuery{Scope: long, Panel: tp("number", count, nil)}).Compile(); !isInvalid(err) {
		t.Errorf("8d traces panel accepted")
	}
	if d, err := PanelMaxRange("metrics"); err != nil || d != MaxRangeMetrics {
		t.Errorf("metrics range = %v, %v", d, err)
	}
}

func TestMetricQueryWithPanelPredicates(t *testing.T) {
	h := "x') OR 1=1 --"
	st, err := MetricQuery{
		Scope: scope(), Name: "m", Info: MetricInfo{Type: MetricSum, Monotonic: true}, Agg: AggIncrease, GroupBy: []string{"service.name"}, StepSeconds: 60,
		Where: []PanelPredicate{{Field: "metric.attr.k", Op: "eq", Value: h}, {Field: "service.name", Op: "in", Values: []string{h}}},
	}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, h)
	if _, err := (MetricQuery{Scope: scope(), Name: "m", Info: MetricInfo{Type: MetricGauge}, Agg: AggAvg, StepSeconds: 60,
		Where: []PanelPredicate{{Field: "span.name", Op: "eq", Value: "x"}}}).Compile(); !isInvalid(err) {
		t.Errorf("a traces field in a metrics panel: %v", err)
	}
}
