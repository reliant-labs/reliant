package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/dbquery"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/release"
)

// dbQueryFixture is a one-workload render of an open env whose api runs as
// runtime with the given DATABASE_URL.
func dbQueryFixture(runtime, dsn string) string {
	env := ""
	if dsn != "" {
		env = fmt.Sprintf(`"env":[{"name":"DATABASE_URL","value":%q}]`, dsn)
	}
	return fmt.Sprintf(`{"output":{"data_access":{"db_query":"open"},"workloads":[{"name":"api","kind":"service","runtime":{"type":%q},"spec":{"kind":"service"%s}}]}}`,
		runtime, map[bool]string{true: "," + env, false: ""}[env != ""])
}

// runDBQueryCmd runs `forge db query args...` and returns stdout and the error.
func runDBQueryCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// The release lookup reads a ledger; a test that is not about it sees
	// "nothing promoted yet".
	if !releaseStubbed {
		prev := liveReleaseAt
		liveReleaseAt = func(context.Context, string, time.Time) (*liveRelease, error) { return nil, nil }
		t.Cleanup(func() { liveReleaseAt = prev })
	}
	cmd := newDBCmd()
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetArgs(append([]string{"query"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestDBQueryRequiresTheEnvAndOneStatement(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"dev", "select 1", "extra"},
	} {
		if _, err := runDBQueryCmd(t, args...); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	for name, o := range map[string]dbQueryOptions{
		"no statement":   {env: "dev", format: "table"},
		"both forms":     {env: "dev", sql: "select 1", file: "q.sql", format: "table"},
		"unknown format": {env: "dev", sql: "select 1", format: "xml"},
		"long reason":    {env: "dev", sql: "select 1", format: "table", reason: strings.Repeat("x", 501)},
	} {
		if err := validateDBQueryFlags(o); exitCodeForError(err) != exitWrong {
			t.Errorf("%s: err = %v, want exit 1", name, err)
		}
	}
}

func TestDBQueryPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		e    *KCLEntities
		want release.DBQueryPolicy
	}{
		"declared open":  {&KCLEntities{DataAccess: &release.ShapeDataAccess{DBQuery: release.DBQueryOpen}}, release.DBQueryOpen},
		"declared deny":  {&KCLEntities{DataAccess: &release.ShapeDataAccess{DBQuery: release.DBQueryDeny}}, release.DBQueryDeny},
		"absent=audited": {&KCLEntities{}, release.DBQueryAudited},
		"no render":      {nil, release.DBQueryAudited},
	} {
		if got := dbQueryPolicy(tc.e); got != string(tc.want) {
			t.Errorf("%s: policy %s, want %s", name, got, tc.want)
		}
	}

	open, audited, deny := string(release.DBQueryOpen), string(release.DBQueryAudited), string(release.DBQueryDeny)
	if err := checkDBQueryPolicy(dbQueryOptions{env: "prod"}, open); err != nil {
		t.Errorf("open env refused: %v", err)
	}
	for _, bad := range []dbQueryOptions{
		{env: "prod"},
		{env: "prod", confirmEnv: "prod"},
		{env: "prod", confirmEnv: "staging", reason: "x"},
		{env: "prod", confirmEnv: "prod", reason: "   "},
	} {
		if err := checkDBQueryPolicy(bad, audited); exitCodeForError(err) != exitWrong {
			t.Errorf("%+v on an audited env: err = %v, want exit 1", bad, err)
		}
	}
	if err := checkDBQueryPolicy(dbQueryOptions{env: "prod", confirmEnv: "prod", reason: "ISS-7f3a"}, audited); err != nil {
		t.Errorf("confirmed audited query refused: %v", err)
	}
	if err := checkDBQueryPolicy(dbQueryOptions{env: "prod", confirmEnv: "prod", reason: "x"}, deny); exitCodeForError(err) != exitWrong {
		t.Errorf("deny env: err = %v, want exit 1", err)
	}
}

func TestDBQueryErrorExitCodes(t *testing.T) {
	for kind, want := range map[dbquery.Kind]int{
		dbquery.KindRefused:     exitWrong,
		dbquery.KindReadOnly:    exitWrong,
		dbquery.KindPermission:  exitWrong,
		dbquery.KindSQL:         exitWrong,
		dbquery.KindTimeout:     exitUndetermined,
		dbquery.KindCanceled:    exitUndetermined,
		dbquery.KindUnavailable: exitUndetermined,
		dbquery.KindAudit:       exitUndetermined,
	} {
		if got := exitCodeForError(dbQueryError(&dbquery.Error{Kind: kind, Message: "m"})); got != want {
			t.Errorf("%s: exit %d, want %d", kind, got, want)
		}
	}
}

