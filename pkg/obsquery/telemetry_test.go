package obsquery

import (
	"strings"
	"testing"
	"time"
)

func telemetryScope(services ...string) Scope {
	end := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return Scope{Database: LocalDatabase, Local: true, Services: services, Start: end.Add(-time.Hour), End: end,
		Filters: []AttrFilter{{Key: "deployment.environment.name", Op: "eq", Value: "dev"}}}
}

func TestTelemetryArrivalReadsEachSignalsTables(t *testing.T) {
	for sig, tables := range map[string][]string{
		ArrivalLogs:     {"default.otel_logs"},
		ArrivalTraces:   {"default.otel_traces"},
		ArrivalProfiles: {"default.otel_profiles"},
		ArrivalMetrics:  {"default.otel_metrics_gauge", "default.otel_metrics_sum", "default.otel_metrics_histogram", "default.otel_metrics_exponential_histogram", "default.otel_metrics_summary"},
	} {
		st, err := TelemetryArrival{Scope: telemetryScope("temporal", "nats"), Signal: sig}.Compile()
		if err != nil {
			t.Fatalf("%s: %v", sig, err)
		}
		for _, table := range tables {
			if !strings.Contains(st.SQL, "FROM "+table+"\n") {
				t.Errorf("%s does not read %s:\n%s", sig, table, st.SQL)
			}
		}
		if !strings.Contains(st.SQL, "ServiceName IN (") || !strings.Contains(st.SQL, "GROUP BY service") {
			t.Errorf("%s is not one row per named service:\n%s", sig, st.SQL)
		}
		var services, env int
		for _, v := range st.Params {
			switch v {
			case "temporal", "nats":
				services++
			case "dev":
				env++
			}
		}
		if services != 2*len(tables) || env != len(tables) {
			t.Errorf("%s: every table must be narrowed to the services and the env: %v", sig, st.Params)
		}
	}
}

func TestTelemetryArrivalRefusals(t *testing.T) {
	if _, err := (TelemetryArrival{Scope: telemetryScope(), Signal: ArrivalLogs}).Compile(); err == nil {
		t.Error("a read of no service is refused")
	}
	if _, err := (TelemetryArrival{Scope: telemetryScope("a"), Signal: "checks"}).Compile(); err == nil {
		t.Error("an unknown signal is refused")
	}
	sc := telemetryScope("a")
	sc.Filters = append(sc.Filters, AttrFilter{Key: "metric.attr.x", Op: "eq", Value: "y"})
	if _, err := (TelemetryArrival{Scope: sc, Signal: ArrivalProfiles}).Compile(); err == nil {
		t.Error("a profiles read takes only resource filters")
	}
	sc = telemetryScope("a")
	sc.Database = LocalDatabase
	sc.Local = false
	if _, err := (TelemetryScrapes{Scope: sc}).Compile(); err == nil {
		t.Error("the local database on a non-local scope is refused")
	}
}

func TestTelemetryScrapesIsTheNewestResultPerEndpoint(t *testing.T) {
	st, err := TelemetryScrapes{Scope: telemetryScope("temporal")}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FROM default.otel_metrics_gauge", "argMaxIf(Value, TimeUnix, MetricName = ", "GROUP BY service, instance", "LIMIT 1000"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("missing %q:\n%s", want, st.SQL)
		}
	}
	for _, metric := range []string{"up", "scrape_samples_scraped", "scrape_samples_post_metric_relabeling"} {
		found := false
		for _, v := range st.Params {
			found = found || v == metric
		}
		if !found {
			t.Errorf("the statement does not read %s: %v", metric, st.Params)
		}
	}
}
