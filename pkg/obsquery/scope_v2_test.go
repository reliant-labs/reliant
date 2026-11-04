package obsquery

import (
	"strings"
	"testing"
	"time"
)

func scopeWith(mut func(*Scope)) Scope {
	s := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	if mut != nil {
		mut(&s)
	}
	return s
}

// Every allowlisted key compiles as a filter and as a group-by. (Keys are
// constants from the allowlist; a key outside it never reaches the compiler.)
func TestAllowlistedKeysCompile(t *testing.T) {
	for _, key := range AllowedKeys() {
		sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: key, Op: "eq", Value: "v"}} })
		for name, compile := range map[string]func() (Stmt, error){
			"traces": TraceQuery{Scope: sc, Limit: 5}.Compile,
			"logs":   LogQuery{Scope: sc, Limit: 5}.Compile,
			"overview": func() (Stmt, error) {
				return Overview{Scope: sc, GroupBy: []string{key}}.RED()
			},
		} {
			st, err := compile()
			if err != nil {
				t.Errorf("%s: key %q: %v", name, key, err)
				continue
			}
			assertParameterized(t, st)
		}
	}
}

func TestKeysOutsideTheAllowlistAreInvalid(t *testing.T) {
	for _, key := range []string{
		"", "x') OR 1=1 --", "password", "ResourceAttributes", "reliant.org.id", "reliant.source", "span.attr.x",
		"Attributes['a']", strings.Repeat("a", 129), "SERVICE.NAME", "service.name ",
	} {
		sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: key, Op: "eq", Value: "v"}} })
		for name, compile := range map[string]func() (Stmt, error){
			"filter/traces":  TraceQuery{Scope: sc, Limit: 5}.Compile,
			"filter/logs":    LogQuery{Scope: sc, Limit: 5}.Compile,
			"filter/metrics": MetricCatalog{Scope: sc, Limit: 5}.Compile,
			"filter/work":    Workloads{Scope: sc}.Usage,
			"group/overview": func() (Stmt, error) { return Overview{Scope: scopeWith(nil), GroupBy: []string{key}}.RED() },
			"group/traces":   TraceQuery{Scope: scopeWith(nil), GroupBy: []string{key}}.CompileREDGroups,
			"group/metric": func() (Stmt, error) {
				return MetricQuery{Scope: scopeWith(nil), Name: "m", Info: MetricInfo{Type: MetricGauge}, Agg: AggAvg, GroupBy: []string{key}, StepSeconds: 60}.Compile()
			},
			"values": AttrValues{Scope: scopeWith(nil), Key: key, Limit: 10}.Compile,
		} {
			if key == "span.attr.x" && name == "filter/traces" {
				continue // a trace filter takes the open vocabulary (TestOpenAttributeFilters); nothing else does
			}
			if _, err := compile(); !isInvalid(err) {
				t.Errorf("%s: key %q: err = %v, want ErrInvalid", name, key, err)
			}
		}
	}
}

// A metric's own declared attribute is the one extension to the allowlist, and
// only for metrics.
func TestDeclaredMetricKeys(t *testing.T) {
	declared := scopeWith(func(s *Scope) { s.Declared = map[string]bool{"status": true, "pod": true} })
	q := MetricQuery{Scope: declared, Name: "orders_created", Info: MetricInfo{Type: MetricSum, Monotonic: true}, Agg: AggRate, GroupBy: []string{"pod"}, StepSeconds: 60}
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	undeclared := q
	undeclared.Scope = scopeWith(nil)
	if _, err := undeclared.Compile(); !isInvalid(err) {
		t.Errorf("an undeclared metric key accepted: %v", err)
	}
	// declared keys never leak onto other signals
	other := scopeWith(func(s *Scope) {
		s.Declared = map[string]bool{"pod": true}
		s.Filters = []AttrFilter{{Key: "pod", Op: "eq", Value: "x"}}
	})
	if _, err := (TraceQuery{Scope: other, Limit: 5}).Compile(); !isInvalid(err) {
		t.Errorf("a declared metric key accepted as a span filter: %v", err)
	}
	if !NeedsDeclared("pod") || NeedsDeclared("service.name", "metric.attr.pod", "") {
		t.Errorf("NeedsDeclared misclassifies")
	}
}

