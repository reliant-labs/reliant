// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The browser's trace context, as the web client sends it on the upgrade URL.
const (
	browserTraceID = "0af7651916cd43dd8448eb211c80319c"
	browserSpanID  = "b7ad6b7169203331"
)

// terminalTelemetryHarness is a TerminalWSHandler whose spans and metrics go
// to an in-memory recorder and reader.
type terminalTelemetryHarness struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	url    string
}

func newTerminalTelemetryHarness(t *testing.T, router toolexec.DaemonRouter, opts ...TerminalWSOption) *terminalTelemetryHarness {
	t.Helper()
	h := &terminalTelemetryHarness{spans: tracetest.NewSpanRecorder(), reader: sdkmetric.NewManualReader()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	opts = append(opts, withTerminalTelemetry(terminalTelemetryProviders{
		tracer: tp, meter: mp, propagator: propagation.TraceContext{},
	}))
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}, opts...))
	t.Cleanup(srv.Close)
	h.url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	return h
}

// dial opens a terminal connection with query (token included by the caller).
func (h *terminalTelemetryHarness) dial(t *testing.T, query string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(h.url+"?"+query, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

// span waits for the connection's span to end and returns it.
func (h *terminalTelemetryHarness) span(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	require.Eventually(t, func() bool { return len(h.spans.Ended()) > 0 }, 5*time.Second, 10*time.Millisecond,
		"the connection's terminal.session span never ended")
	ended := h.spans.Ended()
	require.Len(t, ended, 1, "one span per connection")
	return ended[0]
}

func (h *terminalTelemetryHarness) metrics(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, h.reader.Collect(context.Background(), &rm))
	return rm
}

// int64Sum is the value of a counter or up-down counter's point whose
// attributes include want (all of them when want is empty); 0 when the
// instrument has recorded nothing.
func int64Sum(t *testing.T, rm metricdata.ResourceMetrics, name string, want ...attribute.KeyValue) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "%s is not an int64 sum", name)
			for _, dp := range sum.DataPoints {
				if hasAttributes(dp.Attributes, want) {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// histogramCount is how many values the histogram name recorded with want.
func histogramCount(t *testing.T, rm metricdata.ResourceMetrics, name string, want ...attribute.KeyValue) uint64 {
	t.Helper()
	var count uint64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "%s is not a float64 histogram", name)
			for _, dp := range hist.DataPoints {
				if hasAttributes(dp.Attributes, want) {
					count += dp.Count
				}
			}
		}
	}
	return count
}

func hasAttributes(set attribute.Set, want []attribute.KeyValue) bool {
	for _, kv := range want {
		if v, ok := set.Value(kv.Key); !ok || v != kv.Value {
			return false
		}
	}
	return true
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes() {
		attrs[kv.Key] = kv.Value
	}
	return attrs
}

func outcome(o string) attribute.KeyValue { return attribute.String("outcome", o) }

// A session the browser opens and closes is one server span continuing the
// browser's trace, with its outcome and traffic, and moves every session
// metric by exactly one connection's worth.
func TestTerminalWS_TracesAndMetersASession(t *testing.T) {
	router := &pingTestRouter{terminalCommandRouter: newTerminalCommandRouter(), inputs: make(chan []byte, 8)}
	router.output = make(chan *toolexec.TerminalOutputEvent, 1)
	h := newTerminalTelemetryHarness(t, router)

	conn := h.dial(t, "token=secret-token&workingDir=/tmp&projectId=proj-1&reconnect=1"+
		"&traceparent=00-"+browserTraceID+"-"+browserSpanID+"-01")
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)

	// While the session is open it is counted as active.
	require.Equal(t, int64(1), int64Sum(t, h.metrics(t), "reliant.terminal.sessions.active"))

	// 3 bytes in, 5 bytes out.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("ls\n")))
	select {
	case <-router.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("input was not forwarded")
	}
	router.output <- &toolexec.TerminalOutputEvent{Data: []byte("hello")}
	var out wsMessage
	require.NoError(t, conn.ReadJSON(&out))
	require.Equal(t, "hello", out.Data)

	require.NoError(t, conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))

	span := h.span(t)
	require.Equal(t, "terminal.session", span.Name())
	require.Equal(t, trace.SpanKindServer, span.SpanKind())
	require.Equal(t, browserTraceID, span.SpanContext().TraceID().String(), "the span must join the browser's trace")
	require.Equal(t, browserSpanID, span.Parent().SpanID().String(), "the browser's span is the parent")
	require.NotEqual(t, codes.Error, span.Status().Code)

	attrs := spanAttributes(span)
	require.Equal(t, outcomeClientClosed, attrs[attrOutcome].AsString())
	require.Equal(t, int64(3), attrs[attrBytesIn].AsInt64())
	require.Equal(t, int64(5), attrs[attrBytesOut].AsInt64())
	require.True(t, attrs[attrReconnect].AsBool())
	require.Equal(t, "daemon-session-1", attrs[attrSessionID].AsString())
	require.Equal(t, "proj-1", attrs[attrProjectID].AsString())
	require.Equal(t, terminalTestUserID, attrs["user.id"].AsString())
	require.Contains(t, attrs, attribute.Key(attrDurationMS))
	for k, v := range attrs {
		require.NotContains(t, v.Emit(), "secret-token", "attribute %s leaks the token", k)
		require.NotContains(t, v.Emit(), "/tmp", "attribute %s leaks the working directory", k)
	}

	rm := h.metrics(t)
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.sessions.started", attribute.Bool("reconnect", true)))
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.sessions.ended", outcome(outcomeClientClosed)))
	require.Equal(t, int64(0), int64Sum(t, rm, "reliant.terminal.sessions.active"))
	require.Equal(t, uint64(1), histogramCount(t, rm, "reliant.terminal.session.duration", outcome(outcomeClientClosed)))
	require.Equal(t, int64(3), int64Sum(t, rm, "reliant.terminal.bytes", attribute.String("direction", "in")))
	require.Equal(t, int64(5), int64Sum(t, rm, "reliant.terminal.bytes", attribute.String("direction", "out")))
	require.Equal(t, int64(0), int64Sum(t, rm, "reliant.terminal.ping_timeouts"))
	require.Equal(t, int64(0), int64Sum(t, rm, "reliant.terminal.sessions.rejected"))
}

