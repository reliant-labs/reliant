package obsquery

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func exemplarQuery() MetricExemplars {
	return MetricExemplars{Scope: scope(), Name: "rpc.server.duration", Kind: CatalogHistogram, Limit: 20}
}

func TestMetricExemplarsCompile(t *testing.T) {
	const hostile = "x') OR 1=1 --"
	min := 0.25
	q := exemplarQuery()
	q.Name = hostile
	q.MinValue = &min
	q.Where = []PanelPredicate{{Field: "metric.attr.procedure", Op: "eq", Value: hostile}}
	q.Group = map[string]string{"service.name": "api", "metric.attr.code": hostile}
	q.From, q.To = t0.Add(10*time.Minute), t0.Add(20*time.Minute)
	q.Limit = 7
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, hostile)
	for _, frag := range []string{
		"t_org_a.otel_metrics_histogram", "ARRAY JOIN", "Exemplars.TraceId", "Exemplars.SpanId", "Exemplars.Value",
		"Exemplars.TimeUnix", "Exemplars.FilteredAttributes", "ORDER BY", "DESC", "LIMIT 1 BY", "LIMIT 7",
	} {
		if !strings.Contains(st.SQL, frag) {
			t.Errorf("missing %q:\n%s", frag, st.SQL)
		}
	}
	for _, want := range []int64{q.From.Unix(), q.To.Unix()} {
		if !hasValue(st.Params, strconv.FormatInt(want, 10)) {
			t.Errorf("window bound %d is not bound: %v", want, st.Params)
		}
	}
	if !hasValue(st.Params, "0.25") || !hasValue(st.Params, "api") || !hasValue(st.Params, hostile) {
		t.Errorf("min value, group or where value not bound: %v", st.Params)
	}
}

func TestMetricExemplarsEveryKindWithExemplarsCompiles(t *testing.T) {
	for _, k := range []CatalogKind{CatalogGauge, CatalogSum, CatalogHistogram, CatalogExpHistogram} {
		q := exemplarQuery()
		q.Kind = k
		st, err := q.Compile()
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		assertParameterized(t, st)
		if !strings.Contains(st.SQL, "t_org_a."+k.table()) {
			t.Errorf("%s reads the wrong table:\n%s", k, st.SQL)
		}
	}
}

func TestMetricExemplarsDefaultsToTheScopeRangeAndLimit(t *testing.T) {
	q := exemplarQuery()
	q.Limit = 0
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(st.SQL), "LIMIT 20") {
		t.Errorf("default limit is not 20:\n%s", st.SQL)
	}
	if !hasValue(st.Params, strconv.FormatInt(t0.Unix(), 10)) {
		t.Errorf("scope start not bound: %v", st.Params)
	}
	if strings.Contains(st.SQL, "Exemplars.Value >=") {
		t.Errorf("an unset MinValue must add no value condition:\n%s", st.SQL)
	}
}