// Resource keys read the resource only, so a client-supplied data-point or span
// attribute can never spoof identity; the others read their own map first.
func TestResourceKeysIgnoreSignalAttributes(t *testing.T) {
	b := newBuilder(scopeWith(nil))
	e, resource, err := b.keyExpr(SigTraces, "reliant.env.id")
	if err != nil || !resource {
		t.Fatalf("keyExpr: %v %v", err, resource)
	}
	if strings.Contains(e, "SpanAttributes") || !strings.Contains(e, "ResourceAttributes[") {
		t.Errorf("a resource key reads the span's attributes: %s", e)
	}
	e, resource, err = b.keyExpr(SigTraces, "http.route")
	if err != nil || resource || !strings.Contains(e, "SpanAttributes") || !strings.Contains(e, "ResourceAttributes") {
		t.Errorf("a span key must read the span then the resource: %s (%v %v)", e, resource, err)
	}
	e, _, _ = b.keyExpr(SigTraces, "status")
	if !strings.Contains(e, "StatusCode") {
		t.Errorf("status is not the span status: %s", e)
	}
}

func TestEnvironmentRestriction(t *testing.T) {
	cases := map[string]struct {
		mut  func(*Scope)
		want string
		none string
	}{
		"unrestricted":      {mut: nil, none: "reliant.env.id"},
		"one env":           {mut: func(s *Scope) { s.EnvRestricted, s.EnvIDs = true, []string{"e1"} }, want: "ResourceAttributes['reliant.env.id'] = {"},
		"two envs":          {mut: func(s *Scope) { s.EnvRestricted, s.EnvIDs = true, []string{"e1", "e2"} }, want: "has({"},
		"restricted, empty": {mut: func(s *Scope) { s.EnvRestricted = true }, want: "1 = 0"},
	}
	for name, c := range cases {
		st, err := TraceQuery{Scope: scopeWith(c.mut), Limit: 5}.Compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.want != "" && !strings.Contains(st.SQL, c.want) {
			t.Errorf("%s: SQL lacks %q:\n%s", name, c.want, st.SQL)
		}
		if c.none != "" && strings.Contains(st.SQL, c.none) {
			t.Errorf("%s: SQL mentions %q:\n%s", name, c.none, st.SQL)
		}
		assertParameterized(t, st)
	}
	// the env ids travel as parameters
	st, _ := TraceQuery{Scope: scopeWith(func(s *Scope) { s.EnvRestricted, s.EnvIDs = true, []string{"e1' OR 1=1", "e2"} }), Limit: 5}.Compile()
	assertParameterized(t, st, "e1' OR 1=1")
	// GetTrace honours the same restriction
	sp, err := TraceSpans{Scope: scopeWith(func(s *Scope) { s.EnvRestricted, s.EnvIDs = true, []string{"e1"} }), TraceID: "abc"}.Compile()
	if err != nil || !strings.Contains(sp.SQL, "reliant.env.id") {
		t.Errorf("GetTrace ignores the env restriction: %v\n%s", err, sp.SQL)
	}
}

// deployment.environment.name resolves to the Reliant environment's NAME, from
// the env ids in scope, falling back to the attribute the app emitted.
func TestEnvironmentNameResolution(t *testing.T) {
	sc := scopeWith(func(s *Scope) {
		s.Envs = []Env{{ID: "e1", Name: "prod"}, {ID: "e2", Name: "staging"}}
	})
	st, err := Overview{Scope: sc, GroupBy: []string{"deployment.environment.name"}}.RED()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "transform(") || !strings.Contains(st.SQL, "ResourceAttributes['deployment.environment.name']") {
		t.Errorf("no id->name transform with an emitted-attribute fallback:\n%s", st.SQL)
	}
	foundNames := false
	for _, v := range st.Params {
		if strings.Contains(v, "prod") && strings.Contains(v, "staging") {
			foundNames = true
		}
	}
	if !foundNames {
		t.Errorf("environment names not bound as a parameter: %v", st.Params)
	}
	// no known envs: the emitted attribute is used as is
	st, _ = Overview{Scope: scopeWith(nil), GroupBy: []string{"deployment.environment.name"}}.RED()
	if strings.Contains(st.SQL, "transform(") {
		t.Errorf("transform without environments:\n%s", st.SQL)
	}
	// a hostile environment name stays out of the SQL text
	evil := scopeWith(func(s *Scope) { s.Envs = []Env{{ID: "e1", Name: "x') OR 1=1 --"}} })
	st, _ = Overview{Scope: evil, GroupBy: []string{"deployment.environment.name"}}.RED()
	assertParameterized(t, st, "x') OR 1=1 --")
}