// failingCreateRouter fails terminal.create with an error that is not a
// machine state: the daemon broke.
type failingCreateRouter struct{ *terminalCommandRouter }

func (r failingCreateRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "terminal.create" {
		return nil, errors.New("forkpty: device not configured")
	}
	return r.terminalCommandRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// A daemon that cannot open the shell is an error span and a create_failed
// session, and its error text (which can name a path) stays off the span.
func TestTerminalWS_TracesAFailedCreate(t *testing.T) {
	h := newTerminalTelemetryHarness(t, failingCreateRouter{newTerminalCommandRouter()})

	conn := h.dial(t, "token=test&workingDir=/tmp")
	var msg wsMessage
	require.NoError(t, conn.ReadJSON(&msg))
	require.Equal(t, "error", msg.Type)

	span := h.span(t)
	require.Equal(t, codes.Error, span.Status().Code)
	require.Equal(t, outcomeCreateFailed, span.Status().Description)
	attrs := spanAttributes(span)
	require.Equal(t, outcomeCreateFailed, attrs[attrOutcome].AsString())
	require.Equal(t, outcomeCreateFailed, attrs["error.type"].AsString())
	for k, v := range attrs {
		require.NotContains(t, v.Emit(), "forkpty", "attribute %s carries the daemon's error text", k)
	}
	require.Empty(t, span.Events(), "the daemon's error text must not be recorded as a span event")

	rm := h.metrics(t)
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.sessions.started", attribute.Bool("reconnect", false)))
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.sessions.ended", outcome(outcomeCreateFailed)))
	require.Equal(t, int64(0), int64Sum(t, rm, "reliant.terminal.sessions.active"))
}

// A browser that stops answering is closed at the pong deadline and counted
// as a ping timeout.
func TestTerminalWS_MetersAPingTimeout(t *testing.T) {
	h := newTerminalTelemetryHarness(t, newTerminalCommandRouter(), withPongTimeout(200*time.Millisecond))

	conn := h.dial(t, "token=test&workingDir=/tmp")
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)
	// Send nothing: the read deadline passes with no pong.

	span := h.span(t)
	require.Equal(t, outcomePingTimeout, spanAttributes(span)[attrOutcome].AsString())
	require.NotEqual(t, codes.Error, span.Status().Code, "a browser that went away is not a server fault")

	rm := h.metrics(t)
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.ping_timeouts"))
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.sessions.ended", outcome(outcomePingTimeout)))
}

// A request refused before the upgrade still gets its span, and is counted as
// rejected rather than as a session.
func TestTerminalWS_TracesARefusedUpgrade(t *testing.T) {
	h := newTerminalTelemetryHarness(t, newTerminalCommandRouter())

	_, resp, err := websocket.DefaultDialer.Dial(h.url+"?workingDir=/tmp", nil)
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = resp.Body.Close()

	span := h.span(t)
	require.Equal(t, outcomeUnauthenticated, spanAttributes(span)[attrOutcome].AsString())

	rm := h.metrics(t)
	require.Equal(t, int64(1), int64Sum(t, rm, "reliant.terminal.sessions.rejected", outcome(outcomeUnauthenticated)))
	require.Equal(t, int64(0), int64Sum(t, rm, "reliant.terminal.sessions.started"))
	require.Equal(t, int64(0), int64Sum(t, rm, "reliant.terminal.sessions.active"))
}
