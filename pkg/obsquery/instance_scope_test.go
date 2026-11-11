package obsquery

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	instIDCond   = "ResourceAttributes['reliant.instance.id']"
	instUserCond = "ResourceAttributes['reliant.instance.user']"
	instSessions = "SessionId IN (SELECT ResourceAttributes['rum.sessionId'] FROM "
)

// instanceStatements compiles one statement of every resource-attribute family
// (traces, logs, metrics, issues, workloads, exemplars, panels) over sc.
func instanceStatements(sc Scope) map[string]func() (Stmt, error) {
	gauge := MetricInfo{Type: MetricGauge}
	return map[string]func() (Stmt, error){
		"traces":        TraceQuery{Scope: sc, Limit: 5}.Compile,
		"traces/user":   TraceQuery{Scope: sc, Limit: 5, UserID: "u1"}.Compile,
		"traces/red":    TraceQuery{Scope: sc, GroupBy: []string{"service.name"}}.CompileREDGroups,
		"trace":         TraceSpans{Scope: sc, TraceID: "abc"}.Compile,
		"logs":          LogQuery{Scope: sc, Limit: 5}.Compile,
		"overview":      Overview{Scope: sc}.RED,
		"overview/logs": Overview{Scope: sc}.Logs,
		"metric":        MetricQuery{Scope: sc, Name: "m", Info: gauge, Agg: AggAvg, StepSeconds: 60}.Compile,
		"metric/lookup": MetricLookup{Scope: sc, Name: "m"}.Compile,
		"catalog":       MetricCatalog{Scope: sc, Limit: 5}.Compile,
		"exemplars":     MetricExemplars{Scope: sc, Name: "m", Kind: CatalogHistogram, Limit: 5}.Compile,
		"issues":        ServerIssues{Scope: sc, Limit: 5}.Compile,
		"workloads":     Workloads{Scope: sc}.Usage,
		"workloads/log": Workloads{Scope: sc}.LatestWarnings,
		"values":        AttrValues{Scope: sc, Key: "service.name", Limit: 5}.Compile,
		"panel": func() (Stmt, error) {
			st, _, err := PanelQuery{Scope: sc, Panel: Panel{ID: "p", Kind: "number", Signal: "traces", Series: []PanelSeries{{Agg: "count"}}}}.Compile()
			return st, err
		},
	}
}

// browserStatements compiles every read of the hosted browser tables over sc.
func browserStatements(sc Scope) map[string]func() (Stmt, error) {
	panel := func(kind string) func() (Stmt, error) {
		return func() (Stmt, error) {
			p := bp(kind)
			if kind == "funnel" {
				p.Funnel = &PanelFunnel{Steps: []string{"a", "b"}}
			}
			st, _, err := PanelQuery{Scope: sc, Panel: p}.Compile()
			return st, err
		}
	}
	return map[string]func() (Stmt, error){
		"funnel":   panel("funnel"),
		"issues":   panel("issues"),
		"sessions": panel("sessions"),
		"list":     SessionQuery{Scope: sc, Limit: 5}.Compile,
		"lookup":   SessionLookup{Scope: sc, SessionID: "s1"}.Compile,
		"timeline": SessionTimeline{Scope: sc, SessionID: "s1", Limit: 5}.Compile,
		"replay":   ReplayQuery{Scope: sc, SessionID: "s1", MaxBytes: 1024}.Compile,
	}
}

// An instance narrows every resource-attribute read on reliant.instance.id,
// as an environment narrows on reliant.env.id, and the ids travel as
// parameters.
func TestInstanceIDsNarrowEveryFamily(t *testing.T) {
	hostile := "i1' OR 1=1 --"
	for _, c := range []struct {
		name string
		ids  []string
		want string
	}{
		{"one", []string{hostile}, instIDCond + " = {"},
		{"two", []string{hostile, "i2"}, "has({"},
	} {
		sc := scopeWith(func(s *Scope) { s.InstanceIDs = c.ids })
		for name, compile := range instanceStatements(sc) {
			st, err := compile()
			if err != nil {
				t.Errorf("%s/%s: %v", c.name, name, err)
				continue
			}
			if !strings.Contains(st.SQL, c.want) || !strings.Contains(st.SQL, instIDCond) {
				t.Errorf("%s/%s: SQL is not narrowed to the instance (want %q):\n%s", c.name, name, c.want, st.SQL)
			}
			assertParameterized(t, st, hostile)
		}
	}
}

// No instance named is no narrowing: today's statements, byte for byte.
func TestNoInstanceNarrowsNothing(t *testing.T) {
	for name, compile := range instanceStatements(scopeWith(nil)) {
		st, err := compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(st.SQL, "reliant.instance") {
			t.Errorf("%s: an unnarrowed scope mentions the instance:\n%s", name, st.SQL)
		}
	}
	for name, compile := range browserStatements(browserScope()) {
		st, err := compile()
		if err != nil {
			t.Fatalf("browser %s: %v", name, err)
		}
		if strings.Contains(st.SQL, "reliant.instance") {
			t.Errorf("browser %s: an unnarrowed scope mentions the instance:\n%s", name, st.SQL)
		}
	}
}