func TestFilterOperators(t *testing.T) {
	h := "x') OR 1=1 --"
	for _, f := range []AttrFilter{
		{Key: "http.route", Op: "eq", Value: h}, {Key: "http.route", Value: h}, {Key: "http.route", Op: "neq", Value: h},
		{Key: "http.route", Op: "contains", Value: h}, {Key: "http.route", Op: "in", Values: []string{h, "b"}},
		{Key: "http.route", Op: "exists"}, {Key: "service.name", Op: "exists"}, {Key: "reliant.env.id", Op: "exists"},
		{Key: "status", Op: "in", Values: []string{"ERROR", "ok"}},
	} {
		sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{f} })
		st, err := TraceQuery{Scope: sc, Limit: 5}.Compile()
		if err != nil {
			t.Errorf("%+v: %v", f, err)
			continue
		}
		assertParameterized(t, st, h)
	}
	for name, f := range map[string]AttrFilter{
		"unknown op":        {Key: "http.route", Op: "regex", Value: "x"},
		"in without values": {Key: "http.route", Op: "in"},
		"too many values":   {Key: "http.route", Op: "in", Values: make([]string, MaxPanelValues+1)},
		"contains empty":    {Key: "http.route", Op: "contains"},
		"huge value":        {Key: "http.route", Op: "eq", Value: strings.Repeat("a", maxPanelValueLen+1)},
	} {
		sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{f} })
		if _, err := (TraceQuery{Scope: sc, Limit: 5}).Compile(); !isInvalid(err) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	tooMany := scopeWith(func(s *Scope) {
		for i := 0; i <= MaxScopeFilters; i++ {
			s.Filters = append(s.Filters, AttrFilter{Key: "service.name", Value: "x"})
		}
	})
	if _, err := (TraceQuery{Scope: tooMany, Limit: 5}).Compile(); !isInvalid(err) {
		t.Errorf("%d filters accepted", MaxScopeFilters+1)
	}
}

// Any attribute is filterable when named by the map it lives in: the traces
// of one chat are found by span.attr.chat.id, an app attribute no allowlist
// could anticipate. The key and value stay parameters, the span map is read
// without the resource fallback an allowlisted key gets, and a span filter
// keeps a trace when ANY of its spans matches.
func TestOpenAttributeFilters(t *testing.T) {
	h := "x') OR 1=1 --"
	for _, f := range []AttrFilter{
		{Key: "span.attr.chat.id", Value: h}, {Key: "span.attr." + h, Value: "c"},
		{Key: "span.attr.chat.id", Op: "neq", Value: h}, {Key: "span.attr.chat.id", Op: "in", Values: []string{h, "b"}},
		{Key: "span.attr.chat.id", Op: "exists"}, {Key: "resource.attr.host.name", Value: h},
	} {
		sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{f} })
		st, err := TraceQuery{Scope: sc, Limit: 5}.Compile()
		if err != nil {
			t.Errorf("%+v: %v", f, err)
			continue
		}
		assertParameterized(t, st, h)
	}

	sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: "span.attr.chat.id", Value: "chat-1"}} })
	st, err := TraceQuery{Scope: sc, Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	_, having, _ := strings.Cut(st.SQL, "HAVING")
	if !strings.Contains(having, "countIf(SpanAttributes[") {
		t.Errorf("span.attr filter is not a per-trace HAVING over SpanAttributes:\n%s", st.SQL)
	}
	if strings.Contains(st.SQL, "mapContains(SpanAttributes") {
		t.Errorf("span.attr filter falls back to the resource map:\n%s", st.SQL)
	}
	bound := false
	for _, v := range st.Params {
		bound = bound || v == "chat.id"
	}
	if !bound {
		t.Errorf("the attribute key is not bound as a parameter: %v", st.Params)
	}

	res := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: "resource.attr.host.name", Value: "h"}} })
	st, err = TraceQuery{Scope: res, Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if where, _, _ := strings.Cut(st.SQL, "HAVING"); !strings.Contains(where, "ResourceAttributes[") {
		t.Errorf("resource.attr filter not in WHERE:\n%s", st.SQL)
	}

	for name, key := range map[string]string{
		"another signal's map": "log.attr.chat.id",
		"no key":               "span.attr.",
		"unknown bare key":     "chat.id",
	} {
		sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: key, Value: "x"}} })
		if _, err := (TraceQuery{Scope: sc, Limit: 5}).Compile(); !isInvalid(err) {
			t.Errorf("%s (%q): %v, want ErrInvalid", name, key, err)
		}
	}
}

