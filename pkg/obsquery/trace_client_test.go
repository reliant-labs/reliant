package obsquery

import (
	"regexp"
	"strings"
	"testing"
)

// A trace's page row names its client, its frontend build and its backend
// builds after the location, so the columns before them keep their positions
// too.
func TestTraceRowsNameTheClientAndBuilds(t *testing.T) {
	page, err := (TraceQuery{Scope: scope(), Limit: 10}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	last := strings.Index(page.SQL, "AS city")
	for _, col := range []string{"AS client_name", "AS client_version", "AS os_name", "AS os_version", "AS device_type",
		"AS desktop_version", "AS frontend_version", "AS backend_services", "AS backend_versions"} {
		i := strings.Index(page.SQL, col)
		if i < last {
			t.Errorf("%s missing or out of order:\n%s", col, page.SQL)
		}
		last = i
	}

	// The frontend is a browser span's build; the backend every other span's
	// that did not come through the browser pipeline.
	for _, want := range []string{
		"anyIf(ResourceAttributes['service.version'], ResourceAttributes['rum.sessionId'] != '' AND",
		"ResourceAttributes['rum.sessionId'] = '' AND ResourceAttributes['reliant.ingest.class'] != 'browser'",
	} {
		if !strings.Contains(page.SQL, want) {
			t.Errorf("missing %q:\n%s", want, page.SQL)
		}
	}
}

// The page statement is read by column name (perTrace's contract): every
// column it returned before the client and build columns were added is still
// returned, once, under the same name and in the same order, with and without
// a group-by. A reader written against the 16-column page (the control
// plane's ListTraces, via scanByName) therefore still finds each of its
// columns. The RED statements' inner rows are no such contract: their outer
// SELECT is what a reader scans.
func TestTracePageKeepsEveryColumnItReturnedBefore(t *testing.T) {
	before := []string{"trace_id", "start", "dur_ms", "root_service", "root_name", "root_dur_ms", "span_count",
		"error_count", "tw", "services", "source", "user_id", "session_id", "country", "region", "city"}
	page, err := (TraceQuery{Scope: scope(), Limit: 10}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := (TraceQuery{Scope: scope(), Limit: 10, GroupBy: []string{"service.name"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{"page": page.SQL, "grouped page": grouped.SQL} {
		got := selectAliases(t, sql)
		at := map[string]int{}
		for i, a := range got {
			if _, dup := at[a]; dup {
				t.Errorf("%s: column %q returned twice: %v", name, a, got)
			}
			at[a] = i
		}
		last := -1
		for _, col := range before {
			i, ok := at[col]
			if !ok {
				t.Errorf("%s: column %q is no longer returned: %v", name, col, got)
				continue
			}
			if i < last {
				t.Errorf("%s: column %q moved before an earlier column: %v", name, col, got)
			}
			last = i
		}
	}
}

// selectAliases returns the aliases of the per-trace SELECT in sql, in order:
// each `AS <name>` between its `SELECT TraceId AS trace_id` and its FROM.
func selectAliases(t *testing.T, sql string) []string {
	t.Helper()
	start := strings.Index(sql, "SELECT TraceId AS trace_id")
	if start < 0 {
		t.Fatalf("no per-trace SELECT in:\n%s", sql)
	}
	body := sql[start:]
	end := strings.Index(body, "\nFROM ")
	if end < 0 {
		t.Fatalf("no FROM after the per-trace SELECT in:\n%s", sql)
	}
	var out []string
	for _, line := range strings.Split(body[:end], "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		i := strings.LastIndex(line, " AS ")
		if i < 0 {
			t.Fatalf("select line without an alias: %q", line)
		}
		out = append(out, line[i+len(" AS "):])
	}
	return out
}

// Each client and build filter narrows the candidate traces to those with a
// matching span on its side, binds its value, and is part of the page token.
func TestTraceClientAndBuildFilters(t *testing.T) {
	base := TraceQuery{Scope: scope(), Limit: 10}
	for name, c := range map[string]struct {
		q    TraceQuery
		want string
	}{
		"browser":  {TraceQuery{Scope: scope(), Limit: 10, Browser: "Firefox"}, "ResourceAttributes['rum.sessionId'] != '' AND ResourceAttributes['user_agent.name'] = {"},
		"os":       {TraceQuery{Scope: scope(), Limit: 10, OS: "Windows"}, "ResourceAttributes['rum.sessionId'] != '' AND ResourceAttributes['os.name'] = {"},
		"frontend": {TraceQuery{Scope: scope(), Limit: 10, FrontendVersion: "1.8.16"}, "ResourceAttributes['rum.sessionId'] != '' AND ResourceAttributes['service.version'] = {"},
		"backend":  {TraceQuery{Scope: scope(), Limit: 10, BackendVersion: "v1.7.91"}, "ResourceAttributes['reliant.ingest.class'] != 'browser' AND ResourceAttributes['service.version'] = {"},
	} {
		st, err := c.q.Compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(st.SQL, "TraceId IN (SELECT TraceId FROM") || !strings.Contains(st.SQL, c.want) {
			t.Errorf("%s: no prefilter %q:\n%s", name, c.want, st.SQL)
		}
		if c.q.Fingerprint() == base.Fingerprint() {
			t.Errorf("%s is not part of the page token", name)
		}
	}
	if (TraceQuery{Scope: scope(), Limit: 10}).Fingerprint() != base.Fingerprint() {
		t.Error("a query without the new filters changed its page token")
	}
	if _, err := (TraceQuery{Scope: scope(), Limit: 10, BackendVersion: strings.Repeat("v", 1000)}).Compile(); !isInvalid(err) {
		t.Errorf("an oversized backend_version compiled: %v", err)
	}
}

// The RED summaries aggregate only what they read. Each page column is an
// aggregate over every span of the window, and with all of them a 24h summary
// of prod traffic outlasted the gateway's 15s: the traces list answered 504
// for "Last 24 hours" and "Last 7 days".
func TestTraceREDReadsOnlyWhatItSummarizes(t *testing.T) {
	q := TraceQuery{Scope: scope(), Limit: 10, ErrorsOnly: true, MinDurationMs: 5, RootSpanName: "Get", GroupBy: []string{"service.name"}}
	red, err := q.CompileRED()
	if err != nil {
		t.Fatal(err)
	}
	groups, err := q.CompileREDGroups()
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]Stmt{"red": red, "red groups": groups} {
		assertParameterized(t, st)
		want := []string{"trace_id", "dur_ms", "root_name", "error_count", "tw"}
		if name == "red groups" {
			want = append(want, "g0")
		}
		if got := selectAliases(t, st.SQL); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s inner columns = %v, want %v", name, got, want)
		}
		for _, h := range []string{"error_count > 0", "dur_ms >= {", "positionCaseInsensitive(root_name, {"} {
			if !strings.Contains(st.SQL, h) {
				t.Errorf("%s lost the HAVING term %q:\n%s", name, h, st.SQL)
			}
		}
	}
}

// Who made the call and on which build are facts of the whole trace: the
// service and resource filters that pick the spans counted must not narrow
// the spans those facts are looked for on. "The api's traces from Chrome"
// looked for Chrome on the api's own spans, which never carry a browser, and
// matched nothing. Errors and the root name are about the counted spans, so
// they keep the narrowing.
func TestTraceWideFiltersIgnoreTheServiceNarrowing(t *testing.T) {
	sc := scope()
	sc.Services = []string{"api"}
	sc.EnvIDs, sc.EnvRestricted = []string{"env-prod"}, true
	sc.Filters = []AttrFilter{{Key: "k8s.workload.name", Value: "api"}}
	for name, q := range map[string]TraceQuery{
		"user":     {UserID: "u1"},
		"session":  {SessionID: "0123456789abcdef0123456789abcdef"},
		"browser":  {Browser: "Chrome"},
		"os":       {OS: "macOS"},
		"frontend": {FrontendVersion: "f72e604bef0c"},
		"backend":  {BackendVersion: "v1.7.96"},
	} {
		q.Scope, q.Limit = sc, 10
		st, err := q.Compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertParameterized(t, st)
		sub := prefilterOf(t, st.SQL)
		if strings.Contains(sub, "ServiceName IN") || strings.Contains(sub, "k8s.workload.name") {
			t.Errorf("%s: the candidate traces are narrowed to the scope's service or workload:\n%s", name, sub)
		}
		if !strings.Contains(sub, "ResourceAttributes['reliant.env.id']") {
			t.Errorf("%s: the candidate traces are not narrowed to the environment:\n%s", name, sub)
		}
		if !strings.Contains(st.SQL, "AND ServiceName IN (") {
			t.Errorf("%s: the counted spans lost the service narrowing:\n%s", name, st.SQL)
		}
	}
	st, err := (TraceQuery{Scope: sc, Limit: 10, ErrorsOnly: true}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	if sub := prefilterOf(t, st.SQL); !strings.Contains(sub, "ServiceName IN") {
		t.Errorf("errors-only lost the service narrowing its HAVING relies on:\n%s", sub)
	}
}

// Dropping the service narrowing from the trace-wide candidate sets must not
// drop the tenant or the environment: a span of another org's database, or of
// an environment the caller may not read, never makes a trace match "from
// Chrome" or "user u1". Each candidate subquery reads the scope's database and
// the bound authorized environment ids, and a restricted scope with no
// environments matches nothing (1 = 0), on the page and both RED statements.
func TestTraceWideFiltersKeepTheTenantAndEnvironment(t *testing.T) {
	envs := map[string]struct {
		mut  func(*Scope)
		want func(t *testing.T, sub string, params map[string]string)
	}{
		"one env": {
			mut: func(s *Scope) { s.EnvRestricted, s.EnvIDs = true, []string{"env-prod"} },
			want: func(t *testing.T, sub string, params map[string]string) {
				m := regexp.MustCompile(`ResourceAttributes\['reliant\.env\.id'\] = \{([a-z0-9]+):String\}`).FindStringSubmatch(sub)
				if m == nil || params[m[1]] != "env-prod" {
					t.Errorf("candidate traces are not bound to env-prod (%v):\n%s", params, sub)
				}
			},
		},
		"two envs": {
			mut: func(s *Scope) { s.EnvRestricted, s.EnvIDs = true, []string{"env-prod", "env-staging"} },
			want: func(t *testing.T, sub string, params map[string]string) {
				m := regexp.MustCompile(`has\(\{([a-z0-9]+):Array\(String\)\}, ResourceAttributes\['reliant\.env\.id'\]\)`).FindStringSubmatch(sub)
				if m == nil || params[m[1]] != "['env-prod','env-staging']" {
					t.Errorf("candidate traces are not bound to the authorized envs (%v):\n%s", params, sub)
				}
			},
		},
		"restricted, empty": {
			mut: func(s *Scope) { s.EnvRestricted = true },
			want: func(t *testing.T, sub string, _ map[string]string) {
				if !strings.Contains(sub, "1 = 0") {
					t.Errorf("a restricted scope with no environments does not fail closed:\n%s", sub)
				}
			},
		},
	}
	filters := map[string]TraceQuery{
		"user":     {UserID: "u1"},
		"session":  {SessionID: "0123456789abcdef0123456789abcdef"},
		"browser":  {Browser: "Chrome"},
		"os":       {OS: "macOS"},
		"frontend": {FrontendVersion: "f72e604bef0c"},
		"backend":  {BackendVersion: "v1.7.96"},
	}
	for envName, e := range envs {
		for fName, q := range filters {
			sc := scope()
			sc.Services = []string{"api"}
			sc.Filters = []AttrFilter{{Key: "k8s.workload.name", Value: "api"}}
			e.mut(&sc)
			q.Scope, q.Limit, q.GroupBy = sc, 10, []string{"service.name"}
			for stName, compile := range map[string]func() (Stmt, error){
				"page": q.Compile, "red": q.CompileRED, "red groups": q.CompileREDGroups,
			} {
				name := envName + "/" + fName + "/" + stName
				st, err := compile()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				assertParameterized(t, st)
				sub := prefilterOf(t, st.SQL)
				if !strings.Contains(sub, "SELECT TraceId FROM t_org_a.otel_traces ") {
					t.Errorf("%s: the candidate traces are not read from the scope's database:\n%s", name, sub)
				}
				e.want(t, sub, st.Params)
			}
		}
	}
}

// prefilterOf returns the first candidate-trace subquery in sql.
func prefilterOf(t *testing.T, sql string) string {
	t.Helper()
	i := strings.Index(sql, "TraceId IN (SELECT TraceId FROM")
	if i < 0 {
		t.Fatalf("no candidate-trace prefilter in:\n%s", sql)
	}
	depth, j := 0, i+len("TraceId IN ")
	for k := j; k < len(sql); k++ {
		switch sql[k] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[j : k+1]
			}
		}
	}
	t.Fatalf("unbalanced prefilter in:\n%s", sql)
	return ""
}

// An app's spans name their workload by its kind (the k8sattributes
// processor's k8s.deployment.name, ...), never k8s.workload.name, which only
// the node agent stamps on what it collects. Prod spans carry the former
// alone, so a Workload filter on traces matched nothing. It reads either, on
// traces and logs; a metric keeps reading the key itself.
func TestWorkloadFilterReadsTheKindSpecificName(t *testing.T) {
	sc := scope()
	sc.Filters = []AttrFilter{{Key: "k8s.workload.name", Value: "admin-server"}}
	st, err := (TraceQuery{Scope: sc, Limit: 10}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	for _, want := range []string{"ResourceAttributes['k8s.workload.name']", "ResourceAttributes['k8s.deployment.name']", "ResourceAttributes['k8s.statefulset.name']"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("workload filter does not read %s:\n%s", want, st.SQL)
		}
	}
	b := newBuilder(sc)
	if c, resource, err := b.filterCond(SigTraces, AttrFilter{Key: "k8s.workload.name", Op: "exists"}); err != nil || !resource || c != workloadNameExpr+" != ''" {
		t.Errorf("exists = %q, %v, %v", c, resource, err)
	}
	if c, _, err := b.filterCond(SigMetrics, AttrFilter{Key: "k8s.workload.name", Value: "api"}); err != nil || strings.Contains(c, "k8s.deployment.name") {
		t.Errorf("metric workload filter = %q, %v", c, err)
	}
}
