//go:build e2e

package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2ETraceDepth is the acceptance gate for the tracing audit: a request
// through a scaffolded app is ONE trace with real depth, and every log line it
// writes can be joined to that trace.
//
// The audit (control-plane and reliant in prod) found the opposite: most
// traces were a single server span, because nothing between the RPC edge and
// the database opened a span — the business packages were undecorated, the
// pool was untraced, outbound HTTP went through a bare client — and most log
// lines carried no trace_id. Each of those was fixed in its own layer (F1
// dbtrace, F2 decorate every contract, F3 the traced HTTP client, F7 log
// events, F8 the lint), and each layer's unit tests passed while the PATH
// could still fall apart at a seam between two of them. So this walks the
// path a user walks — project new → entity + custom rpc → a contract package
// that reads the DB and calls an adapter → generate → build → boot — and
// judges the spans the server actually exports and the lines it actually
// logs.
//
// Two calls:
//
//   - CreateWidget, a CRUD rpc: server span → SQL span.
//   - QuoteWidget, a custom rpc: server span → pricing.Quote (the contract
//     decorator; the package carries NO forge:constructor marker, so this is
//     the default) → SQL span, and → rates.HealthCheck (the adapter's
//     decorator) → the outbound HTTP client span, whose traceparent the
//     downstream receives.
func TestE2ETraceDepth(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "traceapp", "--mod", "example.com/traceapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "traceapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	proto := strings.Replace(readFileE2E(t, protoPath), "  // TODO: Add your RPC methods here.\n}",
		"  rpc QuoteWidget(QuoteWidgetRequest) returns (QuoteWidgetResponse);\n}", 1)
	proto += `
// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}

message QuoteWidgetRequest {
  string id = 1;
}

message QuoteWidgetResponse {
  int64 cents = 1;
}
`
	writeFileE2E(t, protoPath, proto)
	runCmd(t, projectDir, forgeBin, "scaffold")
	runCmd(t, projectDir, forgeBin, "scaffold", "adapter", "rates")
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "pricing")

	writeTraceDepthCode(t, projectDir)

	runCmd(t, projectDir, forgeBin, "generate")
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	for _, want := range []string{"pricing.NewServiceWithForgeMiddleware(", "rates.NewServiceWithForgeMiddleware("} {
		if !strings.Contains(compose, want) {
			t.Fatalf("compose.go must construct %s wrapped in its decorator (instrumented by default):\n%s", want, compose)
		}
	}
	runCmd(t, projectDir, "go", "build", "./...")

	bootTraceDepth(t, projectDir)
}

// writeTraceDepthCode fills in the user's half: the pricing contract and its
// implementation, the handler's dep on it, and the custom rpc.
func writeTraceDepthCode(t *testing.T, projectDir string) {
	t.Helper()
	edit := func(rel, old, new string) {
		t.Helper()
		p := filepath.Join(projectDir, rel)
		s := readFileE2E(t, p)
		if !strings.Contains(s, old) {
			t.Fatalf("%s: the scaffold no longer contains %q — update this test's edit", rel, old)
		}
		writeFileE2E(t, p, strings.Replace(s, old, new, 1))
	}

	edit("internal/pricing/contract.go", "type Service interface {\n\t// Define your interface methods here.\n}",
		"type Service interface {\n\tQuote(ctx context.Context, id string) (int64, error)\n}")
	edit("internal/pricing/contract.go", "package pricing\n", "package pricing\n\nimport \"context\"\n")
	edit("internal/pricing/service.go", "\t// Add your dependencies here.\n}", "\tWidgets db.WidgetStore\n\tRates   rates.Service\n}")
	edit("internal/pricing/service.go", "import (\n",
		"import (\n\t\"context\"\n\n\t\"example.com/traceapp/internal/db\"\n\t\"example.com/traceapp/internal/rates\"\n")
	// Instrumented by DEFAULT: drop the scaffold's opt-in marker.
	edit("internal/pricing/service.go", "// forge:constructor\n//\n", "")
	svc := readFileE2E(t, filepath.Join(projectDir, "internal", "pricing", "service.go"))
	writeFileE2E(t, filepath.Join(projectDir, "internal", "pricing", "service.go"), svc+`
func (s *service) Quote(ctx context.Context, id string) (int64, error) {
	s.deps.Logger.InfoContext(ctx, "trace-depth: quoting", "widget", id)
	w, err := s.deps.Widgets.GetWidgetByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if err := s.deps.Rates.HealthCheck(ctx); err != nil {
		return 0, err
	}
	s.deps.Logger.InfoContext(ctx, "trace-depth: quoted", "widget", w.Name)
	return int64(len(w.Name)) * 100, nil
}
`)

	edit("internal/handlers/widgets/service.go", "\t// Add your dependencies here (e.g. Repo Repository).",
		"\tPricing pricing.Service\n\t// Add your dependencies here (e.g. Repo Repository).")
	edit("internal/handlers/widgets/service.go", "import (\n", "import (\n\t\"example.com/traceapp/internal/pricing\"\n")
	writeFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "widgets", "rpc_quote_widget.go"), `package widgets

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "example.com/traceapp/gen/services/widgets/v1"
)

// QuoteWidget prices a widget through the pricing contract.
func (s *Service) QuoteWidget(
	ctx context.Context,
	req *connect.Request[pb.QuoteWidgetRequest],
) (*connect.Response[pb.QuoteWidgetResponse], error) {
	s.deps.Logger.InfoContext(ctx, "trace-depth: handling quote")
	cents, err := s.deps.Pricing.Quote(ctx, req.Msg.GetId())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.QuoteWidgetResponse{Cents: cents}), nil
}
`)
	// The scaffolded row test asserts the stub's Unimplemented answer.
	_ = os.Remove(filepath.Join(projectDir, "internal", "handlers", "widgets", "handlers_scaffold_quote_widget_test.go"))
}

