package observe

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var otelEnvVars = []string{
	"OTEL_SDK_DISABLED", "OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES",
	"OTEL_LOGS_EXPORTER", "OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER",
	"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_HEADERS",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_HEADERS",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_HEADERS",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "OTEL_EXPORTER_OTLP_LOGS_HEADERS",
}

// clearOTelEnv blanks every variable of the contract so a test starts from
// "nothing configured" regardless of the developer's shell. An empty value is
// treated as unset by both this package and the SDK.
func clearOTelEnv(t *testing.T) {
	t.Helper()
	for _, k := range otelEnvVars {
		t.Setenv(k, "")
	}
}

// snapshotGlobals restores the process-global providers and propagator Setup
// replaces, so tests do not leak SDK providers into each other.
func snapshotGlobals(t *testing.T) {
	t.Helper()
	tp, mp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		otel.SetTextMapPropagator(prop)
	})
}

type otlpRequest struct {
	Path        string
	ContentType string
	Header      http.Header
	Body        []byte
}

// otlpReceiver is a local in-process OTLP/HTTP collector.
type otlpReceiver struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []otlpRequest
}

func newOTLPReceiver(t *testing.T) *otlpReceiver {
	t.Helper()
	r := &otlpReceiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, otlpRequest{Path: req.URL.Path, ContentType: req.Header.Get("Content-Type"), Header: req.Header.Clone(), Body: body})
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *otlpReceiver) requests(path string) []otlpRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []otlpRequest
	for _, q := range r.reqs {
		if q.Path == path {
			out = append(out, q)
		}
	}
	return out
}

func emitSpan(ctx context.Context) {
	_, span := otel.Tracer("observe-test").Start(ctx, "op")
	span.End()
}

func flush(t *testing.T, shutdown func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestEnvEndpointIsTheDefaultSourceAndHeadersReachTheExporter(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	rcv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=secret-key,x-team=forge")

	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)

	got := rcv.requests("/v1/traces")
	if len(got) == 0 {
		t.Fatalf("no trace export reached the env endpoint; saw %+v", rcv.reqs)
	}
	if got[0].Header.Get("Authorization") != "secret-key" || got[0].Header.Get("X-Team") != "forge" {
		t.Errorf("headers from OTEL_EXPORTER_OTLP_HEADERS missing on the wire: %v", got[0].Header)
	}
	if got[0].ContentType != "application/x-protobuf" {
		t.Errorf("default protocol is not http/protobuf: Content-Type %q", got[0].ContentType)
	}
	if len(rcv.requests("/v1/metrics")) == 0 {
		t.Error("metrics were not exported to /v1/metrics")
	}
}

func TestPerSignalHeadersOverrideTheGeneralOnes(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	rcv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=general")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "authorization=traces-only")

	shutdown, _, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)

	if tr := rcv.requests("/v1/traces"); len(tr) == 0 || tr[0].Header.Get("Authorization") != "traces-only" {
		t.Errorf("trace headers = %+v, want traces-only", tr)
	}
	if m := rcv.requests("/v1/metrics"); len(m) == 0 || m[0].Header.Get("Authorization") != "general" {
		t.Errorf("metric headers = %+v, want general", m)
	}
}

func TestTypedEndpointOverridesEnvEndpoint(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	typed, fromEnv := newOTLPReceiver(t), newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", fromEnv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=still-from-env")

	shutdown, _, err := Setup(context.Background(), Config{OTLPEndpoint: typed.URL})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)

	tr := typed.requests("/v1/traces")
	if len(tr) == 0 {
		t.Fatal("typed endpoint received no traces")
	}
	if len(fromEnv.requests("/v1/traces")) != 0 {
		t.Error("env endpoint received traces although the typed endpoint is set")
	}
	if tr[0].Header.Get("Authorization") != "still-from-env" {
		t.Errorf("headers must keep coming from the env when only the endpoint is typed: %v", tr[0].Header)
	}
}

func TestBareHostPortTypedEndpointIsPlainHTTP(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	rcv := newOTLPReceiver(t)
	hostPort := strings.TrimPrefix(rcv.URL, "http://")

	shutdown, _, err := Setup(context.Background(), Config{OTLPEndpoint: hostPort})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)
	if len(rcv.requests("/v1/traces")) == 0 {
		t.Fatal("host:port endpoint did not reach the receiver over http")
	}
}

type traceSink struct {
	collectortrace.UnimplementedTraceServiceServer
	mu  sync.Mutex
	md  []metadata.MD
	req []*collectortrace.ExportTraceServiceRequest
}

func (s *traceSink) Export(ctx context.Context, r *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	s.md = append(s.md, md)
	s.req = append(s.req, r)
	s.mu.Unlock()
	return &collectortrace.ExportTraceServiceResponse{}, nil
}

