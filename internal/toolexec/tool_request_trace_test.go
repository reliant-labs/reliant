// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// tracingDaemonMgr answers every tool request and records the trace context
// the gateway hands the daemon round trip: the context its
// gateway.daemon.tool span is opened under.
type tracingDaemonMgr struct {
	recordingDaemonMgr
	mu  sync.Mutex
	saw []trace.SpanContext
}

func (m *tracingDaemonMgr) SendToolRequestSync(ctx context.Context, _, _ string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	m.mu.Lock()
	m.saw = append(m.saw, trace.SpanContextFromContext(ctx))
	m.mu.Unlock()
	return &ToolExecutionResponse{RequestID: req.RequestID, Success: true, Content: "ok"}, nil
}

// The worker's tool-call span reaches the gateway across NATS: the gateway's
// handling of tools.request.sync — and so its gateway.daemon.tool span, opened
// under that context — is a child of the worker's execute_tool span, on the
// same trace, whether the request fits one message or is chunked. It swaps
// the global tracer provider and propagator, so it does not run in parallel.
func TestToolRequestSync_TraceContextReachesTheGateway(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	for name, input := range map[string]string{
		"one message": `{"file_path":"/tmp/x"}`,
		"chunked":     `{"content":"` + strings.Repeat("x", 3<<20) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec.Reset()
			nc := startBridgeTestNATS(t)
			mgr := &tracingDaemonMgr{}
			bridge := NewNATSToolBridge(nc, nil, mgr)
			t.Cleanup(func() { _ = bridge.Close() })
			const userID, daemonID = "u-trace", "d-trace"
			bridge.OnDaemonConnected(userID, daemonID)
			router := NewNATSDaemonRouter(nc)

			ctx, toolSpan := otel.Tracer("test").Start(context.Background(), "execute_tool view")
			resp, _, err := router.toolRequestRoundTrip(ctx, userID, daemonID,
				&ToolExecutionRequest{RequestID: "req-" + strings.ReplaceAll(name, " ", "-"), ToolName: "view", ToolInput: input}, "tools.request.sync")
			toolSpan.End()
			require.NoError(t, err)
			require.True(t, resp.Success)

			require.Len(t, mgr.saw, 1)
			gateway := mgr.saw[0]
			assert.Equal(t, toolSpan.SpanContext().TraceID(), gateway.TraceID(), "the gateway's work is on the tool call's trace")

			// Walk the gateway's span up to the worker's tool span. The
			// gateway ends its span after it replies, so wait for it.
			byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
			require.Eventually(t, func() bool {
				for _, s := range rec.Ended() {
					byID[s.SpanContext().SpanID()] = s
				}
				_, ok := byID[gateway.SpanID()]
				return ok
			}, 5*time.Second, 5*time.Millisecond)
			found := false
			for s := byID[gateway.SpanID()]; s != nil; s = byID[s.Parent().SpanID()] {
				if s.SpanContext().SpanID() == toolSpan.SpanContext().SpanID() {
					found = true
					break
				}
			}
			assert.True(t, found, "the gateway's span descends from the worker's execute_tool span")
			consumer := byID[gateway.SpanID()]
			require.NotNil(t, consumer)
			assert.Equal(t, "nats.handle.tools.request.sync", consumer.Name())
		})
	}
}