// A span-level filter keeps a trace when ANY of its spans matches; a resource
// one picks the spans counted.
func TestTraceFilterPlacement(t *testing.T) {
	sc := scopeWith(func(s *Scope) {
		s.Filters = []AttrFilter{{Key: "http.route", Value: "/x"}, {Key: "k8s.workload.name", Value: "api"}}
	})
	st, err := TraceQuery{Scope: sc, Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	where, having, _ := strings.Cut(st.SQL, "HAVING")
	if !strings.Contains(where, "k8s.workload.name") && !strings.Contains(where, "ResourceAttributes[{") {
		t.Errorf("resource filter not in WHERE:\n%s", st.SQL)
	}
	if !strings.Contains(having, "countIf(") || !strings.Contains(having, "SpanAttributes") {
		t.Errorf("span filter not a per-trace HAVING:\n%s", st.SQL)
	}
	// the cursor belongs to the filters
	q := TraceQuery{Scope: sc, Limit: 5}
	other := TraceQuery{Scope: scopeWith(nil), Limit: 5}
	if q.Fingerprint() == other.Fingerprint() {
		t.Errorf("fingerprint ignores scope filters")
	}
	if q.Fingerprint() != (TraceQuery{Scope: sc, Limit: 5}).Fingerprint() {
		t.Errorf("fingerprint unstable")
	}
}

func TestTraceREDGroups(t *testing.T) {
	st, err := TraceQuery{Scope: scopeWith(nil), GroupBy: []string{"reliant.env.id", "rpc.method"}}.CompileREDGroups()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "argMin(toString(") || !strings.Contains(st.SQL, "GROUP BY g0, g1") {
		t.Errorf("groups are not read at the root span:\n%s", st.SQL)
	}
}

// Overview: a span-level group key has no log counterpart, so Logs reports it
// rather than silently counting zero; resource-level keys group logs too.
func TestOverviewGroupingAndLogs(t *testing.T) {
	ov := Overview{Scope: scopeWith(nil), GroupBy: []string{"reliant.env.id"}}
	for name, f := range map[string]func() (Stmt, error){
		"red": ov.RED, "logs": ov.Logs, "trend": func() (Stmt, error) { return ov.Trend(60) },
	} {
		st, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertParameterized(t, st)
		if !strings.Contains(st.SQL, "AS g0") {
			t.Errorf("%s: not grouped:\n%s", name, st.SQL)
		}
	}
	if _, err := (Overview{Scope: scopeWith(nil), GroupBy: []string{"rpc.method"}}).Logs(); !IsNotOnLogs(err) {
		t.Errorf("a span-level group on logs: %v", err)
	}
	// a span-level scope filter must not empty the log counts
	sc := scopeWith(func(s *Scope) {
		s.Filters = []AttrFilter{{Key: "rpc.method", Value: "Get"}, {Key: "k8s.pod.name", Value: "p"}}
	})
	st, err := Overview{Scope: sc}.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, "LogAttributes") {
		t.Errorf("a span-level filter was applied to logs:\n%s", st.SQL)
	}
	if _, err := (Overview{Scope: scopeWith(nil), GroupBy: []string{"a", "b", "c", "d"}}).RED(); !isInvalid(err) {
		t.Errorf("4 group keys accepted")
	}
}

func TestPanelGroupByAllowlistedKey(t *testing.T) {
	p := Panel{ID: "p", Kind: "line", Signal: "traces", Series: []PanelSeries{{Label: "n", Agg: "count"}}, GroupBy: []string{"reliant.env.id", "span.name", "span.attr.k"}}
	st, _, err := PanelQuery{Scope: scopeWith(nil), Panel: p, StepSeconds: 60}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	p.GroupBy = []string{"password"}
	if _, _, err := (PanelQuery{Scope: scopeWith(nil), Panel: p, StepSeconds: 60}).Compile(); !isInvalid(err) {
		t.Errorf("panel group-by on a non-key: %v", err)
	}
	// a scope filter applies to the panel's signal
	sc := scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: "k8s.workload.name", Value: "api"}} })
	p.GroupBy = nil
	st, _, err = PanelQuery{Scope: sc, Panel: p, StepSeconds: 60}.Compile()
	if err != nil || !strings.Contains(st.SQL, "ResourceAttributes[{") {
		t.Errorf("scope filter not applied to the panel: %v\n%s", err, st.SQL)
	}
}

