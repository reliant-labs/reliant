package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/dbquery"
)

// fakeQueryDatabase is a control plane that answers QueryDatabase with reply
// (or err) and records the request bodies.
type fakeQueryDatabase struct {
	reply  string
	err    error
	bodies []map[string]any
}

func (f *fakeQueryDatabase) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != procQueryDatabase {
		return errors.New("unexpected call " + procedure)
	}
	b, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.bodies = append(f.bodies, m)
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.reply), out)
}

// withHostedDB points the hosted transport at fake, as env "env-prod-1".
func withHostedDB(t *testing.T, fake *fakeQueryDatabase) {
	t.Helper()
	withHostedCaller(t, fake)
}

// withHostedCaller points the hosted transport at caller, as env "env-prod-1".
func withHostedCaller(t *testing.T, caller dbQueryCaller) {
	t.Helper()
	prev := dbQueryCloudOpen
	dbQueryCloudOpen = func(context.Context, string, time.Duration) (dbQueryCaller, string, error) {
		return caller, "env-prod-1", nil
	}
	t.Cleanup(func() { dbQueryCloudOpen = prev })
}

// hostedDBQueryFixture is a one-workload render of prod whose api is hosted, with
// the given declared policy.
func hostedDBQueryFixture(policy string) string {
	return `{"output":{"data_access":{"db_query":"` + policy + `"},"workloads":[{"name":"api","kind":"service","runtime":{"type":"` + RuntimeHosted + `"},"spec":{"kind":"service"}}]}}`
}

const queryDatabaseReply = `{
 "columns":[{"name":"id","pgType":"int8"},{"name":"name","pgType":"text"},{"name":"meta","pgType":"jsonb"},{"name":"gone","pgType":"timestamptz"},{"name":"ok","pgType":"bool"}],
 "rows":[{"values":[7,"ada",{"b":1, "a":[2]},null,true]},{"values":["12345678901234567890","bob",[],null,false]}],
 "rowCount":"2","durationMs":"12","replica":"ro","auditId":"aud-1","sqlSha256":"abc","database":"main"}`

