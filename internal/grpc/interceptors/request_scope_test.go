// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

type passthrough struct{}

func (passthrough) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (passthrough) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (passthrough) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// The layer that logs a failed RPC sits outside authentication; the user the
// authenticator binds must still reach it, and the server span.
func TestRequestScopeLetsAuthAttributeOuterLayers(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")

	var outer context.Context
	handler := RequestScope(passthrough{}).WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		outer = ctx
		telemetry.BindUser(ctx, "user-1")
		return nil, nil
	})
	_, err := handler(ctx, nil)
	require.NoError(t, err)
	span.End()

	assert.Equal(t, "user-1", telemetry.UserIDFromContext(outer))
	require.True(t, trace.SpanFromContext(ctx).SpanContext().IsValid())
	ended := recorder.Ended()
	require.Len(t, ended, 1)
	assert.Contains(t, ended[0].Attributes(), attribute.String(telemetry.AttrUserID, "user-1"))
}

// serverSpan runs one unary call of handler through RequestScope inside a
// recorded server span and returns the span's attributes.
func serverSpan(t *testing.T, req connect.AnyRequest, handler connect.UnaryFunc) []attribute.KeyValue {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")
	_, _ = RequestScope(passthrough{}).WrapUnary(handler)(ctx, req)
	span.End()
	ended := recorder.Ended()
	require.Len(t, ended, 1)
	return ended[0].Attributes()
}

// A trace is found by its chat: the server span carries the ids the request
// names, and the logs written inside the RPC are scoped to them.
func TestRequestScopeStampsTheRunTheRequestNames(t *testing.T) {
	var inner context.Context
	attrs := serverSpan(t,
		connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: "chat-1", ThreadId: proto.String("thread-1")}),
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			inner = ctx
			return connect.NewResponse(&reliantv1.ListMessagesResponse{}), nil
		})
	assert.Contains(t, attrs, attribute.String("chat.id", "chat-1"))
	assert.Contains(t, attrs, attribute.String("thread.id", "thread-1"))
	assert.Equal(t, "chat-1", telemetry.RunScopeFromContext(inner).ChatID)
	for _, kv := range attrs {
		assert.NotEqual(t, "workflow.id", string(kv.Key), "an id the request does not name is absent, not empty")
	}
}

// StartChat's request names no chat; its response carries the one it made,
// and the workflow running it.
func TestRequestScopeStampsTheRunTheResponseNames(t *testing.T) {
	attrs := serverSpan(t,
		connect.NewRequest(&reliantv1.StartChatRequest{}),
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&reliantv1.StartChatResponse{Chat: &reliantv1.Chat{Id: "chat-2"}, WorkflowId: "wf-2"}), nil
		})
	assert.Contains(t, attrs, attribute.String("chat.id", "chat-2"))
	assert.Contains(t, attrs, attribute.String("workflow.id", "wf-2"))
}

// The request's ids win: a response naming another chat does not relabel
// the span, and a failed call stamps only what the request named.
func TestRequestScopeKeepsTheRequestsRun(t *testing.T) {
	attrs := serverSpan(t,
		connect.NewRequest(&reliantv1.GetChatRequest{ChatId: "chat-3"}),
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&reliantv1.GetChatResponse{Chat: &reliantv1.Chat{Id: "other"}}), nil
		})
	var chats []string
	for _, kv := range attrs {
		if kv.Key == "chat.id" {
			chats = append(chats, kv.Value.AsString())
		}
	}
	assert.Equal(t, []string{"chat-3"}, chats)

	failed := serverSpan(t, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: "chat-4"}),
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeNotFound, nil)
		})
	assert.Contains(t, failed, attribute.String("chat.id", "chat-4"))
}

// receivingConn is a stream whose client sends one message.
type receivingConn struct {
	connect.StreamingHandlerConn
	send proto.Message
}

func (c receivingConn) Receive(msg any) error {
	proto.Merge(msg.(proto.Message), c.send)
	return nil
}

// A stream names its run in its first message, after the span opened.
func TestRequestScopeStampsAStreamFromItsFirstMessage(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")
	handler := RequestScope(passthrough{}).WrapStreamingHandler(func(_ context.Context, conn connect.StreamingHandlerConn) error {
		return conn.Receive(&reliantv1.ListMessagesRequest{})
	})
	require.NoError(t, handler(ctx, receivingConn{send: &reliantv1.ListMessagesRequest{ChatId: "chat-5"}}))
	span.End()
	require.Len(t, recorder.Ended(), 1)
	assert.Contains(t, recorder.Ended()[0].Attributes(), attribute.String("chat.id", "chat-5"))
}

func TestRequestScopeIgnoresMessagesThatNameNoRun(t *testing.T) {
	attrs := serverSpan(t, connect.NewRequest(&reliantv1.ListInboxRequest{}),
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&reliantv1.ListInboxResponse{}), nil
		})
	assert.Empty(t, attrs)
}