// InstanceUsers narrows on reliant.instance.user, ANDed with the ids and the
// environments.
func TestInstanceUsersNarrowBesideIDsAndEnvironments(t *testing.T) {
	sc := scopeWith(func(s *Scope) {
		s.EnvRestricted, s.EnvIDs = true, []string{"env-dev"}
		s.InstanceIDs = []string{"i1"}
		s.InstanceUsers = []string{"u1", "u2"}
	})
	for name, compile := range instanceStatements(sc) {
		st, err := compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{envIDExpr, instIDCond + " = {", instUserCond + ")"} {
			if !strings.Contains(st.SQL, want) {
				t.Errorf("%s: SQL lacks %q:\n%s", name, want, st.SQL)
			}
		}
		assertParameterized(t, st)
	}
}

// A local scope and a hosted one narrow an instance with the same predicate:
// the statements differ only in the database they name.
func TestInstanceNarrowsLocalAsHosted(t *testing.T) {
	hosted := scopeWith(func(s *Scope) { s.InstanceIDs, s.InstanceUsers = []string{"i1"}, []string{"u1"} })
	local := hosted
	local.Database, local.Local = LocalDatabase, true
	hs, ls := instanceStatements(hosted), instanceStatements(local)
	for name := range hs {
		h, err := hs[name]()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		l, err := ls[name]()
		if err != nil {
			t.Fatalf("%s local: %v", name, err)
		}
		if got := strings.ReplaceAll(l.SQL, LocalDatabase+".", hosted.Database+"."); got != h.SQL {
			t.Errorf("%s: a local scope narrows differently:\nlocal:  %s\nhosted: %s", name, l.SQL, h.SQL)
		}
	}
}

// The browser tables keep no resource attributes: an instance narrows them to
// the sessions its spans carry, read from a day before the range.
func TestInstanceNarrowsBrowserTablesBySession(t *testing.T) {
	sc := browserScope()
	sc.InstanceIDs = []string{"i1' OR 1=1 --"}
	for name, compile := range browserStatements(sc) {
		st, err := compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(st.SQL, instSessions+"t_org_a.otel_traces") || !strings.Contains(st.SQL, instIDCond) {
			t.Errorf("%s: browser read is not narrowed to the instance's sessions:\n%s", name, st.SQL)
		}
		assertParameterized(t, st, "i1' OR 1=1 --")
		lo := sc.Start.Add(-24 * time.Hour).UnixNano()
		if !slices.Contains(mapValues(st.Params), strconv.FormatInt(lo, 10)) {
			t.Errorf("%s: the session lookup does not read from a day before the range: %v", name, st.Params)
		}
	}
	// error_hourly has no session: an instance's issues are counted from its events.
	p := bp("issues")
	st, _, err := PanelQuery{Scope: sc, Panel: p}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, tblIssues) || !strings.Contains(st.SQL, "count() AS events, uniq(SessionId) AS sessions") {
		t.Errorf("an instance's issues must be counted from error_events, not error_hourly:\n%s", st.SQL)
	}
}

func TestInstanceScopeLimits(t *testing.T) {
	many := make([]string, MaxScopeInstances+1)
	for i := range many {
		many[i] = "i" + strconv.Itoa(i)
	}
	for name, mut := range map[string]func(*Scope){
		"ids":   func(s *Scope) { s.InstanceIDs = many },
		"users": func(s *Scope) { s.InstanceUsers = many },
	} {
		if _, err := (TraceQuery{Scope: scopeWith(mut), Limit: 5}).Compile(); !isInvalid(err) {
			t.Errorf("%s: %d instances accepted: %v", name, len(many), err)
		}
	}
	atCap := scopeWith(func(s *Scope) { s.InstanceIDs = many[:MaxScopeInstances] })
	if _, err := (TraceQuery{Scope: atCap, Limit: 5}).Compile(); err != nil {
		t.Errorf("%d instances refused: %v", MaxScopeInstances, err)
	}
}

// A cursor minted under one instance never pages another, and a scope with no
// instance keeps the fingerprint it had before instances existed.
func TestInstanceIsPartOfTheFingerprint(t *testing.T) {
	plain := scopeWith(nil)
	if slices.Contains(plain.Fingerprint(), "inst") || slices.Contains(plain.Fingerprint(), "iusr") {
		t.Errorf("an unnarrowed fingerprint changed: %v", plain.Fingerprint())
	}
	a := scopeWith(func(s *Scope) { s.InstanceIDs = []string{"i1"} })
	b := scopeWith(func(s *Scope) { s.InstanceIDs = []string{"i2"} })
	u := scopeWith(func(s *Scope) { s.InstanceUsers = []string{"i1"} })
	if slices.Equal(a.Fingerprint(), b.Fingerprint()) || slices.Equal(a.Fingerprint(), u.Fingerprint()) ||
		slices.Equal(a.Fingerprint(), plain.Fingerprint()) {
		t.Errorf("instances do not separate fingerprints: %v %v %v", a.Fingerprint(), b.Fingerprint(), u.Fingerprint())
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
