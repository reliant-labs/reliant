// Package obstest boots a throwaway ClickHouse holding the OpenTelemetry
// exporter's tables and seeds it with a known set of traces, logs and metrics,
// for the tests of the obs verbs (obsbackend's own, and the CLI's end to end).
package obstest

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// exporterSchema is the ClickHouse exporter's tables (v0.162.0), trimmed to the
// five the obs verbs read; indexes and TTLs are omitted.
//
//go:embed exporter_schema.sql
var exporterSchema string

// TraceID is the seeded trace that fails in the database.
const TraceID = "0af7651916cd43dd8448eb211c80319c"

// clickHouseImage is the server the local ClickStack embeds, alpine build.
const clickHouseImage = "clickhouse/clickhouse-server:26.8.24-alpine"

// Writer writes setup statements. The DockerRunner under test is always
// readonly=2 (that is the point), so seeding goes around it.
type Writer func(t *testing.T, sql string)

// Start boots a throwaway ClickHouse with the exporter's tables and
// returns a DockerRunner over it (the same clickhouse-client invocation the
// ClickStack path uses, through `docker exec` instead of `docker compose
// exec`) and a writer for seeding.
func Start(t *testing.T) (obsbackend.DockerRunner, Writer) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable: " + err.Error())
	}
	name := fmt.Sprintf("forge-obsbackend-%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "CLICKHOUSE_USER=default", "-e", "CLICKHOUSE_PASSWORD=pw", clickHouseImage).CombinedOutput(); err != nil {
		t.Skipf("cannot start %s: %v\n%s", clickHouseImage, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })

	runner := obsbackend.DockerRunner{Env: "dev", Exec: func(ctx context.Context, script string, args []string) ([]byte, error) {
		argv := append([]string{"exec", name, "sh", "-c", script, "sh"}, args...)
		cmd := exec.CommandContext(ctx, "docker", argv...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, stderr.String())
		}
		return out, nil
	}}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		if _, err := runner.Run(context.Background(), obsquery.Stmt{SQL: "SELECT 1"}, obsbackend.DefaultLimits()); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("clickhouse never became ready: %v", err)
		}
	}
	write := func(t *testing.T, sql string) {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", name, "clickhouse-client", "--password", "pw", "--multiquery")
		cmd.Stdin = strings.NewReader(sql)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clickhouse setup: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
	}
	write(t, exporterSchema)
	return runner, write
}

// Seed writes one HTTP request that fails in the database, one that is slow,
// and a bulk of fast ones, plus the logs, metrics and a second env that must
// stay invisible to `dev`.
func Seed(t *testing.T, mustExec func(*testing.T, string), now time.Time) {
	t.Helper()
	ts := func(offset time.Duration) string {
		return fmt.Sprintf("toDateTime64('%s', 9)", now.Add(offset).UTC().Format("2006-01-02 15:04:05.000000000"))
	}
	span := func(trace, span, parent, svc, name, kind, status string, offset time.Duration, dur time.Duration, env string, events string) string {
		return fmt.Sprintf(`INSERT INTO otel_traces (Timestamp, TraceId, SpanId, ParentSpanId, SpanName, SpanKind, ServiceName, ResourceAttributes, SpanAttributes, Duration, StatusCode, StatusMessage, `+"`Events.Timestamp`, `Events.Name`, `Events.Attributes`"+`)
SELECT %s, '%s', '%s', '%s', '%s', '%s', '%s', map('deployment.environment.name','%s','service.name','%s'), map('db.system','postgresql'), %d, '%s', '%s' %s`,
			ts(offset), trace, span, parent, name, kind, svc, env, svc, dur.Nanoseconds(), status, map[bool]string{true: "relation jobs does not exist", false: ""}[status == "Error"], events)
	}
	tid := TraceID
	exc := `, [` + ts(-9*time.Minute) + `], ['exception'], [map('exception.type','*pgconn.PgError','exception.message','relation "jobs" does not exist')]`
	noEv := `, [], [], []`
	mustExec(t, span(tid, "s1", "", "api", "GET /v1/jobs", "Server", "Error", -10*time.Minute, 90*time.Millisecond, "dev", exc))
	mustExec(t, span(tid, "s2", "s1", "api", "SELECT jobs", "Client", "Error", -10*time.Minute+5*time.Millisecond, 20*time.Millisecond, "dev", exc))
	for i := 0; i < 12; i++ {
		mustExec(t, span(tid, fmt.Sprintf("q%d", i), "s1", "api", "SELECT crews", "Client", "Unset", -10*time.Minute+time.Duration(30+i)*time.Millisecond, 2*time.Millisecond, "dev", noEv))
	}
	mustExec(t, span("bb", "b1", "", "api", "POST /v1/jobs", "Server", "Unset", -8*time.Minute, 900*time.Millisecond, "dev", noEv))
	for i := 0; i < 20; i++ {
		mustExec(t, span(fmt.Sprintf("ff%02d", i), "f1", "", "api", "GET /healthz", "Server", "Unset", -time.Duration(i+1)*time.Minute, 3*time.Millisecond, "dev", noEv))
	}
	mustExec(t, span("ee", "e1", "", "api", "GET /v1/jobs", "Server", "Error", -5*time.Minute, 500*time.Millisecond, "prod", noEv))

	log := func(offset time.Duration, sev string, num int, svc, body, trace, env string) string {
		return fmt.Sprintf(`INSERT INTO otel_logs (Timestamp, TraceId, SeverityText, SeverityNumber, ServiceName, Body, ResourceAttributes, LogAttributes)
SELECT %s, '%s', '%s', %d, '%s', '%s', map('deployment.environment.name','%s'), map('http.route','/v1/jobs')`, ts(offset), trace, sev, num, svc, body, env)
	}
	mustExec(t, log(-9*time.Minute, "ERROR", 17, "api", "query failed: relation jobs does not exist", tid, "dev"))
	mustExec(t, log(-9*time.Minute+time.Second, "INFO", 9, "api", "request done", tid, "dev"))
	mustExec(t, log(-7*time.Minute, "WARN", 13, "worker", "retrying job", "", "dev"))
	mustExec(t, log(-6*time.Minute, "ERROR", 17, "api", "prod only failure", "", "prod"))

	mustExec(t, fmt.Sprintf(`INSERT INTO otel_metrics_sum (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, TimeUnix, Value, AggregationTemporality, IsMonotonic)
SELECT map('deployment.environment.name','dev'), 'api', 'rpc.server.calls', '{call}', map('rpc.method', if(n %% 2 = 0, 'List', 'Get')), toDateTime('%s') - INTERVAL (5 - intDiv(n, 2)) MINUTE, 100 + intDiv(n, 2) * 10, 2, true
FROM (SELECT number n FROM numbers(12))`, now.UTC().Format("2006-01-02 15:04:05")))
	mustExec(t, fmt.Sprintf(`INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, TimeUnix, Value)
SELECT map('deployment.environment.name','dev'), 'api', 'heap', 'By', map(), toDateTime('%s') - INTERVAL n MINUTE, 1000 + n FROM (SELECT number n FROM numbers(5))`, now.UTC().Format("2006-01-02 15:04:05")))
}
