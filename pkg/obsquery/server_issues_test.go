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
	for _, want := range []string{ServerFingerprintSQL + " AS fingerprint", ServerErrorFilterSQL, ServerTitleSQL, ServerCulpritSQL,
		"GROUP BY fingerprint, service", "FROM default.otel_logs", "LIMIT 26", "ResourceAttributes['service.version']", "argMax(TraceId, Timestamp)"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("statement lacks %q:\n%s", want, st.SQL)
		}
	}
	assertParameterized(t, st, "x') OR 1=1 --", "'api'", "'dev'")
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
		if !strings.Contains(st.SQL, ServerFingerprintSQL) || !strings.Contains(st.SQL, ServerErrorFilterSQL) {
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
