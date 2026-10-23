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
		obsquery.ServerCulpritSQL+", "+obsquery.MessageNormSQL("Body")+" FROM logs ORDER BY Id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id                       uint32
			fp, title, culprit, norm string
		)
		if err := rows.Scan(&id, &fp, &title, &culprit, &norm); err != nil {
			t.Fatal(err)
		}
		r := recs[id]
		for _, cmp := range []struct{ what, sql, gold string }{
			{"fingerprint", fp, obsquery.ServerFingerprint(r)},
			{"title", title, obsquery.ServerTitle(r)},
			{"culprit", culprit, obsquery.ServerCulprit(r)},
			{"norm(Body)", norm, obsquery.NormalizeMessage(r.Body)},
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