func fixtureResult() *dbquery.Result {
	return &dbquery.Result{
		Columns: []dbquery.Column{{Name: "id", PGType: "int4"}, {Name: "name", PGType: "text"}, {Name: "meta", PGType: "jsonb"}, {Name: "ok", PGType: "bool"}},
		Rows: [][]any{
			{json.Number("1"), "ada", json.RawMessage(`{"a":1}`), true},
			{json.Number("2"), "multi\nline, \"quoted\"", nil, false},
		},
		RowCount: 2, Truncated: true, TruncatedBy: "rows", DurationMS: 3, Bytes: 40,
	}
}

func TestDBQueryTableAndCSV(t *testing.T) {
	var table bytes.Buffer
	if err := writeDBQueryTable(&table, fixtureResult(), nil, nil); err != nil {
		t.Fatal(err)
	}
	wantTable := "id  name                  meta     ok\n" +
		"1   ada                   {\"a\":1}  true\n" +
		"2   multi line, \"quoted\"  NULL     false\n" +
		"(2 rows, 3 ms; truncated by the rows cap, raise --max-rows or narrow the query)\n"
	if table.String() != wantTable {
		t.Errorf("table:\n%s\nwant:\n%s", table.String(), wantTable)
	}

	var csvOut bytes.Buffer
	if err := writeDBQueryCSV(&csvOut, fixtureResult()); err != nil {
		t.Fatal(err)
	}
	wantCSV := "id,name,meta,ok\n" +
		"1,ada,\"{\"\"a\"\":1}\",true\n" +
		"2,\"multi\nline, \"\"quoted\"\"\",,false\n"
	if csvOut.String() != wantCSV {
		t.Errorf("csv:\n%s\nwant:\n%s", csvOut.String(), wantCSV)
	}
}

func TestDBQueryRefusesTransportsItCannotReach(t *testing.T) {
	for _, tc := range []struct{ runtime, dsn string }{
		{RuntimeCluster, ""},
		{RuntimeHost, ""}, // no DATABASE_URL declared
	} {
		t.Run(tc.runtime+tc.dsn, func(t *testing.T) {
			withKCLFixture(t, dbQueryFixture(tc.runtime, tc.dsn))
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			out, err := runDBQueryCmd(t, "dev", "select 1", "--json")
			if exitCodeForError(err) != exitUndetermined {
				t.Fatalf("err = %v, want exit 2", err)
			}
			var doc dbQueryDocument
			if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
				t.Fatalf("not one JSON document: %v\n%s", jerr, out)
			}
			if doc.OK || doc.ExitCode != exitUndetermined || doc.Schema != dbQuerySchema || doc.Error == "" {
				t.Errorf("envelope %+v", doc.jsonEnvelope)
			}
		})
	}
}

