package obsquery

import (
	"strings"
	"testing"
)

// Ingress RED reads only the platform's ingress metrics, scoped like every
// other statement, and has RED's columns: its counts from the request
// counter (which counts what the histogram never times), its quantiles from
// the duration histogram.
func TestIngressREDReadsTheIngressMetricsOnly(t *testing.T) {
	sc := scope()
	sc.EnvRestricted, sc.EnvIDs = true, []string{"env-'; DROP"}
	sc.Services = []string{"svc-'; DROP"}
	st, err := Overview{Scope: sc}.IngressRED()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, "DROP")
	for _, want := range []string{
		"FROM t_org_a.otel_metrics_sum", "FROM t_org_a.otel_metrics_histogram", "MetricName = {", "ResourceAttributes['reliant.source'] = {",
		"toUInt64(sum(Value)) AS requests", "sumIf(Value, Attributes['status_class'] = '5xx')", "quantilesExactWeighted(0.5, 0.95, 0.99)", "AS source",
		"ARRAY JOIN buckets AS bucket", "LEFT JOIN (", "ServiceName IN (",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("IngressRED lacks %q:\n%s", want, st.SQL)
		}
	}
	var counter, histogram, source bool
	for _, v := range st.Params {
		counter = counter || v == IngressRequestsMetric
		histogram = histogram || v == IngressDurationMetric
		source = source || v == IngressSource
	}
	if !counter || !histogram || !source {
		t.Errorf("params %v do not name %s, %s and source %s", st.Params, IngressRequestsMetric, IngressDurationMetric, IngressSource)
	}
	if _, err := (Overview{Scope: scope(), GroupBy: []string{"service.version"}}).IngressRED(); err == nil {
		t.Error("IngressRED accepted a group_by")
	}
}

func TestBrowserREDCountsWhatThePageReports(t *testing.T) {
	st, err := Overview{Scope: scope()}.BrowserRED()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	for _, want := range []string{
		"FROM t_org_a.otel_traces", "countIf(" + browserViewExpr + ") AS views", "countIf(" + browserErrorExpr + ") AS js_errors",
		"countIf(" + browserRequestExpr + " AND " + browserFailedExpr + ") AS failed", "quantilesExactIf(0.5, 0.95, 0.99)",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("BrowserRED lacks %q:\n%s", want, st.SQL)
		}
	}
	if _, err := (Overview{Scope: scope(), GroupBy: []string{"service.version"}}).BrowserRED(); err == nil {
		t.Error("BrowserRED accepted a group_by")
	}
}
