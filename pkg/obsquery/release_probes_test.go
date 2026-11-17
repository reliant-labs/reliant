package obsquery

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The filters name the contract attribute, compare it as the string the
// exporters store a bool attribute as, and let a row without it through.
func TestNotProbeFiltersNameTheContractAttribute(t *testing.T) {
	if want := "SpanAttributes['forge.probe'] != 'true'"; SpanNotProbeSQL != want {
		t.Errorf("SpanNotProbeSQL = %s, want %s", SpanNotProbeSQL, want)
	}
	if want := "LogAttributes['forge.probe'] != 'true'"; LogNotProbeSQL != want {
		t.Errorf("LogNotProbeSQL = %s, want %s", LogNotProbeSQL, want)
	}
}

// Probe RED reads only the prober's metrics, scoped like every other
// statement: its counts by outcome from the request counter, its quantiles
// from the duration histogram over the ok and failed requests only.
func TestProbeREDReadsTheProbeMetricsOnly(t *testing.T) {
	sc := scope()
	sc.EnvRestricted, sc.EnvIDs = true, []string{"env-'; DROP"}
	sc.Services = []string{"web-'; DROP"}
	for name, compile := range map[string]func(Overview) (Stmt, error){
		"ProbeRED":       Overview.ProbeRED,
		"ProbeREDByPath": Overview.ProbeREDByPath,
	} {
		st, err := compile(Overview{Scope: sc})
		if err != nil {
			t.Fatal(name, err)
		}
		assertParameterized(t, st, "DROP")
		for _, want := range []string{
			"FROM t_org_a.otel_metrics_sum", "FROM t_org_a.otel_metrics_histogram", "MetricName = {", "ResourceAttributes['reliant.source'] = {",
			"sumIf(Value, Attributes['probe.outcome'] = 'ok')) AS ok", "sumIf(Value, Attributes['probe.outcome'] = 'failed')) AS failed",
			"sumIf(Value, Attributes['probe.outcome'] = 'throttled')) AS throttled", "sumIf(Value, Attributes['probe.outcome'] = 'degraded')) AS degraded",
			"Attributes['probe.outcome'] IN ('ok', 'failed')", "quantilesExactWeighted(0.5, 0.95, 0.99)", histogramMidSQL,
			"ARRAY JOIN buckets AS bucket", "LEFT JOIN (", "ServiceName IN (", "AS source",
		} {
			if !strings.Contains(st.SQL, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, st.SQL)
			}
		}
		byPath := strings.Contains(st.SQL, "Attributes['probe.path'] AS path")
		if byPath != (name == "ProbeREDByPath") || byPath != strings.Contains(st.SQL, "AND h.path = c.path") {
			t.Errorf("%s: split by path = %v:\n%s", name, byPath, st.SQL)
		}
		var counter, histogram, source bool
		for _, v := range st.Params {
			counter = counter || v == release.ProbeMetricRequests
			histogram = histogram || v == release.ProbeMetricDuration
			source = source || v == release.ProbeSource
		}
		if !counter || !histogram || !source {
			t.Errorf("%s params %v do not name %s, %s and source %s", name, st.Params, release.ProbeMetricRequests, release.ProbeMetricDuration, release.ProbeSource)
		}
		if _, err := compile(Overview{Scope: scope(), GroupBy: []string{"service.version"}}); err == nil {
			t.Errorf("%s accepted a group_by", name)
		}
	}
}

func TestProbeFailuresListsFailedRequestsOnly(t *testing.T) {
	sc := scope()
	sc.Services = []string{"web-'; DROP"}
	st, err := Overview{Scope: sc}.ProbeFailures()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, "DROP")
	for _, want := range []string{
		"FROM t_org_a.otel_metrics_sum", "Attributes['probe.outcome'] = 'failed'", "Attributes['probe.path'] AS path",
		"Attributes['status_class'] AS status_class", "Attributes['error.type'] AS error_type", "min(TimeUnix) AS first", "max(TimeUnix) AS last",
		"GROUP BY ServiceName, path, status_class, error_type", "LIMIT 5 BY ServiceName", "ServiceName IN (",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("ProbeFailures lacks %q:\n%s", want, st.SQL)
		}
	}
	if strings.Contains(st.SQL, "otel_metrics_histogram") {
		t.Errorf("ProbeFailures reads the histogram:\n%s", st.SQL)
	}
	if _, err := (Overview{Scope: scope(), GroupBy: []string{"service.version"}}).ProbeFailures(); err == nil {
		t.Error("ProbeFailures accepted a group_by")
	}
}

