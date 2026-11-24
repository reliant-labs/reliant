// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// The gateway's call id (x-litellm-call-id) is what control-plane's wallet
// ledger keys a managed call's cost by, so it is on every gateway-routed chat
// span: a success, a failure and a stream alike, not only in the failure log.

const (
	gwChatJSON   = `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`
	gwStreamBody = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	gwBadRequest = `{"error":{"message":"bad request","type":"invalid_request_error","code":"bad"}}`
)

// gwReply is one response of the fake gateway.
type gwReply struct {
	status int    // 0 is 200
	callID string // "" sends no x-litellm-call-id
	body   string
	stream bool
}

// scriptedGateway answers its requests with replies in order, the last one
// repeated.
func scriptedGateway(t *testing.T, replies ...gwReply) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		r := replies[i]
		if r.callID != "" {
			w.Header().Set("x-litellm-call-id", r.callID)
		}
		w.Header().Set("x-oai-request-id", "req-upstream")
		if r.stream {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		if r.status != 0 {
			w.WriteHeader(r.status)
		}
		_, _ = fmt.Fprint(w, r.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newGatewayTestClient(srv *httptest.Server) *ReliantClient {
	c := NewClient(llm.DriverOptions{ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100, Model: models.Model{ID: "m", APIModel: "m"}})
	c.ladder = ratelimit.Retryer{
		MaxAttempts: 3,
		Jitter:      func(time.Duration) time.Duration { return 0 },
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
	return c
}

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// gatewayCallIDOf is the span's reliant.gateway.call_id, and whether it is set.
func gatewayCallIDOf(s sdktrace.ReadOnlySpan) (string, bool) {
	for _, kv := range s.Attributes() {
		if kv.Key == attribute.Key(telemetry.AttrGatewayCallID) {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

// callUnderChatSpan makes one call through c under a chat span, as the base
// driver does, and returns the ended span and the call's error.
func callUnderChatSpan(t *testing.T, rec *tracetest.SpanRecorder, c *ReliantClient, stream bool) (sdktrace.ReadOnlySpan, error) {
	t.Helper()
	ctx, span := llm.StartGenAISpan(context.Background(), "reliant", c.Options.Model, stream)
	var err error
	if stream {
		for ev := range c.StreamResponse(ctx, nil, nil, nil) {
			span.Observe(ev)
			if ev.Type == llm.EventError {
				err = ev.Error
			}
		}
	} else {
		var resp *llm.DriverResponse
		resp, err = c.SendMessages(ctx, nil, nil, nil)
		span.RecordResponse(resp, "")
		span.RecordError(err)
	}
	span.End()
	var chat []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if strings.HasPrefix(s.Name(), "chat ") {
			chat = append(chat, s)
		} else if _, ok := gatewayCallIDOf(s); ok {
			t.Errorf("span %q is not the chat span and carries the gateway call id", s.Name())
		}
	}
	require.Len(t, chat, 1)
	return chat[0], err
}

func TestGatewayCallIDOnChatSpan(t *testing.T) {
	tests := []struct {
		name    string
		stream  bool
		replies []gwReply
		wantErr bool
		want    string // "" means the attribute is absent
	}{
		{name: "send success", replies: []gwReply{{callID: "call-ok", body: gwChatJSON}}, want: "call-ok"},
		{name: "send error", replies: []gwReply{{status: http.StatusBadRequest, callID: "call-err", body: gwBadRequest}}, wantErr: true, want: "call-err"},
		{name: "stream success", stream: true, replies: []gwReply{{callID: "call-stream", body: gwStreamBody, stream: true}}, want: "call-stream"},
		{name: "stream error at open", stream: true, replies: []gwReply{{status: http.StatusBadRequest, callID: "call-stream-err", body: gwBadRequest}}, wantErr: true, want: "call-stream-err"},
		{
			// The attempt that ended the call names it, not the refused one.
			name: "send retried, last attempt wins",
			replies: []gwReply{
				{status: http.StatusServiceUnavailable, callID: "call-503", body: `{"error":{"message":"service unavailable"}}`},
				{callID: "call-retry-ok", body: gwChatJSON},
			},
			want: "call-retry-ok",
		},
		{
			name:   "stream retried, last attempt wins",
			stream: true,
			replies: []gwReply{
				{status: http.StatusServiceUnavailable, callID: "call-503", body: `{"error":{"message":"service unavailable"}}`},
				{callID: "call-stream-retry-ok", body: gwStreamBody, stream: true},
			},
			want: "call-stream-retry-ok",
		},
		{name: "no header sets nothing", replies: []gwReply{{body: gwChatJSON}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := recordSpans(t)
			c := newGatewayTestClient(scriptedGateway(t, tt.replies...))

			s, err := callUnderChatSpan(t, rec, c, tt.stream)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			got, ok := gatewayCallIDOf(s)
			if tt.want == "" {
				assert.False(t, ok, "no gateway call id was sent, so none is recorded (got %q)", got)
				return
			}
			assert.True(t, ok, "the chat span carries %s", telemetry.AttrGatewayCallID)
			assert.Equal(t, tt.want, got)
			assert.NotEqual(t, got, attrString(s, "gen_ai.response.id"), "the gateway id is not the upstream provider's id")
		})
	}
}

// A call with no chat span on its ctx (a key validation) puts the id on no
// other span.
func TestGatewayCallIDNeedsAChatSpan(t *testing.T) {
	rec := recordSpans(t)
	c := newGatewayTestClient(scriptedGateway(t, gwReply{callID: "call-ok", body: gwChatJSON}))

	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	_, err := c.SendMessages(ctx, nil, nil, nil)
	require.NoError(t, err)
	parent.End()

	ended := rec.Ended()
	require.NotEmpty(t, ended)
	for _, s := range ended {
		_, ok := gatewayCallIDOf(s)
		assert.False(t, ok, "span %q: only the call's own chat span takes the gateway id", s.Name())
	}
}

// The ledger must never join the final outcome to a refused attempt's bill.
func TestGatewayCallIDClearedOnRetryWithoutHeader(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			body := gwChatJSON
			if stream {
				body = gwStreamBody
			}
			c := newGatewayTestClient(scriptedGateway(t,
				gwReply{status: http.StatusServiceUnavailable, callID: "refused-call", body: `{"error":{"message":"service unavailable"}}`},
				gwReply{body: body, stream: stream},
			))
			var call llm.GatewayCall
			ctx := llm.WithGatewayCall(context.Background(), &call)
			if stream {
				for ev := range c.StreamResponse(ctx, nil, nil, nil) {
					require.NotEqual(t, llm.EventError, ev.Type)
				}
			} else {
				_, err := c.SendMessages(ctx, nil, nil, nil)
				require.NoError(t, err)
			}
			assert.Empty(t, call.ID())
		})
	}
}

func TestGatewayCallIDClearedByUnansweredAttempt(t *testing.T) {
	var call llm.GatewayCall
	ctx := llm.WithGatewayCall(context.Background(), &call)
	recordGatewayCallID(ctx, &http.Response{Header: http.Header{"X-Litellm-Call-Id": {"refused-call"}}})
	require.Equal(t, "refused-call", call.ID())
	// Both send and stream paths pass nil when no response was received.
	recordGatewayCallID(ctx, nil)
	assert.Empty(t, call.ID())
}

func attrString(s sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}