// otlpSink is an OTLP/HTTP trace receiver: it keeps every span it is sent.
type otlpSink struct {
	mu    sync.Mutex
	spans []*tracepb.Span
}

func (s *otlpSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/traces" {
		w.WriteHeader(http.StatusOK) // metrics, logs, profiles: accepted, ignored
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = zr
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			s.spans = append(s.spans, ss.GetSpans()...)
		}
	}
	s.mu.Unlock()
	out, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

// trace returns the spans of one trace, keyed by hex span id.
func (s *otlpSink) trace(traceID string) map[string]*tracepb.Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*tracepb.Span{}
	for _, sp := range s.spans {
		if hex.EncodeToString(sp.GetTraceId()) == traceID {
			out[hex.EncodeToString(sp.GetSpanId())] = sp
		}
	}
	return out
}

func bootTraceDepth(t *testing.T, projectDir string) {
	t.Helper()
	sink := &otlpSink{}
	collector := httptest.NewServer(sink)
	defer collector.Close()

	// The adapter's downstream: records the traceparent each call carries.
	var downstreamMu sync.Mutex
	var downstreamTraceparents []string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamMu.Lock()
		downstreamTraceparents = append(downstreamTraceparents, r.Header.Get("Traceparent"))
		downstreamMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()

	port := freePortE2E(t)
	serverBin := filepath.Join(projectDir, "trace-depth-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision trace-depth postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)

	pubPEM, bearer := mintDevJWT(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		"JWT_SECRET="+pubPEM,
		"LOG_FORMAT=json",
		"RATES_BASE_URL="+downstream.URL,
		"OTEL_EXPORTER_OTLP_ENDPOINT="+collector.URL,
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_TRACES_SAMPLER=always_on",
		"OTEL_BSP_SCHEDULE_DELAY=100",
	)
	serverOut := &apiKeyServerLog{}
	cmd.Stdout = serverOut
	cmd.Stderr = serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start trace-depth server: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
		}
	}
	defer stop()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 30*time.Second) {
		t.Fatalf("trace-depth server did not become ready\nserver output:\n%s", serverOut.String())
	}

	// post calls one rpc and returns its body and the trace the server
	// recorded it in (traceresponse when the server sends one, else the
	// trace id of the traceparent this test sends).
	post := func(procedure, body string) ([]byte, string) {
		t.Helper()
		traceID := fmt.Sprintf("%032x", time.Now().UnixNano())
		req, err := http.NewRequest(http.MethodPost, base+"/services.widgets.v1.WidgetsService/"+procedure, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build %s request: %v", procedure, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", bearer)
		req.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", procedure, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = HTTP %d, want 200\nresponse: %s\nserver output:\n%s", procedure, resp.StatusCode, out, serverOut.String())
		}
		if tr := resp.Header.Get("Traceresponse"); tr != "" {
			if parts := strings.Split(tr, "-"); len(parts) == 4 {
				traceID = parts[1]
			}
		}
		return out, traceID
	}

	created, createTrace := post("CreateWidget", `{"name":"gizmo"}`)
	var createResp struct {
		Widget struct {
			Id string `json:"id"`
		} `json:"widget"`
	}
	if err := json.Unmarshal(created, &createResp); err != nil || createResp.Widget.Id == "" {
		t.Fatalf("CreateWidget did not return a row (%v): %s", err, created)
	}
	quoted, quoteTrace := post("QuoteWidget", fmt.Sprintf(`{"id":%q}`, createResp.Widget.Id))
	if !strings.Contains(string(quoted), `"cents":"500"`) { // int64 is a JSON string
		t.Fatalf("QuoteWidget = %s, want 500 cents (5 letters x 100)", quoted)
	}

	// SIGTERM flushes the batch span processor; then every span is in.
	stop()
	logs := serverOut.String()

	// ── CRUD: server → SQL ──────────────────────────────────────────────
	crud := sink.trace(createTrace)
	assertTraceDepth(t, "CreateWidget", crud, 2, logs)
	assertHasSpan(t, "CreateWidget", crud, isSQLSpan, "a database span (dbtrace)")

	// ── Custom rpc: server → contract → SQL, and → adapter → HTTP ────────
	quote := sink.trace(quoteTrace)
	assertTraceDepth(t, "QuoteWidget", quote, 4, logs)
	assertHasSpan(t, "QuoteWidget", quote, func(s *tracepb.Span) bool {
		return strings.Contains(s.GetName(), "Quote") && s.GetKind() == tracepb.Span_SPAN_KIND_INTERNAL
	}, "the pricing.Quote decorator span")
	assertHasSpan(t, "QuoteWidget", quote, func(s *tracepb.Span) bool { return strings.Contains(s.GetName(), "HealthCheck") }, "the rates.HealthCheck decorator span")
	assertHasSpan(t, "QuoteWidget", quote, isSQLSpan, "a database span under pricing.Quote")
	assertHasSpan(t, "QuoteWidget", quote, func(s *tracepb.Span) bool {
		return s.GetKind() == tracepb.Span_SPAN_KIND_CLIENT && strings.HasPrefix(s.GetName(), "GET ")
	}, "the outbound HTTP client span")

	downstreamMu.Lock()
	got := append([]string(nil), downstreamTraceparents...)
	downstreamMu.Unlock()
	if len(got) == 0 || !strings.Contains(got[len(got)-1], quoteTrace) {
		t.Errorf("the adapter's downstream received traceparents %q, want one in trace %s: the outbound call left the trace", got, quoteTrace)
	}

	// ── Every log line of the request carries its trace_id ──────────────
	lines := 0
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "trace-depth:") {
			continue
		}
		lines++
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("log line is not JSON (LOG_FORMAT=json): %s", line)
			continue
		}
		if rec["trace_id"] != quoteTrace {
			t.Errorf("log line has trace_id %v, want %s — it cannot be joined to its request:\n%s", rec["trace_id"], quoteTrace, line)
		}
	}
	if lines != 3 {
		t.Errorf("found %d trace-depth log lines, want 3 (handler, quoting, quoted)\nserver output:\n%s", lines, logs)
	}
}

