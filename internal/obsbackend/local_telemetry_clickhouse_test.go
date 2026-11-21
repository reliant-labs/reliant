package obsbackend_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsbackend/obstest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestLocalTelemetryArrivalAgainstClickHouse runs `forge obs telemetry`'s
// read through the real clickhouse-client invocation: per signal the newest
// row of each declared system, per scraped endpoint its newest up and sample
// counts, the env narrowing a project system and not a platform one, and a
// table the store lacks (the exporter schema has no profiles table) costing
// only its signal.
func TestLocalTelemetryArrivalAgainstClickHouse(t *testing.T) {
	testkit.Slow(t, "boots a ClickHouse container and reads declared-telemetry arrival from it")
	runner, write := obstest.Start(t)
	now := time.Now().UTC().Truncate(time.Second)
	at := func(ago time.Duration) string {
		return fmt.Sprintf("toDateTime64('%s', 9)", now.Add(-ago).Format("2006-01-02 15:04:05"))
	}
	gauge := func(env, svc, metric, inst string, ago time.Duration, v float64) {
		write(t, fmt.Sprintf(`INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, TimeUnix, Value)
SELECT map('deployment.environment.name','%s','service.instance.id','%s'), '%s', '%s', '', map(), %s, %g`, env, inst, svc, metric, at(ago), v))
	}
	// temporal (project): two endpoints, pod-a's newest scrape failed.
	gauge("dev", "temporal", "up", "pod-a:9090", 4*time.Minute, 1)
	gauge("dev", "temporal", "up", "pod-a:9090", time.Minute, 0)
	gauge("dev", "temporal", "scrape_samples_scraped", "pod-a:9090", time.Minute, 9001)
	gauge("dev", "temporal", "up", "pod-b:9090", time.Minute, 1)
	gauge("dev", "temporal", "scrape_samples_scraped", "pod-b:9090", time.Minute, 420)
	gauge("dev", "temporal", "scrape_samples_post_metric_relabeling", "pod-b:9090", time.Minute, 120)
	// prod's endpoint must stay invisible to dev.
	gauge("prod", "temporal", "up", "pod-p:9090", time.Minute, 1)
	// clickhouse (platform): no env on its rows.
	gauge("", "clickhouse", "up", "ch-0:9363", 2*time.Minute, 1)
	write(t, fmt.Sprintf(`INSERT INTO otel_logs (Timestamp, SeverityText, SeverityNumber, ServiceName, Body, ResourceAttributes, LogAttributes)
SELECT %s, 'INFO', 9, 'temporal', 'started', map('deployment.environment.name','dev'), map()`, at(3*time.Minute)))

	l := obsbackend.Local{Env: "dev", Runner: runner}
	n := obsbackend.Narrow{Range: obsbackend.Range{From: now.Add(-time.Hour), To: now.Add(time.Minute)}, Services: []string{"temporal"}}
	res, err := l.TelemetryArrival(context.Background(), obsbackend.TelemetryArrivalRequest{Narrow: n})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]obsbackend.TelemetrySignalArrival{}
	for _, s := range res.Signals {
		got[s.Signal] = s
	}
	if m := got["metrics"]; m.Rows != 6 || !m.LastSeen.Equal(now.Add(-time.Minute)) {
		t.Errorf("metrics arrival = %+v, want dev's 6 rows, newest a minute ago", m)
	}
	if lg := got["logs"]; lg.Rows != 1 {
		t.Errorf("logs arrival = %+v", lg)
	}
	if _, ok := got["traces"]; ok {
		t.Error("temporal sent no spans")
	}
	if _, ok := res.Unread["profiles"]; !ok {
		t.Errorf("a store without the profiles table must report the signal unread, not fail: %+v", res.Unread)
	}
	if len(res.Scrapes) != 2 || res.Scrapes[0].Instance != "pod-a:9090" || res.Scrapes[0].Up != 0 || res.Scrapes[0].Samples != 9001 ||
		res.Scrapes[1].Up != 1 || res.Scrapes[1].Kept != 120 {
		t.Errorf("scrapes = %+v", res.Scrapes)
	}

	n.Services = []string{"clickhouse"}
	res, err = l.TelemetryArrival(context.Background(), obsbackend.TelemetryArrivalRequest{Narrow: n, Platform: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scrapes) != 1 || res.Scrapes[0].Up != 1 {
		t.Errorf("a platform system is read without the env: %+v", res.Scrapes)
	}
}
