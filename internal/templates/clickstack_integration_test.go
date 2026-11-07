//go:build integration

package templates_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	pproftrans "github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/pprof"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/pkg/profilequery"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestGeneratedProjectDeliversTelemetryToClickStack is the gate for "ClickStack
// on by default, zero manual steps".
//
// It scaffolds a FRESH project, renders its dev env through forge's own KCL
// evaluation (that is where the published ports are decided), brings up the
// GENERATED docker-compose.yml's clickstack service exactly as `forge env up`
// would, then plays a host process: it takes the endpoint from the same
// render `forge env up` uses (the typed observability declaration), sends one known
// trace, three metric families and a log over OTLP/HTTP with no credentials,
// and writes one JSON line to .forge/logs/dev/<service>.log. Every assertion is
// on an EXACT service name, trace id, metric name and value read back from
// ClickHouse. There is no registration step and no key anywhere in this test:
// that is the property under test.
//
// Run: RUN_CLICKSTACK_CONTRACT=1 go test -tags integration -run TestGeneratedProject
// -count=1 -timeout 15m ./internal/templates/
func TestGeneratedProjectDeliversTelemetryToClickStack(t *testing.T) {
	if testing.Short() || os.Getenv("RUN_CLICKSTACK_CONTRACT") != "1" {
		t.Skip("pulls ~2 GB of images and boots a database; set RUN_CLICKSTACK_CONTRACT=1 outside -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable: " + err.Error())
	}

	root := scaffoldForRender(t, "shop", []string{"orders"}, "web")
	w := devWorkloadJSON(t, root)
	stack, ok := w[clickstack.Service]
	if !ok {
		t.Fatalf("a fresh dev env does not run %s: %v", clickstack.Service, w)
	}
	port := func(name string) string {
		m := regexp.MustCompile(`"` + name + `":"(\d+)"`).FindStringSubmatch(stack)
		if m == nil {
			t.Fatalf("the rendered clickstack workload carries no %s:\n%s", name, stack)
		}
		return m[1]
	}
	httpPort, grpcPort, uiPort := port(clickstack.EnvOTLPHTTPPort), port(clickstack.EnvOTLPGRPCPort), port(clickstack.EnvUIPort)

	// Continuous profiles go to their own collector; a fresh env must run it too.
	profilesWorkload, ok := w[clickstack.ProfilesService]
	if !ok {
		t.Fatalf("a fresh dev env does not run %s: %v", clickstack.ProfilesService, w)
	}
	profilesPort := regexp.MustCompile(`"` + clickstack.EnvProfilesOTLPPort + `":"(\d+)"`).FindStringSubmatch(profilesWorkload)
	if profilesPort == nil {
		t.Fatalf("the rendered %s workload carries no %s:\n%s", clickstack.ProfilesService, clickstack.EnvProfilesOTLPPort, profilesWorkload)
	}

	// `forge env up` creates the log dir before anything writes to it.
	logDir := filepath.Join(root, ".forge", "logs", "dev")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	project := fmt.Sprintf("forge-cs-gate-%d", time.Now().UnixNano())
	compose := func(args ...string) ([]byte, error) {
		cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", filepath.Join(root, "docker-compose.yml")}, args...)...)
		cmd.Dir = root
		// Exactly what OnCompose.env hands the compose process in `forge env up`.
		cmd.Env = append(os.Environ(),
			clickstack.EnvOTLPHTTPPort+"="+httpPort,
			clickstack.EnvOTLPGRPCPort+"="+grpcPort,
			clickstack.EnvUIPort+"="+uiPort,
			clickstack.EnvProfilesOTLPPort+"="+profilesPort[1],
		)
		return cmd.CombinedOutput()
	}
	t.Cleanup(func() { _, _ = compose("down", "--volumes", "--remove-orphans") })

	start := time.Now()
	if out, err := compose("up", "-d", "--wait", "--wait-timeout", "300", clickstack.Service); err != nil {
		logs, _ := compose("logs", "--tail", "80", clickstack.Service)
		t.Fatalf("docker compose up --wait clickstack: %v\n%s\n--- logs ---\n%s", err, out, logs)
	}
	t.Logf("clickstack healthy %.0fs after `up --wait`", time.Since(start).Seconds())
	// The schema job runs to completion first, then the profiles collector starts.
	if out, err := compose("up", "-d", "--wait", "--wait-timeout", "180", clickstack.ProfilesService); err != nil {
		logs, _ := compose("logs", "--tail", "60", clickstack.ProfilesService, "profiles-schema")
		t.Fatalf("docker compose up --wait %s: %v\n%s\n--- logs ---\n%s", clickstack.ProfilesService, err, out, logs)
	}

	query := func(sql string) string {
		t.Helper()
		out, err := compose("exec", "-T", clickstack.Service, "sh", "-c",
			`clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --format TSV --query "$1"`, "sh", sql)
		if err != nil {
			t.Fatalf("clickhouse query %q: %v\n%s", sql, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// waitRows polls until the query returns want exactly: ingest is async
	// (the collector batches for 5s), and a bare "row exists" proves nothing.
	waitRows := func(what, sql, want string) {
		t.Helper()
		var got string
		for deadline := time.Now().Add(120 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if got = query(sql); got == want {
				return
			}
		}
		t.Fatalf("%s: %s\n got  %q\n want %q", what, sql, got, want)
	}

	// ClickHouse must be new enough for contrib's profiles exporter (>= 26.2),
	// so the profiles pipeline can be added without swapping the database.
	var major, minor int
	if _, err := fmt.Sscanf(query("SELECT version()"), "%d.%d", &major, &minor); err != nil {
		t.Fatalf("parse ClickHouse version: %v", err)
	}
	if major < 26 || (major == 26 && minor < 2) {
		t.Errorf("ClickHouse %d.%d is older than the 26.2 profiles needs", major, minor)
	}

	// ── What a host process is handed ────────────────────────────────────
	// The same render `forge env up` hands every host process: the API's
	// OTEL_EXPORTER_OTLP_* as the typed observability declaration derives
	// them. Reading it from the render, not rebuilding it here, is what makes
	// this gate prove the env a user gets delivers data.
	env := map[string]string{}
	for _, m := range regexp.MustCompile(`"name":"(OTEL_(?:EXPORTER_OTLP|PROFILES)_[A-Z_]+)","value":"([^"]*)"`).FindAllStringSubmatch(w["api"], -1) {
		env[m[1]] = m[2]
	}
	endpoint := env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" || endpoint != "http://127.0.0.1:"+httpPort {
		t.Fatalf("host OTLP env = %v (rendered api: %s)", env, w["api"])
	}
	// Profiles are on by default beside the local ClickStack: the declaration
	// aims host processes at the profiles collector's published port.
	profilesEndpoint := env["OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT"]
	if profilesEndpoint != "http://127.0.0.1:"+profilesPort[1]+"/v1development/profiles" || env["OTEL_PROFILES_EXPORTER"] != "" {
		t.Fatalf("host profiles env = %v: want the profiles collector on :%s, profiling on (rendered api: %s)", env, profilesPort[1], w["api"])
	}

	run := fmt.Sprintf("%d", time.Now().UnixNano())
	svcOTLP := "gate-otlp-" + run // traces, metrics and the OTLP log
	svcFile := "gate-file-" + run // the file-tailed log
	traceID := bytes16(0xA1)
	spanID := []byte{0xB1, 2, 3, 4, 5, 6, 7, 8}
	fileTrace := bytes16(0xC3)
	fileSpan := []byte{0xD4, 2, 3, 4, 5, 6, 7, 8}

	// ── Traces, metrics, an OTLP log: no key, no header ──────────────────
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		kv("service.name", svcOTLP),
		kv("deployment.environment.name", "dev"),
	}}
	now := uint64(time.Now().UnixNano())
	post(t, endpoint+"/v1/traces", &collectortrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: res, ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: traceID, SpanId: spanID, Name: "gate-span", Kind: tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: now, EndTimeUnixNano: now + uint64(time.Millisecond),
		}}}},
	}}})
	post(t, endpoint+"/v1/metrics", &collectormetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: res, ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "gate.gauge", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
				{TimeUnixNano: now, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 7}}}}}},
			{Name: "gate.sum", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: now, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 3}}}}}},
			{Name: "gate.histogram", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricspb.HistogramDataPoint{{TimeUnixNano: now, Count: 1, Sum: proto.Float64(2),
					ExplicitBounds: []float64{5}, BucketCounts: []uint64{1, 0}}}}}},
		}}},
	}}})
	post(t, endpoint+"/v1/logs", &collectorlogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: res, ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
			TimeUnixNano: now, SeverityText: "ERROR", TraceId: traceID, SpanId: spanID,
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "gate-otlp-log"}},
		}}}},
	}}})

	// ── The file log path: exactly what `forge env up` leaves on disk ────
	line, _ := json.Marshal(map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339Nano), "level": "WARN", "msg": "gate-file-log",
		"trace_id": hex.EncodeToString(fileTrace), "span_id": hex.EncodeToString(fileSpan), "procedure": "/shop.v1.Orders/List",
	})
	if err := os.WriteFile(filepath.Join(logDir, svcFile+".log"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// ── Exact assertions ─────────────────────────────────────────────────
	waitRows("trace by exact service name and trace id",
		fmt.Sprintf(`SELECT count() FROM default.otel_traces WHERE ServiceName='%s' AND TraceId='%s' AND SpanId='%s' AND SpanName='gate-span'`,
			svcOTLP, hex.EncodeToString(traceID), hex.EncodeToString(spanID)), "1")
	waitRows("gauge metric: exact name and value",
		fmt.Sprintf(`SELECT MetricName, Value FROM default.otel_metrics_gauge WHERE ServiceName='%s'`, svcOTLP), "gate.gauge\t7")
	waitRows("sum metric: exact name and value",
		fmt.Sprintf(`SELECT MetricName, Value FROM default.otel_metrics_sum WHERE ServiceName='%s'`, svcOTLP), "gate.sum\t3")
	waitRows("histogram metric: exact name, count and sum",
		fmt.Sprintf(`SELECT MetricName, Count, Sum FROM default.otel_metrics_histogram WHERE ServiceName='%s'`, svcOTLP), "gate.histogram\t1\t2")
	waitRows("OTLP log: exact service, body and trace link",
		fmt.Sprintf(`SELECT Body, TraceId, SpanId FROM default.otel_logs WHERE ServiceName='%s'`, svcOTLP),
		"gate-otlp-log\t"+hex.EncodeToString(traceID)+"\t"+hex.EncodeToString(spanID))
	waitRows("file log: service name is the FILE name; body, severity, trace link and env come from the JSON line",
		fmt.Sprintf(`SELECT ServiceName, Body, SeverityText, TraceId, SpanId, ResourceAttributes['deployment.environment.name'] FROM default.otel_logs WHERE ServiceName='%s'`, svcFile),
		strings.Join([]string{svcFile, "gate-file-log", "warn", hex.EncodeToString(fileTrace), hex.EncodeToString(fileSpan), "dev"}, "\t"))

	// ── Continuous profiles: pushed over OTLP to the profiles collector ──
	//
	// What pkg/observe's profiler sends, built here with the same translator, with
	// ONE sample linked to a span and BOTH Go CPU sample types left in so the
	// materialized view's `samples` filter is exercised too.
	svcProf := "gate-prof-" + run
	profTrace, profSpan := bytes16(0xE5), []byte{0xF6, 2, 3, 4, 5, 6, 7, 8}
	fnMain := &profile.Function{ID: 1, Name: "gate.main", Filename: "gate.go"}
	fnWork := &profile.Function{ID: 2, Name: "gate.work", Filename: "gate.go"}
	fnHash := &profile.Function{ID: 3, Name: "gate.hash", Filename: "gate.go"}
	locMain := &profile.Location{ID: 1, Line: []profile.Line{{Function: fnMain, Line: 1}}}
	locWork := &profile.Location{ID: 2, Line: []profile.Line{{Function: fnWork, Line: 2}}}
	locHash := &profile.Location{ID: 3, Line: []profile.Line{{Function: fnHash, Line: 3}}}
	pp := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		PeriodType: &profile.ValueType{Type: "cpu", Unit: "nanoseconds"}, Period: 10_000_000,
		TimeNanos: time.Now().UnixNano(), DurationNanos: int64(10 * time.Second),
		Function: []*profile.Function{fnMain, fnWork, fnHash}, Location: []*profile.Location{locMain, locWork, locHash},
		Sample: []*profile.Sample{
			{Location: []*profile.Location{locHash, locWork, locMain}, Value: []int64{4, 40_000_000}},
			{Location: []*profile.Location{locWork, locMain}, Value: []int64{1, 10_000_000}},
		},
	}
	pdProf, err := pproftrans.ConvertPprofToProfiles(pp)
	if err != nil {
		t.Fatal(err)
	}
	pdProf.ResourceProfiles().At(0).Resource().Attributes().PutStr("service.name", svcProf)
	pdProf.ResourceProfiles().At(0).Resource().Attributes().PutStr("deployment.environment.name", "dev")
	links := pdProf.Dictionary().LinkTable()
	if links.Len() == 0 {
		links.AppendEmpty() // index 0 = no link
	}
	lk := links.AppendEmpty()
	linkIdx := int32(links.Len() - 1)
	var tid pcommon.TraceID
	var sid pcommon.SpanID
	copy(tid[:], profTrace)
	copy(sid[:], profSpan)
	lk.SetTraceID(tid)
	lk.SetSpanID(sid)
	for i := 0; i < pdProf.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().Len(); i++ {
		prof := pdProf.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(i)
		prof.SetProfileID(pprofile.ProfileID{byte(i + 1), 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
		prof.Samples().At(0).SetLinkIndex(linkIdx) // the heavy stack ran inside the span
	}
	body, err := pprofileotlp.NewExportRequestFromProfiles(*pdProf).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	postBytes(t, profilesEndpoint, body)

	waitRows("raw profile row: exact service, type, trace link and a profile id",
		fmt.Sprintf(`SELECT SampleType, TraceId, SpanId, ProfileId != '', arrayStringConcat(arrayReverse(FunctionNames), ';') FROM default.otel_profiles WHERE ServiceName='%s' AND TraceId='%s' AND SampleType='cpu'`,
			svcProf, hex.EncodeToString(profTrace)),
		strings.Join([]string{"cpu", hex.EncodeToString(profTrace), hex.EncodeToString(profSpan), "1", "gate.main;gate.work;gate.hash"}, "\t"))
	waitRows("the samples/count twin reaches the raw tier but never the aggregate",
		fmt.Sprintf(`SELECT (SELECT count() FROM default.otel_profiles WHERE ServiceName='%[1]s' AND SampleType='samples'), (SELECT count() FROM default.profile_samples_1m WHERE ServiceName='%[1]s' AND SampleType='samples')`, svcProf),
		"2\t0")

	// The flamegraph query returns the stacks, root to leaf, with exact values.
	flame := func(q profilequery.Query) string {
		t.Helper()
		sql, params := q.SQL()
		args := []string{"exec", "-T", clickstack.Service, "sh", "-c",
			`q="$1"; shift; exec clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --query "$q" "$@"`, "sh", sql}
		for k, v := range params {
			args = append(args, "--param_"+k+"="+v)
		}
		out, err := compose(args...)
		if err != nil {
			t.Fatalf("flamegraph query: %v\n%s", err, out)
		}
		p, err := profilequery.Parse(bytes.NewReader(out), q)
		if err != nil {
			t.Fatalf("parse flamegraph rows: %v\n%s", err, out)
		}
		var folded bytes.Buffer
		_ = p.Folded(&folded)
		return folded.String()
	}
	wantFolded := "gate.main;gate.work;gate.hash 40000000\ngate.main;gate.work 10000000\n"
	rangeQ := profilequery.Query{Service: svcProf, SampleType: "cpu", Env: "dev", From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}
	var gotFolded string
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if gotFolded = flame(rangeQ); gotFolded == wantFolded {
			break
		}
	}
	if gotFolded != wantFolded {
		t.Errorf("range flamegraph (aggregate tier):\n got %q\nwant %q", gotFolded, wantFolded)
	}
	// Only the span-linked sample belongs to the trace.
	if got := flame(profilequery.Query{Service: svcProf, SampleType: "cpu", TraceID: hex.EncodeToString(profTrace)}); got != "gate.main;gate.work;gate.hash 40000000\n" {
		t.Errorf("trace flamegraph (raw tier) = %q", got)
	}
	if got := flame(profilequery.Query{Service: svcProf, SampleType: "cpu", Env: "staging", From: rangeQ.From, To: rangeQ.To}); got != "" {
		t.Errorf("an env filter that matches nothing returned %q", got)
	}
	// The retention the design promises: 24h raw, 30d aggregate (ClickHouse prints
	// INTERVAL n UNIT as toIntervalUnit(n)).
	for table, want := range map[string]string{"otel_profiles": "toIntervalHour(24)", "profile_stacks": "toIntervalDay(30)", "profile_samples_1m": "toIntervalDay(30)"} {
		if ddl := query("SHOW CREATE TABLE default." + table); !strings.Contains(ddl, want) {
			t.Errorf("%s has no %q TTL:\n%s", table, want, ddl)
		}
	}

	// ── Pre-created sources and the dashboard provisioner ────────────────
	apiGet := func(path string) string {
		out, err := compose("exec", "-T", clickstack.Service, "curl", "-fsS", "http://localhost:8000"+path)
		if err != nil {
			t.Fatalf("GET %s inside the container: %v\n%s", path, err, out)
		}
		return string(out)
	}
	sources := apiGet("/sources")
	for _, name := range []string{`"name":"Logs"`, `"name":"Traces"`, `"name":"Metrics"`} {
		if !strings.Contains(sources, name) {
			t.Errorf("HyperDX has no pre-created source %s:\n%s", name, sources)
		}
	}
	dash := `{"name":"forge-gate-` + run + `","tiles":[],"tags":["forge"]}`
	if err := os.WriteFile(filepath.Join(root, clickstack.DashboardsDir, "gate.json"), []byte(dash), 0o644); err != nil {
		t.Fatal(err)
	}
	var dashboards string
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		if dashboards = apiGet("/dashboards"); strings.Contains(dashboards, `"name":"forge-gate-`+run+`"`) {
			break
		}
	}
	if !strings.Contains(dashboards, `"name":"forge-gate-`+run+`"`) || !strings.Contains(dashboards, `"provisioned":true`) {
		t.Errorf("a dashboard dropped into %s was not provisioned:\n%s", clickstack.DashboardsDir, dashboards)
	}

	// ── What #603 got wrong ──────────────────────────────────────────────
	// ClickHouse is not published, and its passwordless `default` user is gone.
	if out, err := compose("port", clickstack.Service, "8123"); err == nil && strings.TrimSpace(string(out)) != "" && !strings.HasPrefix(strings.TrimSpace(string(out)), ":0") {
		t.Errorf("ClickHouse 8123 is published to the host: %s", out)
	}
	// clickhouse-client inside the container reads CLICKHOUSE_USER/PASSWORD from
	// the environment, so the credential-free probe must be an explicit request
	// that names no user: the passwordless `default` user must be gone.
	if out, err := compose("exec", "-T", clickstack.Service, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"http://127.0.0.1:8123/?query=SELECT%201"); err != nil || strings.TrimSpace(string(out)) != "401" {
		t.Errorf("an anonymous ClickHouse request returned %q (err %v), want 401", out, err)
	}
	if out, err := compose("exec", "-T", clickstack.Service, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"http://127.0.0.1:8123/?user=default&query=SELECT%201"); err != nil || strings.TrimSpace(string(out)) != "401" {
		t.Errorf("ClickHouse's passwordless default user still works: %q (err %v)", out, err)
	}
	// Every published port is on loopback.
	ps, err := compose("ps", "--format", "json", clickstack.Service)
	if err != nil {
		t.Fatalf("compose ps: %v\n%s", err, ps)
	}
	published := 0
	for _, l := range bytes.Split(bytes.TrimSpace(ps), []byte("\n")) {
		var c struct {
			Publishers []struct {
				URL           string
				PublishedPort int
			}
		}
		if err := json.Unmarshal(l, &c); err != nil {
			t.Fatalf("parse compose ps: %v\n%s", err, l)
		}
		for _, p := range c.Publishers {
			if p.PublishedPort == 0 {
				continue
			}
			published++
			if p.URL != "127.0.0.1" {
				t.Errorf("port %d is published on %q, want 127.0.0.1", p.PublishedPort, p.URL)
			}
		}
	}
	if published < 3 {
		t.Errorf("expected the UI and both OTLP ports to be published, saw %d", published)
	}

	// Footprint, for the PR: the whole stack is this one container.
	if out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}",
		strings.TrimSpace(containerID(t, compose))).CombinedOutput(); err == nil {
		t.Logf("clickstack resident memory (docker stats): %s", strings.TrimSpace(string(out)))
	}
}

func containerID(t *testing.T, compose func(...string) ([]byte, error)) string {
	t.Helper()
	out, err := compose("ps", "-q", clickstack.Service)
	if err != nil {
		t.Fatalf("compose ps -q: %v\n%s", err, out)
	}
	return string(out)
}

func bytes16(first byte) []byte {
	b := make([]byte, 16)
	b[0] = first
	for i := 1; i < 16; i++ {
		b[i] = byte(i)
	}
	return b
}

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// post sends one OTLP/HTTP protobuf request with NO credentials and requires
// the collector to accept it: the property #603 lacked.
func post(t *testing.T, url string, msg proto.Message) {
	t.Helper()
	body, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var last error
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-protobuf")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			last = err
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		last = fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Fatalf("POST %s needs credentials (%v): host processes cannot send any", url, last)
		}
	}
	t.Fatalf("POST %s never accepted: %v", url, last)
}

// postBytes sends one pre-marshalled OTLP/HTTP request with no credentials and
// requires the collector to accept it.
func postBytes(t *testing.T, url string, body []byte) {
	t.Helper()
	var last error
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-protobuf")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			last = err
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		last = fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	t.Fatalf("POST %s never accepted: %v", url, last)
}
