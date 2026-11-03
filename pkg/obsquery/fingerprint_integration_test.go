//go:build integration

package obsquery_test

// The sfp1 corpus: ServerFingerprintSQL (and the title, culprit and
// MessageNorm expressions) evaluated by a real ClickHouse must equal the Go
// reference on every record. Gated by the `integration` tag and by -short.
//
//	go test -tags integration -run Corpus ./pkg/obsquery/

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// chImage is the ClickHouse pkg/alerting's integration test runs.
const chImage = "clickhouse/clickhouse-server:26.8.24-alpine"

func startClickHouse(t *testing.T) clickhouse.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: starts a ClickHouse container")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	if exec.Command("docker", "image", "inspect", chImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", chImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", chImage, err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	name := fmt.Sprintf("obsquery-sfp1-ch-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		"-e", "CLICKHOUSE_USER=fp", "-e", "CLICKHOUSE_PASSWORD=pw", chImage).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(90 * time.Second)
	for {
		c, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Database: "default", Username: "fp", Password: "pw"}, DialTimeout: 5 * time.Second})
		if err == nil {
			if err = c.Ping(context.Background()); err == nil {
				t.Cleanup(func() { _ = c.Close() })
				return c
			}
			_ = c.Close()
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("clickhouse never became ready: %v\n%s", err, out)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// corpus is every shape sfp1 must group: rpc failures in two procedures, one
// site with changing ids, generic errors with different messages, error.group,
// records with no source location, multi-line and over-long messages, and
// messages that exercise each mask and its boundaries.
func corpus() []obsquery.ServerErrorRecord {
	const fn = "github.com/acme/app/internal/handlers/jobs.(*Service).ListJobs"
	attrs := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	r := func(service, scope, body string, kv ...string) obsquery.ServerErrorRecord {
		return obsquery.ServerErrorRecord{ServiceName: service, ScopeName: scope, Body: body, Attributes: attrs(kv...)}
	}
	loc := []string{obsquery.LogKeyCodeFunction, fn, obsquery.LogKeyCodeFile, "internal/handlers/jobs/rpc_list_jobs.go", "code.line.number", "42"}
	with := func(extra ...string) []string { return append(append([]string{}, loc...), extra...) }
	pg := []string{obsquery.LogKeyExceptionType, "*pgconn.PgError", obsquery.LogKeyExceptionMessage, `ERROR: relation "jobs" does not exist (SQLSTATE 42P01)`}

	out := []obsquery.ServerErrorRecord{
		r("api", "app", "rpc failed", with(append([]string{obsquery.LogKeyProcedure, "/jobs.v1.JobService/ListJobs"}, pg...)...)...),
		r("api", "app", "rpc failed", with(append([]string{obsquery.LogKeyProcedure, "/jobs.v1.JobService/GetJob"}, pg...)...)...),
		r("api", "app", "reconcile failed", with(obsquery.LogKeyExceptionType, "*errors.errorString", obsquery.LogKeyExceptionMessage, "ledger drift on 12 rows")...),
		r("api", "app", "reconcile failed", with(obsquery.LogKeyExceptionType, "*errors.errorString", obsquery.LogKeyExceptionMessage, "missing invoice 'INV-7'")...),
		r("api", "app", "reconcile failed", with(obsquery.LogKeyExceptionType, "*fmt.wrapError", obsquery.LogKeyExceptionMessage, "wrap: x")...),
		r("api", "app", "reconcile failed", with(obsquery.LogKeyExceptionType, "*fmt.wrapErrors", obsquery.LogKeyExceptionMessage, "a\nb")...),
		r("api", "app", "no error attached", loc...),
		r("api", "app", "pinned", with(obsquery.LogKeyErrorGroup, "billing-reconcile-drift")...),
		r("worker", "app", "pinned", with(obsquery.LogKeyErrorGroup, "billing-reconcile-drift")...),
		r("api", "go.temporal.io/sdk", "activity failed"),
		r("api", "", "no scope, no location"),
		r("api", "app", "", loc...),
		r("api", "app", "first line\nsecond line\nthird", loc...),
		r("api", "app", strings.Repeat("abc ", 80), loc...),
		r("api", "app", strings.Repeat("é", 150), loc...),
		r("api", "app", "panic recovered", with(obsquery.LogKeyExceptionType, "runtime.boundsError", obsquery.LogKeyExceptionMessage, "index out of range [3] with length 2")...),
		r("api", "app", `quote "unterminated`, loc...),
		r("api", "app", `mixed 'single' and "double" quotes`, loc...),
		r("api", "app", "tab\tseparated\tvalues 1\t2", loc...),
		r("api", "app", "unicode ✓ 名前 12", loc...),
		r("api", "app", "url mailto:x@y.z and s3://bucket/key and http://a.b/c?d=1#e", loc...),
		r("api", "app", "url in quotes 'https://x.example/a b' end", loc...),
		r("api", "app", "url then paren (https://x.example/a) end", loc...),
		r("api", "app", "UUID 7F3A9C21-0000-4000-8000-000000000001 upper", loc...),
		r("api", "app", "hex 1234abcd and deadbeef00 and 1234567 short", loc...),
		r("api", "app", "words with digits v2 abc123 123abc 4", loc...),
		r("api", "app", "negative -5 and decimal 3.14 and 1,000", loc...),
		r("api", "app", "ends in number 99", loc...),
		r("api", "app", "glued units 12ms 3s 4KiB", loc...),
		r("api", "app", "\nleading newline", loc...),
		r("api", "app", "crlf line\r\nnext", loc...),
		r("api", "app", "unit\x1fseparator in body", loc...),
		r("api", "app", "rpc failed", with(obsquery.LogKeyExceptionType, "context.deadlineExceededError", obsquery.LogKeyProcedure, "/jobs.v1.JobService/ListJobs")...),
		r("api", "app", "rpc failed", with(obsquery.LogKeyExceptionType, "*connect.Error", obsquery.LogKeyExceptionMessage, "unavailable")...),
		r("api", "app", "file but no function", obsquery.LogKeyCodeFile, "x.go"),

		// sfp2: the cause in the `error` attribute refines records with no type.
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, `ERROR: invalid byte sequence for encoding "UTF8": 0xe2 0x2e (SQLSTATE 22021)`)...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "ERROR: could not serialize access due to read/write dependencies (SQLSTATE 40001)")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, `ERROR: prepared statement "stmtcache_8c1f9e" already exists (SQLSTATE 42P05)`)...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "failed to begin transaction: timeout: context deadline exceeded after 30000 ms")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "*pgconn.PgError: ERROR: deadlock detected (SQLSTATE 40P01)")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "*net.OpError: read tcp 10.1.2.3:41234->10.0.0.5:5432: i/o timeout")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "*errors.errorString: ledger drift on 12 rows")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "failed to connect to `host=db`: dial tcp 10.0.0.5:5432: connect: connection refused")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "multi\nline error\n(SQLSTATE 57P01) on the second line")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "unicode ✓ 名前 12 failure")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, strings.Repeat("e", 300))...),
		// not refined: typed, pinned, or no error at all
		r("api", "app", "Transaction failed", with(obsquery.LogKeyExceptionType, "*pgconn.PgError", obsquery.LogKeyError, "ERROR: x (SQLSTATE 40001)")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyErrorGroup, "tx", obsquery.LogKeyError, "ERROR: x (SQLSTATE 40001)")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyExceptionMessage, "ERROR: x (SQLSTATE 40001)")...),
		r("api", "app", "Transaction failed", with(obsquery.LogKeyError, "")...),
	}
	// One site, changing ids: every one of these is one issue. (An integer
	// glued to a word, "12ms", is not masked: \b sees no boundary. That is
	// the browser masks' behaviour too, and sfp1 keeps their masks.)
	for i := 0; i < 20; i++ {
		body := fmt.Sprintf("job %d for crew %08x-0000-4000-8000-%012d failed at https://h%d.example/p?q=%d after %d ms", i*37, i*7919, i, i, i, i*3)
		out = append(out, r("api", "app", body, with(pg...)...))
	}
	return out
}