func isSQLSpan(s *tracepb.Span) bool {
	for _, a := range s.GetAttributes() {
		if a.GetKey() == "db.system" || a.GetKey() == "db.system.name" || a.GetKey() == "db.query.text" || a.GetKey() == "db.statement" {
			return true
		}
	}
	return false
}

// assertTraceDepth checks the trace has at least min spans, exactly one root
// (the server span), and every other span's parent inside the trace.
func assertTraceDepth(t *testing.T, rpc string, spans map[string]*tracepb.Span, min int, logs string) {
	t.Helper()
	if len(spans) < min {
		t.Fatalf("%s: %d span(s) in its trace, want at least %d — the trace is shallow:\n%s\nserver output:\n%s", rpc, len(spans), min, describeSpans(spans), logs)
	}
	var roots []*tracepb.Span
	for _, s := range spans {
		parent := hex.EncodeToString(s.GetParentSpanId())
		if _, ok := spans[parent]; !ok {
			roots = append(roots, s)
		}
	}
	if len(roots) != 1 || roots[0].GetKind() != tracepb.Span_SPAN_KIND_SERVER {
		t.Errorf("%s: want exactly one root, the server span; roots:\n%s\nall spans:\n%s", rpc, describeSpans(spanMap(roots)), describeSpans(spans))
	}
}

func assertHasSpan(t *testing.T, rpc string, spans map[string]*tracepb.Span, match func(*tracepb.Span) bool, what string) {
	t.Helper()
	for _, s := range spans {
		if match(s) {
			return
		}
	}
	t.Errorf("%s: no %s in its trace:\n%s", rpc, what, describeSpans(spans))
}

func spanMap(spans []*tracepb.Span) map[string]*tracepb.Span {
	out := map[string]*tracepb.Span{}
	for _, s := range spans {
		out[hex.EncodeToString(s.GetSpanId())] = s
	}
	return out
}

func describeSpans(spans map[string]*tracepb.Span) string {
	var b bytes.Buffer
	for id, s := range spans {
		fmt.Fprintf(&b, "  %s parent=%s kind=%s %q\n", id, hex.EncodeToString(s.GetParentSpanId()), s.GetKind(), s.GetName())
	}
	return b.String()
}
