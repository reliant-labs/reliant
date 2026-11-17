package obsquery

import (
	"strings"
	"testing"
	"time"
)

func issueScope() Scope {
	return Scope{Database: LocalDatabase, Local: true, Services: []string{"api"}, Start: t0, End: t0.Add(time.Hour),
		Filters: []AttrFilter{{Key: "deployment.environment.name", Op: "eq", Value: "dev"}}}
}

func TestServerIssuesGroupsByTheSfp1FingerprintOverServerErrors(t *testing.T) {
	st, err := ServerIssues{Scope: issueScope(), Service: "x') OR 1=1 --", Limit: 25}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ServerFingerprintSQL + " AS fingerprint", ServerFailureFilterSQL, ServerTitleSQL, ServerCulpritSQL,
		"GROUP BY fingerprint, service", "FROM default.otel_logs", "LIMIT 26", "ResourceAttributes['service.version']", "argMax(TraceId, Timestamp)"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("statement lacks %q:\n%s", want, st.SQL)
		}
	}
	assertParameterized(t, st, "x') OR 1=1 --", "'api'", "'dev'")
}

// An error issue is a server failure an agent triages: every ERROR, and a WARN
// the error-class policy classed server (a retryable failure such as Aborted,
// or a server deadline logged at WARN without Deadline.ExpiryIsError). A caller's
// own deadline is the caller's choice and stays out. ServerErrorFilterSQL is
// what counts as an error (release health, error counts) and does not widen.
func TestServerIssuesIncludeWarnServerFailures(t *testing.T) {
	if ServerErrorFilterSQL != `SeverityNumber >= 17 AND ResourceAttributes['rum.sessionId'] = ''` {
		t.Errorf("ServerErrorFilterSQL changed: %s", ServerErrorFilterSQL)
	}
	warn := "SeverityNumber >= 13 AND LogAttributes['" + LogKeyErrorClass + "'] = 'server' AND LogAttributes['" + LogKeyDeadline + "'] != 'caller'"
	for name, filter := range map[string]string{"ServerFailureFilterSQL": ServerFailureFilterSQL, "IssueRecordFilterSQL": IssueRecordFilterSQL} {
		for _, want := range []string{"SeverityNumber >= 17", warn, "ResourceAttributes['rum.sessionId'] = ''"} {
			if !strings.Contains(filter, want) {
				t.Errorf("%s lacks %q: %s", name, want, filter)
			}
		}
	}
	errs, err := ServerIssues{Scope: issueScope(), Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.SQL, ServerFailureFilterSQL) {
		t.Errorf("error issues must read server failures, WARN included:\n%s", errs.SQL)
	}
	inc, err := ServerIssueIncidents{Scope: issueScope()}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inc.SQL, ServerFailureFilterSQL) {
		t.Errorf("incidents group the issues obs issues lists:\n%s", inc.SQL)
	}
}

// Errors are the records without an issue source; a source's issues are its
// records at any level; one issue's statements read it whatever its source.
func TestServerIssuesSelectErrorsOrOneSource(t *testing.T) {
	errs, err := ServerIssues{Scope: issueScope(), Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.SQL, ServerFailureFilterSQL) || !strings.Contains(errs.SQL, IssueSourceSQL+" = ''") {
		t.Errorf("errors must exclude sourced records:\n%s", errs.SQL)
	}
	src, err := ServerIssues{Scope: issueScope(), Limit: 5, Source: "llm_bug_report"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src.SQL, "SeverityNumber") || !strings.Contains(src.SQL, IssueSourceSQL+" = ") {
		t.Errorf("a source is not filtered by level:\n%s", src.SQL)
	}
	assertParameterized(t, src, "llm_bug_report")
	one, err := ServerIssueEvents{Scope: issueScope(), Fingerprint: "0123456789abcdef", Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.SQL, IssueRecordFilterSQL) {
		t.Errorf("one issue's events are read whatever their source:\n%s", one.SQL)
	}
	if _, err := (ServerIssues{Scope: issueScope(), Limit: 5, Source: "Bad Source'"}).Compile(); !isInvalid(err) {
		t.Errorf("a malformed source: err = %v, want ErrInvalid", err)
	}
	for _, ok := range []string{"llm_bug_report", "a", "x.y-z"} {
		if !ValidIssueSource(ok) {
			t.Errorf("%q should be a valid source", ok)
		}
	}
}

func TestServerIssuesSortAndLimits(t *testing.T) {
	for sort, want := range map[string]string{"": "ORDER BY last_seen DESC", "last": "ORDER BY last_seen DESC", "first": "ORDER BY first_seen DESC", "events": "ORDER BY events DESC"} {
		st, err := ServerIssues{Scope: issueScope(), Limit: 5, Sort: sort}.Compile()
		if err != nil || !strings.Contains(st.SQL, want) {
			t.Errorf("sort %q: %v\n%s", sort, err, st.SQL)
		}
	}
	for _, q := range []ServerIssues{
		{Scope: issueScope(), Limit: 5, Sort: "id; DROP"},
		{Scope: issueScope(), Limit: 0},
		{Scope: issueScope(), Limit: MaxIssueLimit + 1},
		{Scope: issueScope(), Limit: 5, Fingerprint: "ABC"},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%+v: err = %v, want ErrInvalid", q, err)
		}
	}
}

