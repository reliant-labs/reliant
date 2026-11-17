package observe

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// drain_logging_test.go — a rollout's drain is not an incident.
//
// A replica that is shutting down ends its long-lived calls — a daemon
// gateway tells each connected daemon "gateway draining" with Unavailable,
// so the daemon redials a live replica. Through the default chain that was a
// "stream failed" record at ERROR, error_class=server, and one Sentry event
// per connected client, on every rollout (20 in one product's prod over 7
// days), and the app had to know to declare WithExpectedErrors itself. A drain
// is expected by every layer without being told, and counts as canceled.

const watchProcedure = "/observe.test.v1.WatchService/Watch"

// lockedBuffer is a log sink the server's handler goroutine writes while the
// test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) snapshot() *bytes.Buffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.NewBuffer(append([]byte(nil), b.buf.Bytes()...))
}

// TestLoggingInterceptor_DrainedStreamIsAnExpectedOutcome runs the default
// chain — observe.Chain with no LogOptions — over a real server stream whose
// handler ends it the way a draining gateway does.
func TestLoggingInterceptor_DrainedStreamIsAnExpectedOutcome(t *testing.T) {
	t.Parallel()
	var buf lockedBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mux := http.NewServeMux()
	mux.Handle(watchProcedure, connect.NewServerStreamHandler(watchProcedure,
		func(context.Context, *connect.Request[structpb.Struct], *connect.ServerStream[structpb.Struct]) error {
			return connect.NewError(connect.CodeUnavailable,
				svcerr.Draining("gateway draining: this replica is shutting down; reconnect"))
		},
		connect.WithInterceptors(Chain(Deps{Logger: logger})...)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := connect.NewClient[structpb.Struct, structpb.Struct](srv.Client(), srv.URL+watchProcedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	if connect.CodeOf(stream.Err()) != connect.CodeUnavailable {
		t.Fatalf("client got %v, want Unavailable: that is the daemon's redial signal", stream.Err())
	}
	_ = stream.Close()

	written := buf.snapshot()
	recs := records(t, written, "stream failed")
	if len(recs) != 1 {
		t.Fatalf("want the drained stream written once, got %d:\n%s", len(recs), written.String())
	}
	rec := recs[0]
	if rec["level"] != "INFO" {
		t.Errorf("drained stream logged at %v, want INFO — a rollout's drain must not page", rec["level"])
	}
	if rec["expected"] != true {
		t.Errorf("drained stream expected = %v, want true", rec["expected"])
	}
	if rec[ErrorClassKey] != "canceled" || rec["code"] != "unavailable" {
		t.Errorf("drained stream class/code = %v/%v, want canceled/unavailable", rec[ErrorClassKey], rec["code"])
	}
}

// A hand-written logger.Error carrying a drain — the gateway logging "daemon
// stream ended" itself — is lowered the same way, and reaches no forwarder.
func TestErrorClassHandler_DrainIsNotAnIncident(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo)

	logger.Error("daemon stream ended", "error",
		fmt.Errorf("daemon d-1: %w", svcerr.Draining("gateway draining")))

	if gate.count() != 0 {
		t.Fatal("a drain reached the ERROR forwarder")
	}
	rec := records(t, &buf, "daemon stream ended")
	if len(rec) != 1 || rec[0]["level"] != "INFO" || rec[0][ErrorClassKey] != "canceled" {
		t.Fatalf("want one INFO record with error_class=canceled, got %v", rec)
	}
}

// The error counter keeps drains countable — the client did receive an
// error — under a class no server-fault alert selects.
func TestMetricsInterceptor_CountsADrainAsCanceled(t *testing.T) {
	t.Parallel()
	m := newFakeMeter()
	wrapped := MetricsInterceptor(m).WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, svcerr.Wrap(svcerr.Draining("draining"))
	})
	_, _ = wrapped(context.Background(), newTestRequest())

	got := m.counters["rpc.server.errors"].attrs
	if len(got) != 1 {
		t.Fatalf("want 1 error sample, got %d", len(got))
	}
	class, _ := got[0].Value(attribute.Key(ErrorClassKey))
	code, _ := got[0].Value("code")
	if class.AsString() != "canceled" || code.AsString() != "unavailable" {
		t.Errorf("drain counted as class %q code %q, want canceled unavailable", class.AsString(), code.AsString())
	}
}

// An app's own declarations accumulate on top of the drain default; they do
// not replace it.
func TestWithExpectedErrors_KeepsTheDrainDefault(t *testing.T) {
	t.Parallel()
	p := newLogPolicy(slog.LevelInfo, []LogOption{WithExpectedErrors(errObjectNotFound)})
	if !p.isExpected(svcerr.ErrServerDraining) || !p.isExpected(errObjectNotFound) {
		t.Fatal("declaring an expected error must not drop the drain default")
	}
	if p.isExpected(svcerr.Unavailable("payments offline")) {
		t.Fatal("a real Unavailable is not expected")
	}
}
