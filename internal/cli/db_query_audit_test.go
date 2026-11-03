package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/dbquery"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// readDBQueryAudit returns the records of env in the current project.
func readDBQueryAudit(t *testing.T, env string) []dbQueryAuditRecord {
	t.Helper()
	path, err := dbQueryAuditPath(projectDirForKCL(), env)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []dbQueryAuditRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r dbQueryAuditRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("record %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The auditor appends one line per attempt, failures included, and logs a
// line naming the record; an env that is not a file name has no record file.
func TestDBQueryAuditorAppendsOneLinePerAttempt(t *testing.T) {
	dir := t.TempDir()
	path, err := dbQueryAuditPath(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".forge", "audit", "prod.jsonl"); path != want {
		t.Errorf("path %s, want %s", path, want)
	}
	for _, env := range []string{"", ".", "..", "a/b", "../prod"} {
		if _, err := dbQueryAuditPath(dir, env); err == nil {
			t.Errorf("env %q: want a refusal", env)
		}
	}
	var log bytes.Buffer
	a := newDBQueryAuditor(path, dbQueryAuditRecord{ID: "dbq_1", Env: "prod", Transport: "port-forward", Policy: "audited", Reason: "ISS-1", Role: "forge_ro"}, &log)
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	if err := a.Audit(context.Background(), dbquery.Event{SQL: "select 1", SQLSHA256: "h1", Outcome: "ok", RowCount: 1, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := a.Audit(context.Background(), dbquery.Event{SQL: "delete from t", SQLSHA256: "h2", Outcome: string(dbquery.KindRefused), Err: errors.New("refused"), StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d records, want 2:\n%s", len(lines), b)
	}
	var r dbQueryAuditRecord
	if err := json.Unmarshal([]byte(lines[1]), &r); err != nil {
		t.Fatal(err)
	}
	if r.Schema != dbQueryAuditSchema || r.ID != "dbq_1" || r.SQL != "delete from t" || r.Outcome != "refused" || r.Error != "refused" || r.Reason != "ISS-1" || r.Role != "forge_ro" || !r.At.Equal(at) {
		t.Errorf("record %+v", r)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record file mode: %v %v", fi.Mode(), err)
	}
	if !strings.Contains(log.String(), `"audit_id":"dbq_1"`) || !strings.Contains(log.String(), `"record":"`+path+`"`) || strings.Contains(log.String(), "delete from t") {
		t.Errorf("log line names the record, not the statement:\n%s", log.String())
	}
}

// On the direct transport (a workload's DATABASE_URL) every attempt is
// recorded in .forge/audit/<env>.jsonl: who, the env, the reason, the
// statement and its hash, the outcome and the row count, the refused write
// included. The connection carries the record's id as its application_name.
func TestDBQueryDirectWritesAnAuditRecord(t *testing.T) {
	testkit.Slow(t, "boots a real postgres (pgtest)")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	withKCLFixture(t, dbQueryFixture(RuntimeHost, dsn))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	const sql = "select current_setting('application_name') as app"
	out, err := runDBQueryCmd(t, "dev", sql, "--json", "--reason", "ISS-2")
	if err != nil {
		t.Fatalf("query: %v\n%s", err, out)
	}
	var doc struct {
		Rows  [][]string   `json:"rows"`
		Audit dbQueryAudit `json:"audit"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	recs := readDBQueryAudit(t, "dev")
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1", len(recs))
	}
	r := recs[0]
	sum := sha256.Sum256([]byte(sql))
	u, _ := url.Parse(dsn)
	if r.ID == "" || r.ID != doc.Audit.ID || r.Env != "dev" || r.Transport != "direct" || r.Workload != "api" || r.Policy != "open" ||
		r.Reason != "ISS-2" || r.SQL != sql || r.SQLSHA256 != hex.EncodeToString(sum[:]) || doc.Audit.SQLSHA256 != r.SQLSHA256 ||
		r.Outcome != "ok" || r.RowCount != 1 || r.Role != u.User.Username() || r.Who.User == "" {
		t.Errorf("record %+v (doc audit %+v)", r, doc.Audit)
	}
	if doc.Audit.Record == "" || !strings.HasSuffix(doc.Audit.Record, filepath.Join(".forge", "audit", "dev.jsonl")) {
		t.Errorf("doc audit record %q", doc.Audit.Record)
	}
	if len(doc.Rows) != 1 || doc.Rows[0][0] != Name()+" db query "+r.ID {
		t.Errorf("application_name %v, want it to name record %s", doc.Rows, r.ID)
	}

	if _, err := runDBQueryCmd(t, "dev", "with d as (delete from no_such_table returning 1) select 1"); exitCodeForError(err) != exitWrong {
		t.Errorf("a write: err = %v, want exit 1", err)
	}
	recs = readDBQueryAudit(t, "dev")
	if len(recs) != 2 || recs[1].Outcome == "ok" || recs[1].Error == "" || recs[1].ID == r.ID {
		t.Errorf("the refused write is not on record: %+v", recs)
	}
}

// A real env is read only as a role that cannot write. A workload's own
// DATABASE_URL (here a superuser's) is refused before anything runs, and so
// leaves no record; --allow-writable-role reads as it and the record says
// so; a SELECT-only role needs no flag.
func TestDBQueryRealEnvRefusesAWritableRole(t *testing.T) {
	testkit.Slow(t, "boots a real postgres (pgtest)")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	sfx := make([]byte, 8)
	if _, err := rand.Read(sfx); err != nil {
		t.Fatal(err)
	}
	reader := "fbq_ro_" + hex.EncodeToString(sfx)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE TABLE jobs (id int PRIMARY KEY); INSERT INTO jobs VALUES (1);
		CREATE ROLE %s LOGIN PASSWORD 'pw'; GRANT SELECT ON jobs TO %s`, reader, reader)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON jobs FROM %s; DROP ROLE IF EXISTS %s`, reader, reader))
	})
	confirm := []string{"--confirm-env", "prod", "--reason", "ISS-3"}

	withKCLFixture(t, dbQueryFixtureAs("", "audited", RuntimeHost, dsn))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_, err = runDBQueryCmd(t, append([]string{"prod", "select id from jobs"}, confirm...)...)
	if exitCodeForError(err) != exitWrong || err == nil || !strings.Contains(err.Error(), "can do more than read") || !strings.Contains(err.Error(), "SUPERUSER") {
		t.Fatalf("a writable role on a real env: err = %v, want exit 1 naming what it can do", err)
	}
	if recs := readDBQueryAudit(t, "prod"); len(recs) != 0 {
		t.Errorf("a refused role ran something: %+v", recs)
	}

	out, err := runDBQueryCmd(t, append([]string{"prod", "select id from jobs", "--allow-writable-role"}, confirm...)...)
	if err != nil {
		t.Fatalf("--allow-writable-role: %v\n%s", err, out)
	}
	recs := readDBQueryAudit(t, "prod")
	if len(recs) != 1 || !recs[0].AllowWritableRole || len(recs[0].RoleWrites) == 0 || recs[0].Reason != "ISS-3" || recs[0].Policy != "audited" {
		t.Errorf("records %+v", recs)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(reader, "pw")
	withKCLFixture(t, dbQueryFixtureAs("", "audited", RuntimeHost, u.String()))
	if out, err := runDBQueryCmd(t, append([]string{"prod", "select id from jobs"}, confirm...)...); err != nil {
		t.Errorf("a SELECT-only role on a real env: %v\n%s", err, out)
	}
	if recs := readDBQueryAudit(t, "prod"); len(recs) != 1 || recs[0].Role != reader || len(recs[0].RoleWrites) != 0 {
		t.Errorf("records %+v", recs)
	}
}