func TestAttrValuesCompile(t *testing.T) {
	h := "x') OR 1=1 --"
	sc := scopeWith(func(s *Scope) {
		s.EnvRestricted, s.EnvIDs = true, []string{"e1"}
		s.Filters = []AttrFilter{{Key: "k8s.workload.name", Value: "api"}, {Key: "k8s.container.name", Value: "app"}}
	})
	st, err := AttrValues{Scope: sc, Key: "k8s.workload.name", Prefix: h, Limit: 20}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, h)
	// the filter on the key being completed is dropped, the others kept
	if n := strings.Count(st.SQL, "k8s.container.name"); n != 0 {
		// the key literal is a parameter; look at params instead
		t.Errorf("container key concatenated:\n%s", st.SQL)
	}
	hasParam := func(v string) bool {
		for _, p := range st.Params {
			if p == v {
				return true
			}
		}
		return false
	}
	if !hasParam("app") {
		t.Errorf("the other filter was dropped: %v", st.Params)
	}
	if hasParam("api") {
		t.Errorf("the filter on the completed key was kept: %v", st.Params)
	}
	if !hasParam(strings.ToLower(h)) {
		t.Errorf("prefix not bound lowercased: %v", st.Params)
	}
	if !strings.Contains(st.SQL, "LIMIT 21") {
		t.Errorf("limit+1 missing:\n%s", st.SQL)
	}
	for name, q := range map[string]AttrValues{
		"no limit":                   {Scope: sc, Key: "service.name"},
		"big limit":                  {Scope: sc, Key: "service.name", Limit: MaxValuesLimit + 1},
		"long prefix":                {Scope: sc, Key: "service.name", Limit: 5, Prefix: strings.Repeat("a", maxValuePrefixLen+1)},
		"metric attr without metric": {Scope: sc, Key: "pod", Signal: SigMetrics, Limit: 5},
		"attr without metric signal": {Scope: sc, Key: "pod", Metric: "m", Limit: 5},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	// a declared attribute of a named metric
	st, err = AttrValues{Scope: sc, Key: "pod", Signal: SigMetrics, Metric: "go.goroutine.count", Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
}

func TestWorkloadStatements(t *testing.T) {
	h := "x') OR 1=1 --"
	sc := scopeWith(func(s *Scope) {
		s.EnvRestricted, s.EnvIDs = true, []string{"e1", "e2"}
		s.Filters = []AttrFilter{{Key: "k8s.namespace.name", Value: h}, {Key: "rpc.method", Value: "x"}}
	})
	q := Workloads{Scope: sc, Workload: h, Container: h}
	for name, f := range map[string]func() (Stmt, error){
		"usage": q.Usage, "restarts": q.Restarts, "terminations": q.Terminations, "warnings": q.LatestWarnings,
		"events": q.Events, "images": q.ImageChanges, "versions": func() (Stmt, error) { return q.Versions([]string{h, "b"}) },
		"series": func() (Stmt, error) { return q.Series(60) }, "restart series": func() (Stmt, error) { return q.RestartSeries(60) },
		"replicas": q.Replicas, "readiness": func() (Stmt, error) { return q.Readiness(60) }, "pods": q.Pods, "pod restarts": q.PodRestarts, "jobs": q.Jobs,
	} {
		st, err := f()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		assertParameterized(t, st, h)
	}
	// the K2 names, exactly
	st, _ := q.Usage()
	hasAll := func(sql string, names ...string) {
		t.Helper()
		for _, n := range names {
			ok := strings.Contains(sql, n)
			for _, p := range st.Params {
				ok = ok || p == n
			}
			if !ok {
				t.Errorf("statement does not use %q", n)
			}
		}
	}
	hasAll(st.SQL, "k8s.workload.name", "k8s.container.name", "k8s.pod.uid", "container.image.name", "container.image.tag", "reliant.env.id")
	for _, m := range UsageMetrics {
		hasAll(st.SQL, m)
	}
	for _, m := range []string{"container.cpu.usage", "container.memory.working_set", "k8s.container.cpu_limit_utilization", "k8s.container.memory_limit_utilization"} {
		hasAll(st.SQL, m)
	}
	// span-level scope filters must not empty infra telemetry
	for _, p := range st.Params {
		if p == "x" {
			t.Errorf("a span-level filter (rpc.method) reached the workload usage query")
		}
	}
	// events honour an event filter
	ev := Workloads{Scope: scopeWith(func(s *Scope) { s.Filters = []AttrFilter{{Key: "k8s.event.reason", Value: "BackOff"}} })}
	est, err := ev.Events()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range est.Params {
		found = found || p == "BackOff"
	}
	if !found {
		t.Errorf("event filter not applied: %v", est.Params)
	}
	// bounds
	if _, err := (Workloads{Scope: sc}).Series(60); !isInvalid(err) {
		t.Errorf("a series without a workload accepted")
	}
	if _, err := (Workloads{Scope: sc, Workload: "w"}).Series(1); !isInvalid(err) {
		t.Errorf("a series with too many points accepted")
	}
	if _, err := (Workloads{Scope: sc}).Readiness(30); !isInvalid(err) {
		t.Errorf("a readiness step below the delivery interval accepted")
	}
	if _, err := (Workloads{Scope: scopeWith(func(s *Scope) { s.End = s.Start.Add(7 * 24 * time.Hour) })}).Readiness(60); !isInvalid(err) {
		t.Errorf("a readiness range with more buckets than the cap accepted")
	}
	if _, err := (Workloads{Scope: sc}).Versions(nil); !isInvalid(err) {
		t.Errorf("versions of nothing accepted")
	}
	if _, err := (Workloads{Scope: scopeWith(func(s *Scope) { s.End = s.Start.Add(8 * 24 * time.Hour) })}).Usage(); !isInvalid(err) {
		t.Errorf("workloads over 7 days accepted")
	}
}

// Span statistics are weighted by the tail sampler's threshold (weight.go): no
// bare count()/countIf()/quantile over spans may remain in a span statement.
func TestSpanStatementsAreSampleWeighted(t *testing.T) {
	server := []PanelPredicate{{Field: "span.kind", Op: "eq", Value: "server"}}
	panel := func(series ...PanelSeries) Panel {
		return Panel{ID: "p", Kind: "line", Signal: "traces", Where: server, Series: series}
	}
	stmts := map[string]func() (Stmt, error){
		"overview red":   Overview{Scope: scopeWith(nil)}.RED,
		"overview trend": func() (Stmt, error) { return Overview{Scope: scopeWith(nil)}.Trend(60) },
		"trace red":      TraceQuery{Scope: scopeWith(nil), Limit: 5}.CompileRED,
		"trace red grp":  TraceQuery{Scope: scopeWith(nil), GroupBy: []string{"reliant.env.id"}}.CompileREDGroups,
		"panel": func() (Stmt, error) {
			st, _, err := PanelQuery{Scope: scopeWith(nil), StepSeconds: 60, Panel: panel(
				PanelSeries{Label: "n", Agg: "count"}, PanelSeries{Label: "a", Agg: "avg", Field: "span.duration"},
				PanelSeries{Label: "q", Agg: "quantile", Quantile: 0.9, Field: "span.duration"},
				PanelSeries{Label: "s", Agg: "sum", Field: "span.duration"})}.Compile()
			return st, err
		},
	}
	for name, f := range stmts {
		st, err := f()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		assertParameterized(t, st)
		if !strings.Contains(st.SQL, "th:") {
			t.Errorf("%s does not read the sampling threshold:\n%s", name, st.SQL)
		}
		for _, bad := range []string{"quantiles(", "quantileIf(", "avgIf(", "sumIf(Duration"} {
			if strings.Contains(st.SQL, bad) {
				t.Errorf("%s still has an unweighted %s:\n%s", name, bad, st.SQL)
			}
		}
	}
	// logs are not sampled: they keep their plain counts
	lp := Panel{ID: "l", Kind: "number", Signal: "logs", Series: []PanelSeries{{Label: "n", Agg: "count"}}}
	st, _, err := PanelQuery{Scope: scopeWith(nil), Panel: lp, StepSeconds: 60}.Compile()
	if err != nil || !strings.Contains(st.SQL, "countIf(") || strings.Contains(st.SQL, "th:") {
		t.Errorf("a logs panel was weighted: %v\n%s", err, st.SQL)
	}
	// the weight is never concatenated from data: the tracestate is a column, the
	// expression a constant.
	if strings.Contains(spanWeightExpr, "{p") {
		t.Errorf("the weight expression binds parameters")
	}
}
