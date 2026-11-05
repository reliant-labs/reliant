package obsquery

import (
	"strings"
	"testing"
)

// A trace's page row names its client, its frontend build and its backend
// builds after the location, before any RED group column, so the columns
// before them keep their positions too.
func TestTraceRowsNameTheClientAndBuilds(t *testing.T) {
	st, err := (TraceQuery{Scope: scope(), Limit: 10, GroupBy: []string{"service.name"}}).CompileREDGroups()
	if err != nil {
		t.Fatal(err)
	}
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
	if g := strings.Index(st.SQL, "AS g0"); g < strings.Index(st.SQL, "AS backend_versions") {
		t.Errorf("a RED group column precedes the client columns:\n%s", st.SQL)
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
// RED group columns. A reader written against the 16-column page (the control
// plane's ListTraces, via scanByName) therefore still finds each of its
// columns.
func TestTracePageKeepsEveryColumnItReturnedBefore(t *testing.T) {
	before := []string{"trace_id", "start", "dur_ms", "root_service", "root_name", "root_dur_ms", "span_count",
		"error_count", "tw", "services", "source", "user_id", "session_id", "country", "region", "city"}
	page, err := (TraceQuery{Scope: scope(), Limit: 10}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	groups, err := (TraceQuery{Scope: scope(), Limit: 10, GroupBy: []string{"service.name"}}).CompileREDGroups()
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{"page": page.SQL, "red groups": groups.SQL} {
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