func TestGRPCProtocolSelectionCarriesHeaders(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &traceSink{}
	srv := grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+lis.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=grpc-key")
	t.Setenv("OTEL_METRICS_EXPORTER", "none") // the sink only implements the trace service

	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.req) == 0 {
		t.Fatal("grpc trace export never arrived")
	}
	if v := sink.md[0].Get("authorization"); len(v) != 1 || v[0] != "grpc-key" {
		t.Errorf("grpc metadata authorization = %v, want [grpc-key]", v)
	}
}

func TestPerSignalProtocolOverridesGeneralProtocol(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	if p, err := protocolFor(signalTraces); err != nil || p != ProtocolHTTPProtobuf {
		t.Errorf("traces protocol = %q, %v; want http/protobuf", p, err)
	}
	if p, err := protocolFor(signalMetrics); err != nil || p != ProtocolGRPC {
		t.Errorf("metrics protocol = %q, %v; want grpc", p, err)
	}
}

func TestProtocolDefaultsToHTTPProtobufAndRejectsUnknown(t *testing.T) {
	clearOTelEnv(t)
	if p, err := protocolFor(signalTraces); err != nil || p != ProtocolHTTPProtobuf {
		t.Errorf("default protocol = %q, %v; want http/protobuf", p, err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
	snapshotGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	if _, _, err := Setup(context.Background(), Config{}); err == nil {
		t.Error("Setup accepted unsupported protocol http/json")
	}
}

func TestNoEndpointMeansNoExportButPropagationStaysOn(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	shutdown, handler, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	defer flush(t, shutdown)
	if handler == nil {
		t.Fatal("/metrics handler missing")
	}
	assertPropagatorInstalled(t)
}

func TestSDKDisabledStopsExportButKeepsPropagation(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	rcv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_SDK_DISABLED", "true")

	shutdown, _, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)
	if len(rcv.reqs) != 0 {
		t.Errorf("OTEL_SDK_DISABLED=true still exported: %+v", rcv.reqs)
	}
	assertPropagatorInstalled(t)
}

func assertPropagatorInstalled(t *testing.T) {
	t.Helper()
	have := map[string]bool{}
	for _, f := range otel.GetTextMapPropagator().Fields() {
		have[f] = true
	}
	if !have["traceparent"] || !have["baggage"] {
		t.Errorf("global propagator fields = %v, want traceparent and baggage", otel.GetTextMapPropagator().Fields())
	}
	// Round-trip proves it is functional, not just advertised.
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(trace.ContextWithSpanContext(context.Background(), sc), carrier)
	if carrier.Get("traceparent") == "" {
		t.Error("propagator injected no traceparent")
	}
}

func TestExportDisabledPerSignalByExporterNone(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	rcv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_TRACES_EXPORTER", "none")

	shutdown, _, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)
	if len(rcv.requests("/v1/traces")) != 0 {
		t.Error("OTEL_TRACES_EXPORTER=none still exported traces")
	}
	if len(rcv.requests("/v1/metrics")) == 0 {
		t.Error("metrics should still export when only traces are off")
	}
}

func TestGoRuntimeMetricsAreExportedWhenMetricsExportIsOn(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	rcv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)

	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	flush(t, shutdown)

	m := rcv.requests("/v1/metrics")
	if len(m) == 0 {
		t.Fatal("no metrics export")
	}
	var body strings.Builder
	for _, r := range m {
		body.Write(r.Body)
	}
	if !strings.Contains(body.String(), "go.goroutine.count") {
		t.Errorf("Go runtime metrics (go.goroutine.count) missing from export")
	}
}

func TestNoGoRuntimeMetricsWithoutMetricsExport(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	shutdown, handler, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer flush(t, shutdown)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(rec.Body.String(), "go_goroutine_count") {
		t.Error("runtime instrumentation was started with metrics export off")
	}
}

func TestOTelServiceNameWinsOverCompileTimeDefault(t *testing.T) {
	clearOTelEnv(t)
	for _, tc := range []struct{ env, cfg, want string }{
		{"from-env", "compiled-in", "from-env"},
		{"", "compiled-in", "compiled-in"},
		{"", "", "unknown"},
	} {
		t.Setenv("OTEL_SERVICE_NAME", tc.env)
		res, err := resourceFromConfig(context.Background(), Config{ServiceName: tc.cfg})
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := res.Set().Value("service.name"); v.AsString() != tc.want {
			t.Errorf("OTEL_SERVICE_NAME=%q cfg=%q: service.name = %q, want %q", tc.env, tc.cfg, v.AsString(), tc.want)
		}
	}
}