// Every statement over the real population leaves a probe's spans or records
// out; trace and log search still finds them, since a probe's trace is how a
// probe failure is debugged.
func TestRealTrafficStatementsExcludeProbes(t *testing.T) {
	ov := Overview{Scope: scope()}
	fp := strings.Repeat("ab", 8)
	stmts := map[string]struct {
		compile func() (Stmt, error)
		filter  string
	}{
		"RED":                    {ov.RED, SpanNotProbeSQL},
		"REDByEndpoint":          {ov.REDByEndpoint, SpanNotProbeSQL},
		"Trend":                  {func() (Stmt, error) { return ov.Trend(60) }, SpanNotProbeSQL},
		"Logs":                   {ov.Logs, LogNotProbeSQL},
		"ServerIssues":           {ServerIssues{Scope: scope(), Limit: 10}.Compile, LogNotProbeSQL},
		"ServerIssues by source": {ServerIssues{Scope: scope(), Limit: 10, Source: "billing"}.Compile, LogNotProbeSQL},
		"ServerIssueEvents":      {ServerIssueEvents{Scope: scope(), Fingerprint: fp, Limit: 10}.Compile, LogNotProbeSQL},
		"ServerIssueVersions":    {ServerIssueVersions{Scope: scope(), Fingerprint: fp}.Compile, LogNotProbeSQL},
		"ServerIssueCoOccurring": {ServerIssueCoOccurring{Scope: scope(), Fingerprint: fp}.Compile, LogNotProbeSQL},
		"ServerIssueIncidents":   {ServerIssueIncidents{Scope: scope()}.Compile, LogNotProbeSQL},
	}
	for name, s := range stmts {
		st, err := s.compile()
		if err != nil {
			t.Fatal(name, err)
		}
		if !strings.Contains(st.SQL, s.filter) {
			t.Errorf("%s does not exclude probes (%s):\n%s", name, s.filter, st.SQL)
		}
	}
	for name, compile := range map[string]func() (Stmt, error){
		"TraceQuery": TraceQuery{Scope: scope(), Limit: 10}.Compile,
		"LogQuery":   LogQuery{Scope: scope(), Limit: 10}.Compile,
	} {
		st, err := compile()
		if err != nil {
			t.Fatal(name, err)
		}
		if strings.Contains(st.SQL, release.ProbeAttr) {
			t.Errorf("search %s hides probes:\n%s", name, st.SQL)
		}
	}
}

func TestProbeSourceIsOperatorData(t *testing.T) {
	if !OperatorSource(release.ProbeSource) {
		t.Errorf("OperatorSource(%q) = false: the prober's metrics would be billed", release.ProbeSource)
	}
}

func TestScanProbeRED(t *testing.T) {
	got, err := ScanProbeRED(&fakeRows{rows: [][]any{
		{"web", uint64(58), uint64(2), uint64(1), uint64(3), []float64{40, 44, 90}, "probe"},
		{"api", uint64(0), uint64(0), uint64(4), uint64(0), []float64{}, "probe"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ProbeFigures{
		"web": {Samples: 60, Failures: 2, Throttled: 1, Degraded: 3, P50Ms: 40, P95Ms: 44},
		"api": {Throttled: 4},
	}
	if len(got) != len(want) || got["web"] != want["web"] || got["api"] != want["api"] {
		t.Errorf("ScanProbeRED = %+v, want %+v", got, want)
	}
	byPath, err := ScanProbeREDByPath(&fakeRows{rows: [][]any{
		{"web", "/", uint64(10), uint64(1), uint64(0), uint64(0), []float64{5, 7, 9}, "probe"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if f := byPath[ProbePath{Target: "web", Path: "/"}]; len(byPath) != 1 || f != (ProbeFigures{Samples: 11, Failures: 1, P50Ms: 5, P95Ms: 7}) {
		t.Errorf("ScanProbeREDByPath = %+v", byPath)
	}
}

func TestScanProbeFailures(t *testing.T) {
	first, last := t0.Add(time.Minute), t0.Add(9*time.Minute)
	got, err := ScanProbeFailures(&fakeRows{rows: [][]any{
		{"web", "/", "5xx", "", uint64(3), first, last},
		{"web", "/about", "", "timeout", uint64(1), last, last},
	}})
	if err != nil {
		t.Fatal(err)
	}
	web := got["web"]
	if len(got) != 1 || len(web) != 2 ||
		web[0] != (ProbeFailure{Path: "/", StatusClass: "5xx", First: first, Last: last, Count: 3}) ||
		web[1] != (ProbeFailure{Path: "/about", ErrorType: "timeout", First: last, Last: last, Count: 1}) {
		t.Errorf("ScanProbeFailures = %+v", got)
	}
}
