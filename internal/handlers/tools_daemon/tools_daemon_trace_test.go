// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

func installTestTracing(t *testing.T) (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec, tp
}

// answeringConn is a daemon connection whose "daemon" answers every command
// with resp, capturing the request it received.
func answeringConn(t *testing.T, resp *reliantv1.DaemonCommandResponse, got chan<- *reliantv1.DaemonCommandRequest) *daemonConnection {
	t.Helper()
	conn := &daemonConnection{
		userID:          "u1",
		daemonID:        "d1",
		daemonType:      "cloud",
		platform:        "linux",
		attachSource:    db.DaemonAttachmentSourceInbound,
		network:         daemonNetworkOf(http.Header{"X-Forwarded-For": {"203.0.113.7, 10.0.0.1"}, headerClientRegion: {"US"}, headerClientCity: {"Nashville"}}, "10.0.0.1:443"),
		sendCh:          make(chan *reliantv1.ServerMessage, 1),
		done:            make(chan struct{}),
		pendingCommands: make(map[string]chan *reliantv1.DaemonCommandResponse),
	}
	t.Cleanup(func() { close(conn.done) })
	go func() {
		select {
		case <-conn.done:
		case msg := <-conn.sendCh:
			cmd := msg.GetDaemonCommand()
			got <- cmd
			conn.pendingCommandsMu.Lock()
			ch := conn.pendingCommands[cmd.GetRequestId()]
			conn.pendingCommandsMu.Unlock()
			ch <- resp
		}
	}()
	return conn
}

// The gateway's command span is the daemon command in a trace: it is a child
// of the NATS handler span, its context rides to the daemon in
// trace_context, and it names the daemon, where it is, and how the round trip
// split between the daemon's handler and everything else.
func TestSendCommandToConn_TracesTheRoundTripToTheDaemon(t *testing.T) {
	rec, tp := installTestTracing(t)
	got := make(chan *reliantv1.DaemonCommandRequest, 1)
	conn := answeringConn(t, &reliantv1.DaemonCommandResponse{
		RequestId: "r1", Success: true, HandlerElapsedMs: 7,
	}, got)

	ctx, parent := tp.Tracer("test").Start(context.Background(), "nats.handle.daemon.command")
	_, err := (&Service{}).sendCommandToConn(ctx, conn, &reliantv1.DaemonCommandRequest{
		RequestId: "r1", CommandType: "fs.list",
	})
	parent.End()
	require.NoError(t, err)

	var span sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "gateway.daemon.command fs.list" {
			span = s
		}
	}
	require.NotNil(t, span, "no gateway.daemon.command span")
	assert.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID(), "the command span must be a child of the NATS handler span")
	assert.Equal(t, trace.SpanKindClient, span.SpanKind())

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes() {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "d1", attrs[attrDaemonID].AsString())
	assert.Equal(t, "fs.list", attrs[attrCommandType].AsString())
	assert.Equal(t, "203.0.113.7", attrs[attrClientAddress].AsString())
	assert.Equal(t, "10.0.0.1", attrs[attrNetworkPeerAddr].AsString())
	assert.Equal(t, "US", attrs[attrGeoCountryISOCode].AsString())
	assert.Equal(t, "Nashville", attrs[attrGeoLocalityName].AsString())
	assert.Equal(t, int64(7), attrs[attrHandlerElapsedMs].AsInt64())
	assert.Equal(t, "ok", attrs[attrCommandOutcome].AsString())

	// The daemon received the command span's context.
	cmd := <-got
	carried := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier(cmd.GetTraceContext()))
	sc := trace.SpanContextFromContext(carried)
	assert.Equal(t, span.SpanContext().TraceID(), sc.TraceID())
	assert.Equal(t, span.SpanContext().SpanID(), sc.SpanID(), "the daemon must continue the command span, not its parent")
}

func TestSendCommandToConn_ADaemonFailureMarksTheSpan(t *testing.T) {
	rec, _ := installTestTracing(t)
	got := make(chan *reliantv1.DaemonCommandRequest, 1)
	conn := answeringConn(t, &reliantv1.DaemonCommandResponse{
		RequestId: "r2", Success: false, ErrorMessage: "not a git repository",
	}, got)

	_, err := (&Service{}).sendCommandToConn(context.Background(), conn, &reliantv1.DaemonCommandRequest{
		RequestId: "r2", CommandType: "git.status",
	})
	require.NoError(t, err)

	require.Len(t, rec.Ended(), 1)
	span := rec.Ended()[0]
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, "not a git repository", span.Status().Description)
}

func TestDaemonNetworkOf(t *testing.T) {
	n := daemonNetworkOf(http.Header{}, "198.51.100.4:51234")
	assert.Equal(t, "198.51.100.4", n.clientAddress, "with no proxy the peer is the client")

	n = daemonNetworkOf(http.Header{"X-Real-Ip": {"192.0.2.9"}, headerClientRegion: {"ZZ"}}, "[::1]:443")
	assert.Equal(t, "192.0.2.9", n.clientAddress)
	assert.Equal(t, "::1", n.peerAddress)
	assert.Empty(t, n.country, "the load balancer's unknown-region placeholder is not a country")
}