func TestMetricExemplarsRefusals(t *testing.T) {
	min := math_NaN()
	for name, mut := range map[string]func(*MetricExemplars){
		"no name":         func(q *MetricExemplars) { q.Name = "" },
		"long name":       func(q *MetricExemplars) { q.Name = strings.Repeat("a", 257) },
		"no kind":         func(q *MetricExemplars) { q.Kind = 0 },
		"summary":         func(q *MetricExemplars) { q.Kind = CatalogSummary },
		"limit over 20":   func(q *MetricExemplars) { q.Limit = 21 },
		"negative limit":  func(q *MetricExemplars) { q.Limit = -1 },
		"from after to":   func(q *MetricExemplars) { q.From, q.To = t0.Add(30*time.Minute), t0.Add(10*time.Minute) },
		"from outside":    func(q *MetricExemplars) { q.From, q.To = t0.Add(-time.Hour), t0.Add(10*time.Minute) },
		"to outside":      func(q *MetricExemplars) { q.From, q.To = t0.Add(10*time.Minute), t0.Add(2*time.Hour) },
		"only from":       func(q *MetricExemplars) { q.From = t0.Add(time.Minute) },
		"range over 7d":   func(q *MetricExemplars) { q.Scope.End = q.Scope.Start.Add(8 * 24 * time.Hour) },
		"nan min":         func(q *MetricExemplars) { q.MinValue = &min },
		"4 group keys":    func(q *MetricExemplars) { q.Group = map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"} },
		"unknown group":   func(q *MetricExemplars) { q.Group = map[string]string{"Attributes['x']": "1"} },
		"unknown where":   func(q *MetricExemplars) { q.Where = []PanelPredicate{{Field: "bogus", Op: "eq", Value: "v"}} },
		"too many where":  func(q *MetricExemplars) { q.Where = make([]PanelPredicate, MaxPanelPredicates+1) },
		"long group val":  func(q *MetricExemplars) { q.Group = map[string]string{"service.name": strings.Repeat("a", 1025)} },
		"bad database":    func(q *MetricExemplars) { q.Scope.Database = "a b" },
		"unrestricted dp": func(q *MetricExemplars) { q.Scope.Filters = []AttrFilter{{Key: "nope", Op: "eq", Value: "v"}} },
	} {
		q := exemplarQuery()
		mut(&q)
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
}

func TestMetricExemplarsGroupIsDeterministic(t *testing.T) {
	q := exemplarQuery()
	q.Group = map[string]string{"service.name": "api", "k8s.pod.name": "p1", "k8s.namespace.name": "ns"}
	first, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, _ := q.Compile()
		if !reflect.DeepEqual(first, again) {
			t.Fatal("a map-ordered group made the statement unstable")
		}
	}
}

func TestExemplarsStoredCompile(t *testing.T) {
	ids := []string{"0AF7651916CD43DD8448EB211C80319C", "bb"}
	for _, lookup := range []bool{true, false} {
		st, err := ExemplarsStored{Scope: scope(), TraceIDs: ids, UseLookupTable: lookup}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		assertParameterized(t, st)
		want, other := "otel_traces_trace_id_ts", "otel_traces "
		if !lookup {
			want, other = "t_org_a.otel_traces", "otel_traces_trace_id_ts"
		}
		if !strings.Contains(st.SQL, want) || strings.Contains(st.SQL, other) {
			t.Errorf("lookup=%v reads the wrong table:\n%s", lookup, st.SQL)
		}
		if !hasValue(st.Params, `['0af7651916cd43dd8448eb211c80319c','bb']`) {
			t.Errorf("trace ids are not lowercased and bound as one array: %v", st.Params)
		}
		// +-1h around the window: seconds against the lookup table's DateTime
		// columns, nanoseconds against otel_traces' DateTime64(9) Timestamp.
		unit := func(tm time.Time) int64 {
			if lookup {
				return tm.Unix()
			}
			return tm.UnixNano()
		}
		for _, w := range []int64{unit(t0.Add(-time.Hour)), unit(t0.Add(2 * time.Hour))} {
			if !hasValue(st.Params, strconv.FormatInt(w, 10)) {
				t.Errorf("lookup=%v: the +-1h bound %d is missing: %v", lookup, w, st.Params)
			}
		}
		if !strings.Contains(st.SQL, "GROUP BY") && !strings.Contains(st.SQL, "DISTINCT") {
			t.Errorf("lookup=%v: ids must come back once:\n%s", lookup, st.SQL)
		}
	}
	from, to := t0.Add(10*time.Minute), t0.Add(20*time.Minute)
	st, err := ExemplarsStored{Scope: scope(), TraceIDs: ids, From: from, To: to, UseLookupTable: true}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !hasValue(st.Params, strconv.FormatInt(from.Add(-time.Hour).Unix(), 10)) || !hasValue(st.Params, strconv.FormatInt(to.Add(time.Hour).Unix(), 10)) {
		t.Errorf("explicit window not used: %v", st.Params)
	}
}

func TestExemplarsStoredRefusals(t *testing.T) {
	many := make([]string, MaxExemplars+1)
	for i := range many {
		many[i] = "aa"
	}
	for name, q := range map[string]ExemplarsStored{
		"no ids":       {Scope: scope()},
		"too many ids": {Scope: scope(), TraceIDs: many},
		"non hex id":   {Scope: scope(), TraceIDs: []string{"x') OR 1=1 --"}},
		"empty id":     {Scope: scope(), TraceIDs: []string{""}},
		"long id":      {Scope: scope(), TraceIDs: []string{strings.Repeat("a", 65)}},
		"bad database": {Scope: scopeWith(func(s *Scope) { s.Database = "a b" }), TraceIDs: []string{"aa"}},
		"from after to": {Scope: scope(), TraceIDs: []string{"aa"},
			From: t0.Add(time.Hour), To: t0},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
}

func math_NaN() float64 {
	z := 0.0
	return z / z
}
