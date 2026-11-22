package obsdash

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// overviewProbeSeed is a day of shop-api's server spans in env e1, for the
// overview's structured panels: r1's ordinary traffic (two failed Sends, one
// with no forge.probe and one with forge.probe = false, and a successful
// Send), and synthetic monitor spans (forge.probe = true): a failed Send and a
// failed health check on r1, and r2, a version only a probe ever reached.
const overviewProbeSeed = `
DROP DATABASE IF EXISTS forge_overview_test;
CREATE DATABASE forge_overview_test;
CREATE TABLE forge_overview_test.otel_traces (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), SpanName LowCardinality(String),
    SpanKind LowCardinality(String), StatusCode LowCardinality(String), StatusMessage String, Duration UInt64, TraceState String,
    ResourceAttributes Map(LowCardinality(String), String), SpanAttributes Map(LowCardinality(String), String)
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
INSERT INTO forge_overview_test.otel_traces VALUES
    ('2026-03-15 10:00:00', 'shop-api', 'shop.v1.ChatService/Send', 'Server', 'Error', 'boom', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:01:00', 'shop-api', 'shop.v1.ChatService/Send', 'Server', 'Error', 'boom', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {'forge.probe': 'false'}),
    ('2026-03-15 10:02:00', 'shop-api', 'shop.v1.ChatService/Send', 'Server', 'Ok', '', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:03:00', 'shop-api', 'shop.v1.ChatService/Send', 'Server', 'Error', 'boom', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-15 10:04:00', 'shop-api', 'shop.v1.HealthService/Check', 'Server', 'Error', 'probe failed', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-15 10:05:00', 'shop-api', 'shop.v1.ChatService/Send', 'Server', 'Ok', '', 100000000, '', {'service.version': 'r2', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'});
`

// TestOverviewExcludesProbesOnClickHouse runs the overview's structured panels
// that leave synthetic monitor spans out (forge.probe = true) against
// overviewProbeSeed, compiled by the compiler the console runs: ordinary
// traffic, forge.probe = false or no forge.probe at all, is counted; a probe
// never is. Same container and conventions as TestReleasesSQLOnClickHouse.
func TestOverviewExcludesProbesOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	exec1 := func(sql string, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", container, "clickhouse-client", "--password", "pw",
			"--database", "forge_overview_test", "--multiquery", "--format", "TSV"}, args...)...)
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	seed := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery")
	seed.Stdin = strings.NewReader(overviewProbeSeed)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	in := fixture()
	in.Processes = nil
	in.Deployed = Identities{ServiceNames: []string{"shop-api"}, Workloads: []string{"shop-api"}, RPCServiceNames: []string{"shop-api"}}
	panels := map[string]Panel{}
	for _, p := range Build(in).Dashboards[0].Panels {
		panels[p.ID] = p
	}
	end := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	for id, want := range map[string]string{
		// The two ordinary failed Sends, and no probe's Send or health check.
		"top-errors": "['shop-api','shop.v1.ChatService/Send','boom']\t2",
		// r1's three ordinary requests, and no r2.
		"request-rate-by-version": "2026-03-15 00:00:00\t['r1']\t3",
	} {
		p, ok := panels[id]
		if !ok {
			t.Fatalf("the overview has no panel %s", id)
		}
		if got := runStructured(t, exec1, "forge_overview_test", p, end.Add(-24*time.Hour), end); got != want {
			t.Errorf("overview %s in e1 =\n%s\nwant\n%s\n(a forge.probe span was counted, or ordinary traffic was dropped)", id, got, want)
		}
	}
}

// runStructured compiles a structured (non-sql) panel for env e1 over
// [start, end) in daily buckets, with the compiler the console runs, and runs
// it through exec1. That compiler names the database in its FROM, so the
// scope names the seed's.
func runStructured(t *testing.T, exec1 func(sql string, args ...string) string, database string, p Panel, start, end time.Time) string {
	t.Helper()
	sc := obsquery.Scope{Database: database, Start: start, End: end,
		EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "e1"}}}
	st, _, err := obsquery.PanelQuery{Scope: sc, Panel: compilerPanel(p), StepSeconds: 86400}.Compile()
	if err != nil {
		t.Fatalf("compile %s: %v", p.ID, err)
	}
	var args []string
	for k, v := range st.Params {
		args = append(args, "--param_"+k+"="+v)
	}
	return exec1(st.SQL, args...)
}

// compilerPanel is a dashboard panel in obsquery's vocabulary.
func compilerPanel(p Panel) obsquery.Panel {
	preds := func(in []Predicate) []obsquery.PanelPredicate {
		out := make([]obsquery.PanelPredicate, len(in))
		for i, w := range in {
			out[i] = obsquery.PanelPredicate{Field: w.Field, Op: string(w.Op), Value: w.Value, Values: w.Values}
		}
		return out
	}
	out := obsquery.Panel{ID: p.ID, Kind: string(p.Kind), Signal: string(p.Signal), Ratio: p.Ratio, Formula: p.Formula,
		Limit: p.Limit, SQL: p.SQL, Where: preds(p.Where), GroupBy: p.GroupBy, HasError: p.HasError}
	for _, s := range p.Series {
		out.Series = append(out.Series, obsquery.PanelSeries{Label: s.Label, Agg: string(s.Agg), Quantile: s.Quantile,
			Field: s.Field, MetricType: s.MetricType, Where: preds(s.Where)})
	}
	return out
}