// End to end against a real Postgres: the workload's resolved DATABASE_URL,
// rows in every format, and writes refused with exit 1.
func TestDBQueryDirectAgainstPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres (pgtest); runs in task test")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE jobs (id int PRIMARY KEY, crew text); INSERT INTO jobs VALUES (1, 'a'), (2, NULL), (3, 'b')`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)

	withKCLFixture(t, dbQueryFixture(RuntimeHost, dsn))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out, err := runDBQueryCmd(t, "dev", "select id, crew from jobs order by id")
	if err != nil {
		t.Fatalf("query: %v\n%s", err, out)
	}
	if want := "id  crew\n1   a\n2   NULL\n3   b\n(3 rows, "; !strings.HasPrefix(out, want) {
		t.Errorf("table:\n%s", out)
	}

	out, err = runDBQueryCmd(t, "dev", "select count(*) as n from jobs where crew is null", "--json")
	if err != nil {
		t.Fatalf("json query: %v", err)
	}
	var doc struct {
		OK       bool             `json:"ok"`
		ExitCode int              `json:"exit_code"`
		Schema   string           `json:"schema"`
		Scope    dbQueryScope     `json:"scope"`
		Columns  []dbquery.Column `json:"columns"`
		Rows     [][]json.Number  `json:"rows"`
		RowCount int              `json:"row_count"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if !doc.OK || doc.Schema != dbQuerySchema || doc.RowCount != 1 || doc.Rows[0][0] != "1" || doc.Columns[0].PGType != "int8" {
		t.Errorf("json doc %+v", doc)
	}
	if doc.Scope != (dbQueryScope{Env: "dev", Transport: "direct", Workload: "api", Policy: string(release.DBQueryOpen)}) {
		t.Errorf("scope %+v", doc.Scope)
	}

	file := t.TempDir() + "/q.sql"
	if err := os.WriteFile(file, []byte("select id from jobs where id < 3 order by id"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runDBQueryCmd(t, "dev", "-f", file, "--format", "csv")
	if err != nil || out != "id\n1\n2\n" {
		t.Errorf("csv from file: %v\n%q", err, out)
	}

	out, err = runDBQueryCmd(t, "dev", "select id from jobs order by id", "--max-rows", "2", "--json")
	if err != nil || !strings.Contains(out, `"truncated": true`) || !strings.Contains(out, `"truncated_by": "rows"`) {
		t.Errorf("row cap: %v\n%s", err, out)
	}

	for _, write := range []string{
		"insert into jobs values (4, 'c')",
		"update jobs set crew = 'x'",
		"select 1; delete from jobs",
		"with d as (delete from jobs returning id) select * from d",
	} {
		out, err := runDBQueryCmd(t, "dev", write, "--json")
		if exitCodeForError(err) != exitWrong {
			t.Errorf("%q: err = %v, want exit 1", write, err)
		}
		if !strings.Contains(out, `"ok": false`) || !strings.Contains(out, `"exit_code": 1`) {
			t.Errorf("%q: envelope:\n%s", write, out)
		}
	}
	out, err = runDBQueryCmd(t, "dev", "select count(*) from jobs", "--format", "csv")
	if err != nil || out != "count\n3\n" {
		t.Errorf("a write landed: %v %q", err, out)
	}
}

// releaseStubbed is set by a test that installs its own liveReleaseAt.
var releaseStubbed bool

func TestReleaseFromAt(t *testing.T) {
	if releaseFromAt(releasetimeline.AtResult{BeforeHistory: true}) != nil {
		t.Error("nothing promoted yet is no release")
	}
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	got := releaseFromAt(releasetimeline.AtResult{Live: &releasetimeline.Entry{Version: "v1.4.2", PromotionID: "p-9", At: at}, Unsettled: true})
	if got == nil || got.Version != "v1.4.2" || got.PromotionID != "p-9" || !got.PromotedAt.Equal(at) || !got.Unsettled {
		t.Errorf("%+v", got)
	}
}

// The release live when the query ran is stamped on the document and the
// table footer; a ledger that cannot be read warns and never fails the query.
func TestDBQueryStampsTheLiveRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres (pgtest); runs in task test")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	withKCLFixture(t, dbQueryFixture(RuntimeHost, dsn))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	prev := liveReleaseAt
	releaseStubbed = true
	t.Cleanup(func() { liveReleaseAt, releaseStubbed = prev, false })
	var asked string
	liveReleaseAt = func(_ context.Context, env string, at time.Time) (*liveRelease, error) {
		asked = env
		if at.IsZero() {
			t.Error("the lookup was not given the query time")
		}
		return &liveRelease{Version: "v1.4.2", PromotionID: "p-9", PromotedAt: at.Add(-time.Hour)}, nil
	}
	out, err := runDBQueryCmd(t, "dev", "select 1 as n", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if asked != "dev" || !strings.Contains(out, `"release": {`) || !strings.Contains(out, `"version": "v1.4.2"`) {
		t.Errorf("release not stamped (asked %q):\n%s", asked, out)
	}
	out, err = runDBQueryCmd(t, "dev", "select 1 as n")
	if err != nil || !strings.Contains(out, "; release v1.4.2") {
		t.Errorf("table footer: %v\n%s", err, out)
	}

	liveReleaseAt = func(context.Context, string, time.Time) (*liveRelease, error) {
		return nil, errors.New("ledger unreadable")
	}
	cmd := newDBCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"query", "dev", "select 1 as n", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("an unreadable ledger failed the query: %v", err)
	}
	if strings.Contains(stdout.String(), `"release"`) || !strings.Contains(stderr.String(), "release live in dev is unknown") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}