func TestServerIssueStatementsAreFingerprintScopedAndParameterized(t *testing.T) {
	fp := "0123456789abcdef"
	events, err := ServerIssueEvents{Scope: issueScope(), Fingerprint: fp, Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	versions, err := ServerIssueVersions{Scope: issueScope(), Fingerprint: fp}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	co, err := ServerIssueCoOccurring{Scope: issueScope(), Fingerprint: fp}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]Stmt{"events": events, "versions": versions, "co-occurring": co} {
		// One issue's records are read whatever their issue source.
		if !strings.Contains(st.SQL, ServerFingerprintSQL) || !strings.Contains(st.SQL, IssueRecordFilterSQL) {
			t.Errorf("%s does not use the shared fingerprint and filter:\n%s", name, st.SQL)
		}
		assertParameterized(t, st, fp)
		found := false
		for _, v := range st.Params {
			found = found || v == fp
		}
		if !found {
			t.Errorf("%s never binds the fingerprint", name)
		}
	}
	if !strings.Contains(co.SQL, "TraceId IN (SELECT TraceId") || !strings.Contains(co.SQL, "!= {") {
		t.Errorf("co-occurring must exclude the issue itself and join on shared traces:\n%s", co.SQL)
	}
	for _, bad := range []string{"", "xyz", "0123456789ABCDEF", "0123456789abcde"} {
		if _, err := (ServerIssueEvents{Scope: issueScope(), Fingerprint: bad, Limit: 5}).Compile(); !isInvalid(err) {
			t.Errorf("fingerprint %q: err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestLocalIssueID(t *testing.T) {
	id := LocalIssueID("0123456789abcdef")
	if id != "sfp1-0123456789abcdef" {
		t.Fatalf("id = %s", id)
	}
	if fp, err := ParseLocalIssueID(id); err != nil || fp != "0123456789abcdef" {
		t.Errorf("round trip = %q, %v", fp, err)
	}
	for _, bad := range []string{"", "ISS-1", "sfp1-", "sfp1-xyz", "sfp2-0123456789abcdef"} {
		if _, err := ParseLocalIssueID(bad); !isInvalid(err) {
			t.Errorf("ParseLocalIssueID(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

// Affected is counted over the widest unit at least half of an issue's events
// name, and the unit travels with the count.
func TestServerIssuesCountAffectedOverTheFirstUnitEveryEventNames(t *testing.T) {
	st, err := ServerIssues{Scope: issueScope(), Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"countIf(LogAttributes['user.id'] != '') * 2 >= count(), uniqIf(LogAttributes['user.id'], LogAttributes['user.id'] != '')",
		"countIf(LogAttributes['reliant.chat.id'] != '') * 2 >= count(), uniqIf(LogAttributes['reliant.chat.id'], LogAttributes['reliant.chat.id'] != '')",
		"countIf(LogAttributes['thread.id'] != '') * 2 >= count(), uniqIf(LogAttributes['thread.id'], LogAttributes['thread.id'] != '')",
		"countIf(LogAttributes['workflow.id'] != '') * 2 >= count(), uniqIf(LogAttributes['workflow.id'], LogAttributes['workflow.id'] != '')",
		"uniqIf(TraceId, TraceId != '')) AS affected",
		"'users', countIf(LogAttributes['reliant.chat.id'] != '') * 2 >= count(), 'chats'",
		"'traces') AS affected_unit",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("statement lacks %q:\n%s", want, st.SQL)
		}
	}
	// users are preferred to chats, chats to threads, threads to workflows, workflows to traces
	order := []string{"'user.id'", "'reliant.chat.id'", "'thread.id'", "'workflow.id'"}
	last := -1
	for _, k := range order {
		i := strings.Index(AffectedSQL(), "LogAttributes["+k+"]")
		if i <= last {
			t.Errorf("%s is out of preference order in %s", k, AffectedSQL())
		}
		last = i
	}
	if cols := ServerIssuesColumns; cols[len(cols)-1] != "affected_unit" || cols[6] != "affected" {
		t.Errorf("columns = %v: affected_unit must be last, affected 7th", cols)
	}
	versions, err := ServerIssueVersions{Scope: issueScope(), Fingerprint: "0123456789abcdef"}.Compile()
	if err != nil || !strings.Contains(versions.SQL, AffectedUnitSQL()+" AS affected_unit") {
		t.Errorf("per-version statement lacks the unit: %v\n%s", err, versions.SQL)
	}
}