func TestDBQueryHostedRunsThroughTheControlPlane(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	withKCLFixture(t, hostedDBQueryFixture("audited"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fake := &fakeQueryDatabase{reply: queryDatabaseReply}
	withHostedDB(t, fake)

	out, err := runDBQueryCmd(t, "prod", "select * from jobs", "--json", "--max-rows", "50", "--timeout", "3s",
		"--confirm-env", "prod", "--reason", " ISS-7f3a ", "--database", "main")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(fake.bodies) != 1 {
		t.Fatalf("calls = %d", len(fake.bodies))
	}
	body := fake.bodies[0]
	for k, want := range map[string]any{
		"environmentId": "env-prod-1", "sql": "select * from jobs", "maxRows": float64(50), "timeoutMs": float64(3000),
		"confirmEnvId": "env-prod-1", "reason": "ISS-7f3a", "database": "main",
	} {
		if body[k] != want {
			t.Errorf("request %s = %v, want %v", k, body[k], want)
		}
	}

	var doc struct {
		OK    bool `json:"ok"`
		Scope struct {
			Transport, Database string
			Policy              *string
		} `json:"scope"`
		Audit    dbQueryAudit     `json:"audit"`
		Rows     [][]any          `json:"rows"`
		RowCount int              `json:"row_count"`
		Duration int64            `json:"duration_ms"`
		Columns  []map[string]any `json:"columns"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !doc.OK || doc.Scope.Transport != "control-plane" || doc.Scope.Database != "main" || doc.Scope.Policy != nil {
		t.Errorf("scope: %+v (the control plane applies the recorded policy; forge reports none)", doc.Scope)
	}
	if doc.Audit != (dbQueryAudit{ID: "aud-1", SQLSHA256: "abc", Replica: "ro"}) {
		t.Errorf("audit = %+v", doc.Audit)
	}
	if doc.RowCount != 2 || doc.Duration != 12 || len(doc.Columns) != 5 || doc.Columns[2]["pg_type"] != "jsonb" {
		t.Errorf("result: rows=%d duration=%d columns=%v", doc.RowCount, doc.Duration, doc.Columns)
	}
	if big, ok := doc.Rows[1][0].(string); !ok || big != "12345678901234567890" {
		t.Errorf("a big number stays a string: %#v", doc.Rows[1][0])
	}
	if obj, ok := doc.Rows[0][2].(map[string]any); !ok || obj["b"] != float64(1) {
		t.Errorf("a jsonb object stays an object: %#v", doc.Rows[0][2])
	}

	table, err := runDBQueryCmd(t, "prod", "select 1", "--confirm-env", "prod", "--reason", "x")
	if err != nil || !strings.Contains(table, "(2 rows, 12 ms; audit aud-1)") || !strings.Contains(table, "ada") {
		t.Errorf("table (%v):\n%s", err, table)
	}
}

// The declared policy of this checkout is not the authority for a hosted env:
// the control plane judges from the recorded shape. A declared deny does not
// stop the call; a confirmation for another env is not passed on.
func TestDBQueryHostedLeavesThePolicyToTheControlPlane(t *testing.T) {
	withKCLFixture(t, hostedDBQueryFixture("deny"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fake := &fakeQueryDatabase{reply: queryDatabaseReply}
	withHostedDB(t, fake)
	if out, err := runDBQueryCmd(t, "prod", "select 1", "--confirm-env", "staging", "--json"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(fake.bodies) != 1 {
		t.Fatal("a declared deny stopped a hosted query the control plane decides")
	}
	if _, sent := fake.bodies[0]["confirmEnvId"]; sent {
		t.Errorf("a confirmation of another env was sent: %v", fake.bodies[0])
	}
	if _, sent := fake.bodies[0]["reason"]; sent {
		t.Errorf("an empty reason was sent: %v", fake.bodies[0])
	}
}

func TestDBQueryHostedErrorsMapToTheExitTable(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, tc := range []struct {
		name       string
		err        error
		wantCode   int
		wantReason string
		wantMsg    string
	}{
		{"guard", &cloud.Error{Code: cloud.CodeInvalidArgument, Reason: "multiple_statements", Message: "one statement only"}, exitWrong, "multiple_statements", "one statement only"},
		{"confirm", &cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: "confirmation_required", Message: "confirm"}, exitWrong, "confirmation_required", "confirm"},
		{"no database", &cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: "no_database"}, exitWrong, "no_database", ""},
		{"role not provisioned", &cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: "credential_missing"}, exitUndetermined, "credential_missing", ""},
		{"policy", &cloud.Error{Code: cloud.CodePermissionDenied, Message: "disabled by its data_access policy"}, exitWrong, "", "data_access"},
		{"secret column", &cloud.Error{Code: cloud.CodePermissionDenied, Reason: "permission_denied"}, exitWrong, "permission_denied", ""},
		{"timeout", &cloud.Error{Code: cloud.CodeDeadlineExceeded, Reason: "statement_timeout"}, exitUndetermined, "statement_timeout", ""},
		{"rate", &cloud.Error{Code: cloud.CodeResourceExhausted, Reason: "rate_limited", Message: "retry in 3s"}, exitUndetermined, "rate_limited", "retry in 3s"},
		{"down", &cloud.Error{Code: cloud.CodeUnavailable}, exitUndetermined, "", ""},
		{"old control plane", &cloud.Error{Code: cloud.CodeUnimplemented}, exitUndetermined, "", "does not serve database queries yet"},
		{"transport", errors.New("dial tcp: connection refused"), exitUndetermined, "", "could not be reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHostedDB(t, &fakeQueryDatabase{err: tc.err})
			out, err := runDBQueryCmd(t, "prod", "select 1", "--json")
			if got := exitCodeForError(err); got != tc.wantCode {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.wantCode, err)
			}
			var doc dbQueryDocument
			if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
				t.Fatalf("not one JSON document: %v\n%s", jerr, out)
			}
			if doc.OK || doc.ExitCode != tc.wantCode || doc.Reason != tc.wantReason || !strings.Contains(doc.Error, tc.wantMsg) {
				t.Errorf("doc: ok=%v exit=%d reason=%q error=%q", doc.OK, doc.ExitCode, doc.Reason, doc.Error)
			}
			if doc.Scope == nil || doc.Scope.Transport != "control-plane" {
				t.Errorf("scope = %+v", doc.Scope)
			}
		})
	}
}

// truncatedReply is a QueryDatabase answer cut by a cap, with n rows kept.
func truncatedReply(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = `{"values":[` + strconv.Itoa(i) + `]}`
	}
	return `{"columns":[{"name":"n","pgType":"int8"}],"rows":[` + strings.Join(rows, ",") + `],"truncated":true,"durationMs":"3"}`
}

// The control plane says only "truncated". The row cap trips exactly when the
// rows kept reach --max-rows, so fewer rows than that means the response-size
// cap cut it, and raising --max-rows would not help.
func TestDBQueryHostedTruncatedByFollowsTheRowCap(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, tc := range []struct {
		name, maxRows string
		kept          int
		want          string
	}{
		{"fewer rows than the cap: bytes", "50", 3, "bytes"},
		{"rows reached the cap", "3", 3, "rows"},
		{"no rows kept", "50", 0, "bytes"},
		{"the default cap", "", dbquery.DefaultMaxRows, "rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHostedDB(t, &fakeQueryDatabase{reply: truncatedReply(tc.kept)})
			args := []string{"prod", "select n from t", "--json"}
			if tc.maxRows != "" {
				args = append(args, "--max-rows", tc.maxRows)
			}
			out, err := runDBQueryCmd(t, args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			var doc dbQueryDocument
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if !doc.Truncated || doc.TruncatedBy != tc.want {
				t.Errorf("truncated=%v truncated_by=%q, want %q", doc.Truncated, doc.TruncatedBy, tc.want)
			}
		})
	}
}

// A control plane that cannot be reached is found out before the query is
// said to have run: no release lookup (which would ask the same dead
// endpoint again, and warn a second time for the same cause).
func TestDBQueryHostedUnreachableDoesNotLookUpTheRelease(t *testing.T) {
	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prevOpen := dbQueryCloudOpen
	dbQueryCloudOpen = func(context.Context, string, time.Duration) (dbQueryCaller, string, error) {
		return nil, "", exitCodeError{code: exitUndetermined, msg: "env prod: cannot reach its control plane: dial tcp: connection refused"}
	}
	prevRelease := liveReleaseAt
	lookups := 0
	liveReleaseAt = func(context.Context, string, time.Time) (*liveRelease, error) {
		lookups++
		return nil, errors.New("cannot reach its control plane")
	}
	releaseStubbed = true
	t.Cleanup(func() { dbQueryCloudOpen, liveReleaseAt, releaseStubbed = prevOpen, prevRelease, false })

	out, err := runDBQueryCmd(t, "prod", "select 1", "--json")
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	if lookups != 0 {
		t.Errorf("the release was looked up %d time(s) for a query that never reached the control plane", lookups)
	}
	if !strings.Contains(out, "cannot reach its control plane") {
		t.Errorf("doc:\n%s", out)
	}
}

// A refused credential keeps forge's remedy: which credential was used and how
// to get the missing scope, not only the control plane's one line.
func TestDBQueryHostedForbiddenKeepsTheRemedy(t *testing.T) {
	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"permission_denied","message":"this credential does not carry the db:query scope"}`))
	}))
	t.Cleanup(srv.Close)
	client := cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL, TokenEnv: "FORGE_TOKEN"}, cloud.Credential{Token: "t", Source: cloud.SourceFlag, From: "--token"})
	withHostedCaller(t, client)

	out, err := runDBQueryCmd(t, "prod", "select 1", "--json")
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	var doc dbQueryDocument
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatal(jerr)
	}
	for _, want := range []string{"lacks the db:query scope", "fix:", "from --token", "does not carry the db:query scope"} {
		if !strings.Contains(doc.Error, want) {
			t.Errorf("error lacks %q:\n%s", want, doc.Error)
		}
	}
}