func TestServerFingerprintCorpusMatchesSQL(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	if err := c.Exec(ctx, `CREATE TABLE logs (
		Id UInt32, ServiceName LowCardinality(String), ScopeName String, Body String,
		LogAttributes Map(LowCardinality(String), String)
	) ENGINE = MergeTree ORDER BY Id`); err != nil {
		t.Fatal(err)
	}
	recs := corpus()
	if len(recs) < 50 {
		t.Fatalf("corpus has %d records, want at least 50", len(recs))
	}
	batch, err := c.PrepareBatch(ctx, "INSERT INTO logs")
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range recs {
		a := r.Attributes
		if a == nil {
			a = map[string]string{}
		}
		if err := batch.Append(uint32(i), r.ServiceName, r.ScopeName, r.Body, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	rows, err := c.Query(ctx, "SELECT Id, "+obsquery.ServerFingerprintSQL+", "+obsquery.ServerTitleSQL+", "+
		obsquery.ServerCulpritSQL+", "+obsquery.MessageNormSQL("Body")+", "+obsquery.ServerCauseSQL+" FROM logs ORDER BY Id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id                              uint32
			fp, title, culprit, norm, cause string
		)
		if err := rows.Scan(&id, &fp, &title, &culprit, &norm, &cause); err != nil {
			t.Fatal(err)
		}
		r := recs[id]
		for _, cmp := range []struct{ what, sql, gold string }{
			{"fingerprint", fp, obsquery.ServerFingerprint(r)},
			{"title", title, obsquery.ServerTitle(r)},
			{"culprit", culprit, obsquery.ServerCulprit(r)},
			{"norm(Body)", norm, obsquery.NormalizeMessage(r.Body)},
			{"cause", cause, obsquery.ServerCause(r)},
		} {
			if cmp.sql != cmp.gold {
				t.Errorf("record %d (%q): %s: SQL %q, Go %q", id, r.Body, cmp.what, cmp.sql, cmp.gold)
			}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(recs) {
		t.Fatalf("read %d rows, inserted %d", n, len(recs))
	}

	// The grouping the corpus is built to show.
	fps := make([]string, len(recs))
	for i, r := range recs {
		fps[i] = obsquery.ServerFingerprint(r)
	}
	if fps[0] == fps[1] {
		t.Error("rpc failed in two procedures is one issue")
	}
	if fps[2] == fps[3] {
		t.Error("two errors.New messages at one site are one issue")
	}
	if fps[7] == fps[8] {
		t.Error("error.group merged two services")
	}
	ids := fps[len(recs)-20:]
	for _, fp := range ids[1:] {
		if fp != ids[0] {
			t.Fatal("one call site with changing ids split into several issues")
		}
	}
}

// T7 on a real ClickHouse: the four "Transaction failed" causes are four issues,
// one cause with changing ids is one, and ServerIssueIncidents reads the cause and
// first sighting GroupIncidents clusters (T6).
func TestServerIssuesSeparateCausesAndFormIncidentsOnClickHouse(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	if err := c.Exec(ctx, `CREATE TABLE otel_logs (
		Timestamp DateTime64(9), TraceId String, SeverityNumber UInt8, ServiceName LowCardinality(String), ScopeName String, Body String,
		ResourceAttributes Map(LowCardinality(String), String), LogAttributes Map(LowCardinality(String), String)
	) ENGINE = MergeTree ORDER BY Timestamp`); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 23, 12, 12, 0, 0, time.UTC)
	res := map[string]string{"deployment.environment.name": "dev"}
	batch, err := c.PrepareBatch(ctx, "INSERT INTO otel_logs")
	if err != nil {
		t.Fatal(err)
	}
	add := func(min int, svc, fn, errText string) {
		attrs := map[string]string{obsquery.LogKeyCodeFunction: fn}
		if errText != "" {
			attrs[obsquery.LogKeyError] = errText
		}
		if err := batch.Append(base.Add(time.Duration(min)*time.Minute), "", uint8(17), svc, "app", "Transaction failed", res, attrs); err != nil {
			t.Fatal(err)
		}
	}
	// One site, four causes, a new id each time.
	add(0, "worker", "pkg.Tx", `ERROR: invalid byte sequence for encoding "UTF8": 0xe2 (SQLSTATE 22021)`)
	add(1, "worker", "pkg.Tx", "ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)")
	add(2, "worker", "pkg.Tx", `ERROR: prepared statement "stmtcache_aa" already exists (SQLSTATE 42P05)`)
	add(3, "worker", "pkg.Tx", "failed to begin transaction: context deadline exceeded after 30000 ms")
	add(4, "worker", "pkg.Tx", `ERROR: prepared statement "stmtcache_bb" already exists (SQLSTATE 42P05)`)
	// A dead database, three callers in three services: one incident.
	add(0, "temporal", "temporal.Fetch", "ERROR: terminating connection due to administrator command (SQLSTATE 57P01)")
	add(2, "api", "api.Handler", "ERROR: terminating connection due to administrator command (SQLSTATE 57P01)")
	add(5, "reconciler", "recon.Sweep", "ERROR: terminating connection due to administrator command (SQLSTATE 57P01)")
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	scope := obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Start: base.Add(-time.Hour), End: base.Add(time.Hour),
		Filters: []obsquery.AttrFilter{{Key: "deployment.environment.name", Op: "eq", Value: "dev"}}}
	ctxScope := func() obsquery.Scope { s := scope; s.Database = "default"; return s }()

	list, err := obsquery.ServerIssues{Scope: ctxScope, Service: "worker", Limit: 25}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(paramCtx(ctx, list), list.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, list.SQL)
	}
	titles := map[string]uint64{}
	for rows.Next() {
		var fp, svc, title, culprit, typ, trace, lv, fv, src, unit string
		var events, affected uint64
		var first, last time.Time
		if err := rows.Scan(&fp, &svc, &title, &culprit, &typ, &events, &affected, &first, &last, &trace, &lv, &fv, &src, &unit); err != nil {
			t.Fatal(err)
		}
		titles[title] = events
	}
	rows.Close()
	if len(titles) != 4 {
		t.Fatalf("one message at one site with four causes must be 4 issues, got %d: %v", len(titles), titles)
	}
	if titles["SQLSTATE 42P05: Transaction failed"] != 2 {
		t.Errorf("the same prepared-statement clash with two statement names must be one issue of 2 events: %v", titles)
	}

	inc, err := obsquery.ServerIssueIncidents{Scope: ctxScope}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	rows, err = c.Query(paramCtx(ctx, inc), inc.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, inc.SQL)
	}
	var cands []obsquery.IncidentCandidate
	for rows.Next() {
		var fp, svc, cause string
		var first time.Time
		if err := rows.Scan(&fp, &svc, &cause, &first); err != nil {
			t.Fatal(err)
		}
		cands = append(cands, obsquery.IncidentCandidate{ID: svc + "/" + fp, Cause: cause, FirstSeen: first})
	}
	rows.Close()
	groups := obsquery.GroupIncidents(cands, obsquery.DefaultIncidentWindow)
	var found bool
	for _, g := range groups {
		if g.Cause == "SQLSTATE 57P01" {
			found = len(g.IDs) == 3
		}
	}
	if !found {
		t.Errorf("the three callers of the dead database must be one incident, got %+v", groups)
	}
}

func paramCtx(ctx context.Context, st obsquery.Stmt) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params)))
}
