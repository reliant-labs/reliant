package obsquery

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func bp(kind string) Panel { return Panel{ID: "p", Kind: kind, Signal: "traces"} }

func browserScope() Scope {
	return Scope{Database: "t_org_a", Start: t0, End: t0.Add(24 * time.Hour),
		EnvRestricted: true, EnvIDs: []string{"env-prod"}, Envs: []Env{{ID: "env-prod", Name: "prod"}}}
}

func TestFunnelCompiles(t *testing.T) {
	h := "x') OR 1=1 --"
	p := bp("funnel")
	p.Funnel = &PanelFunnel{Steps: []string{"signup", "checkout_started", h}, Window: "2h"}
	p.Where = []PanelPredicate{{Field: "service.name", Op: "eq", Value: h}, {Field: "span.attr.plan", Op: "eq", Value: "pro"}}
	sc := browserScope()
	sc.Services = []string{h}
	st, shape, err := PanelQuery{Scope: sc, Panel: p, Filters: []PanelPredicate{{Field: "deployment.environment.name", Op: "eq", Value: "prod"}}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, h)
	if shape.Series != 3 || shape.Bucketed {
		t.Errorf("shape = %+v", shape)
	}
	for _, want := range []string{"windowFunnel(", "FROM t_org_a.product_events", "GROUP BY SessionId", "countIf(level >= 3) AS s3",
		"SessionId != ''", "Props[{", "ServiceName IN (", "ResourceAttributes"} {
		if want == "ResourceAttributes" {
			if strings.Contains(st.SQL, want) {
				t.Errorf("product_events has no %s column:\n%s", want, st.SQL)
			}
			continue
		}
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// the window is 2h in milliseconds
	found := false
	for _, v := range st.Params {
		if v == "7200000" {
			found = true
		}
	}
	if !found {
		t.Errorf("window not bound as 7200000 ms: %v", st.Params)
	}
}

func TestFunnelByUserGroupsByUserId(t *testing.T) {
	p := bp("funnel")
	p.Funnel = &PanelFunnel{Steps: []string{"a", "b"}, By: "user"}
	st, _, err := PanelQuery{Scope: browserScope(), Panel: p}.Compile()
	if err != nil || !strings.Contains(st.SQL, "GROUP BY UserId") || !strings.Contains(st.SQL, "UserId != ''") {
		t.Errorf("%v\n%s", err, st.SQL)
	}
}

func TestIssuesCompiles(t *testing.T) {
	h := "x') OR 1=1 --"
	p := bp("issues")
	p.Where = []PanelPredicate{{Field: "resource.attr.rum.sessionId", Op: "exists"}, {Field: "service.version", Op: "eq", Value: h}}
	st, _, err := PanelQuery{Scope: browserScope(), Panel: p}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, h)
	for _, want := range []string{"FROM t_org_a.error_hourly", "FROM t_org_a.error_events", "uniqMerge(Sessions)", "GROUP BY ServiceName, RawStackHash",
		"LIMIT 51", "ServiceVersion = {", "EnvId = {"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// the session-exists predicate is true of every error row and adds nothing
	if strings.Contains(st.SQL, "SessionId != ''") {
		t.Errorf("session-exists should compile away:\n%s", st.SQL)
	}
	if _, _, err := (PanelQuery{Scope: browserScope(), Panel: Panel{ID: "p", Kind: "issues", Where: []PanelPredicate{{Field: "resource.attr.rum.sessionId", Op: "eq", Value: "x"}}}}).Compile(); err == nil {
		t.Error("an issues panel cannot filter by a session value")
	}
}

func TestIssuesRangeIsWholeHours(t *testing.T) {
	sc := browserScope()
	sc.Start = t0.Add(90 * time.Minute)
	sc.End = t0.Add(150 * time.Minute)
	st, _, err := PanelQuery{Scope: sc, Panel: bp("issues")}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	var hours []string
	for _, v := range st.Params {
		hours = append(hours, v)
	}
	want := map[string]bool{
		strconv.FormatInt(t0.Add(time.Hour).Unix(), 10):   false,
		strconv.FormatInt(t0.Add(3*time.Hour).Unix(), 10): false,
	}
	for _, v := range hours {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	for v, ok := range want {
		if !ok {
			t.Errorf("hour bound %s missing from %v", v, hours)
		}
	}
}

func TestSessionsMergesPartialRowsAndFiltersAfterMerging(t *testing.T) {
	p := bp("sessions")
	p.HasError = true
	p.Where = []PanelPredicate{{Field: "span.attr.userId", Op: "eq", Value: "user-42"}, {Field: "service.name", Op: "eq", Value: "web"}}
	sc := browserScope()
	sc.Filters = []AttrFilter{{Key: "service.version", Op: "eq", Value: "3f9c0e"}}
	st, _, err := PanelQuery{Scope: sc, Panel: p}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	beforeHaving, having, _ := strings.Cut(st.SQL, "HAVING")
	_, where, _ := strings.Cut(beforeHaving, "WHERE")
	for _, want := range []string{"GROUP BY ServiceName, SessionId", "argMinMerge(EntryPath)", "LIMIT 51", "ORDER BY started DESC"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// per-row: the service (a key column); after merging: env, user, version, errors
	if !strings.Contains(where, "ServiceName = {") {
		t.Errorf("service predicate must be per row:\n%s", st.SQL)
	}
	for _, want := range []string{"max(EnvId) = {", "max(UserId) = {", "max(ServiceVersion) = {", "sum(Errors) > 0", "started >="} {
		if !strings.Contains(having, want) {
			t.Errorf("HAVING lacks %q:\n%s", want, st.SQL)
		}
	}
	if strings.Contains(where, "UserId") || strings.Contains(where, "EnvId") {
		t.Errorf("merged-only columns leaked into WHERE:\n%s", where)
	}
}

func TestBrowserPanelRanges(t *testing.T) {
	for kind, max := range map[string]time.Duration{"funnel": MaxRangeFunnel, "issues": MaxRangeIssues, "sessions": MaxRangeSessions} {
		p := bp(kind)
		if kind == "funnel" {
			p.Funnel = &PanelFunnel{Steps: []string{"a", "b"}}
		}
		if got, err := p.MaxRange(); err != nil || got != max {
			t.Errorf("%s MaxRange = %v, %v", kind, got, err)
		}
		sc := browserScope()
		sc.End = sc.Start.Add(max + time.Hour)
		if _, _, err := (PanelQuery{Scope: sc, Panel: p}).Compile(); err == nil {
			t.Errorf("%s: a range over %s must be refused", kind, max)
		}
		sc.End = sc.Start.Add(max)
		if _, _, err := (PanelQuery{Scope: sc, Panel: p}).Compile(); err != nil {
			t.Errorf("%s: a range of exactly %s: %v", kind, max, err)
		}
	}
}

func TestBrowserPanelRejections(t *testing.T) {
	two := &PanelFunnel{Steps: []string{"a", "b"}}
	for name, p := range map[string]Panel{
		"funnel without a definition": bp("funnel"),
		"one step":                    {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a"}}},
		"eleven steps":                {Kind: "funnel", Funnel: &PanelFunnel{Steps: strings.Split("a,b,c,d,e,f,g,h,i,j,k", ",")}},
		"repeated step":               {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a", "a"}}},
		"blank step":                  {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a", " "}}},
		"long step":                   {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a", strings.Repeat("x", 129)}}},
		"window without a unit":       {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a", "b"}, Window: "60"}},
		"window past retention":       {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a", "b"}, Window: "91d"}},
		"unknown by":                  {Kind: "funnel", Funnel: &PanelFunnel{Steps: []string{"a", "b"}, By: "org"}},
		"series on a funnel":          {Kind: "funnel", Funnel: two, Series: []PanelSeries{{Agg: "count"}}},
		"group_by on issues":          {Kind: "issues", GroupBy: []string{"service.name"}},
		"logs signal":                 {Kind: "sessions", Signal: "logs"},
		"funnel on issues":            {Kind: "issues", Funnel: two},
		"has_error on issues":         {Kind: "issues", HasError: true},
		"funnel on a number panel":    {Kind: "number", Signal: "traces", Funnel: two, Series: []PanelSeries{{Agg: "count"}}},
		"span field on issues":        {Kind: "issues", Where: []PanelPredicate{{Field: "span.name", Op: "eq", Value: "x"}}},
		"user on issues":              {Kind: "issues", Where: []PanelPredicate{{Field: "span.attr.userId", Op: "eq", Value: "x"}}},
		"prop on sessions":            {Kind: "sessions", Where: []PanelPredicate{{Field: "span.attr.plan", Op: "eq", Value: "x"}}},
	} {
		if _, _, err := (PanelQuery{Scope: browserScope(), Panel: p}).Compile(); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestBrowserPanelsRespectTheScopesEnvironments(t *testing.T) {
	for _, kind := range []string{"funnel", "issues", "sessions"} {
		p := bp(kind)
		if kind == "funnel" {
			p.Funnel = &PanelFunnel{Steps: []string{"a", "b"}}
		}
		sc := browserScope()
		sc.EnvIDs = nil // restricted to nothing: matches nothing
		st, _, err := PanelQuery{Scope: sc, Panel: p}.Compile()
		if err != nil || !strings.Contains(st.SQL, "1 = 0") {
			t.Errorf("%s with no visible environments must match nothing: %v\n%s", kind, err, st.SQL)
		}
		sc = browserScope()
		sc.EnvIDs = []string{"env-prod", "env-stage"}
		st, _, _ = PanelQuery{Scope: sc, Panel: p}.Compile()
		if !strings.Contains(st.SQL, "has({") {
			t.Errorf("%s: several environments compile to has():\n%s", kind, st.SQL)
		}
	}
}