// The HTTP client must outlast the statement: cp runs it for --timeout plus a
// grace, and a client that gives up first turns cp's typed DeadlineExceeded
// into "could not be reached" (and holds cp's in-flight slot until the server
// notices). The open is handed the statement timeout to size it.
func TestDBQueryHostedHTTPClientOutlastsTheStatement(t *testing.T) {
	ep := cloud.Endpoint{Env: "prod", URL: "http://cp.invalid"}
	for _, stmt := range []time.Duration{0, time.Second, dbquery.MaxTimeout} {
		eff := stmt
		if eff == 0 {
			eff = dbquery.DefaultTimeout
		}
		c := newDBQueryCloudClient(ep, cloud.Credential{}, stmt)
		if c.HTTP == nil || c.HTTP.Timeout < eff+hostedQueryGrace+5*time.Second {
			t.Errorf("--timeout %s: HTTP timeout %v does not outlast the statement plus cp's grace", stmt, c.HTTP.Timeout)
		}
	}

	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got time.Duration
	prev := dbQueryCloudOpen
	dbQueryCloudOpen = func(_ context.Context, _ string, stmt time.Duration) (dbQueryCaller, string, error) {
		got = stmt
		return &fakeQueryDatabase{reply: queryDatabaseReply}, "env-prod-1", nil
	}
	t.Cleanup(func() { dbQueryCloudOpen = prev })
	if out, err := runDBQueryCmd(t, "prod", "select 1", "--timeout", "25s", "--json"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got != 25*time.Second {
		t.Errorf("the open was given %v, want the statement timeout 25s", got)
	}
}

// A 403 that is not about a scope (the env's policy, a missing grant) is the
// control plane's refusal of the request: its message, not "sign in again".
func TestDBQueryHostedPolicyRefusalIsNotACredentialProblem(t *testing.T) {
	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"permission_denied","message":"querying this environment's database is disabled by its data_access policy"}`))
	}))
	t.Cleanup(srv.Close)
	withHostedCaller(t, cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL, TokenEnv: "FORGE_TOKEN"}, cloud.Credential{Token: "t", Source: cloud.SourceFlag, From: "--token"}))
	out, err := runDBQueryCmd(t, "prod", "select 1", "--json")
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	var doc dbQueryDocument
	_ = json.Unmarshal([]byte(out), &doc)
	if !strings.Contains(doc.Error, "data_access policy") || strings.Contains(doc.Error, "forge login") || strings.Contains(doc.Error, "rejected the credential") {
		t.Errorf("error = %q", doc.Error)
	}
}
