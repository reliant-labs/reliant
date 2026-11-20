// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

func passthrough(next connect.ServerFunc) connect.ServerFunc { return next }

// unaryHandler is a unary rpc's handler as a v2 ServerFunc, as the generated
// adapter runs it: receive the request, answer, send the response.
type unaryHandler func(ctx context.Context, req proto.Message) (proto.Message, error)

func (h unaryHandler) serve(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
	req := newRequest(spec)
	if req != nil {
		if err := stream.Receive(req); err != nil {
			return err
		}
	}
	resp, err := h(ctx, req)
	if err != nil || resp == nil {
		return err
	}
	return stream.Send(resp)
}

// unarySpec is the spec of the unary rpc whose request is req's type.
func unarySpec(t *testing.T, req proto.Message) connect.Spec {
	t.Helper()
	spec := connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: "/test.v1.Test/Call"}
	if req == nil {
		return spec
	}
	want := req.ProtoReflect().Descriptor().FullName()
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			methods := fd.Services().Get(i).Methods()
			for j := range methods.Len() {
				if md := methods.Get(j); md.Input().FullName() == want && !md.IsStreamingClient() && !md.IsStreamingServer() {
					spec.Schema = md
					return false
				}
			}
		}
		return true
	})
	require.NotNil(t, spec.Schema, "no unary rpc takes %s", want)
	return spec
}

// fakeStream is a unary call's stream: it receives req, keeps what is sent.
type fakeStream struct {
	req  proto.Message
	sent []any
}

func (s *fakeStream) Receive(msg any) error {
	proto.Merge(msg.(proto.Message), s.req)
	return nil
}
func (s *fakeStream) SendHeaders() error { return nil }
func (s *fakeStream) Send(msg any) error { s.sent = append(s.sent, msg); return nil }

// The layer that logs a failed RPC sits outside authentication; the user the
// authenticator binds must still reach it, and the server span.
func TestRequestScopeLetsAuthAttributeOuterLayers(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")

	var outer context.Context
	handler := RequestScope(passthrough)(unaryHandler(func(ctx context.Context, _ proto.Message) (proto.Message, error) {
		outer = ctx
		telemetry.BindUser(ctx, "user-1")
		return nil, nil
	}).serve)
	err := handler(ctx, unarySpec(t, nil), &fakeStream{})
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
func serverSpan(t *testing.T, req proto.Message, handler unaryHandler) []attribute.KeyValue {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")
	_ = RequestScope(passthrough)(handler.serve)(ctx, unarySpec(t, req), &fakeStream{req: req})
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
		&reliantv1.ListMessagesRequest{ChatId: "chat-1", ThreadId: proto.String("thread-1")},
		func(ctx context.Context, _ proto.Message) (proto.Message, error) {
			inner = ctx
			return &reliantv1.ListMessagesResponse{}, nil
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
		&reliantv1.StartChatRequest{},
		func(context.Context, proto.Message) (proto.Message, error) {
			return &reliantv1.StartChatResponse{Chat: &reliantv1.Chat{Id: "chat-2"}, WorkflowId: "wf-2"}, nil
		})
	assert.Contains(t, attrs, attribute.String("chat.id", "chat-2"))
	assert.Contains(t, attrs, attribute.String("workflow.id", "wf-2"))
}

// The request's ids win: a response naming another chat does not relabel
// the span, and a failed call stamps only what the request named.
func TestRequestScopeKeepsTheRequestsRun(t *testing.T) {
	attrs := serverSpan(t,
		&reliantv1.GetChatRequest{ChatId: "chat-3"},
		func(context.Context, proto.Message) (proto.Message, error) {
			return &reliantv1.GetChatResponse{Chat: &reliantv1.Chat{Id: "other"}}, nil
		})
	var chats []string
	for _, kv := range attrs {
		if kv.Key == "chat.id" {
			chats = append(chats, kv.Value.AsString())
		}
	}
	assert.Equal(t, []string{"chat-3"}, chats)

	failed := serverSpan(t, &reliantv1.GetChatRequest{ChatId: "chat-4"},
		func(context.Context, proto.Message) (proto.Message, error) {
			return nil, connect.NewError(connect.CodeNotFound, "")
		})
	assert.Contains(t, failed, attribute.String("chat.id", "chat-4"))
}

// A stream names its run in its first message, after the span opened.
func TestRequestScopeStampsAStreamFromItsFirstMessage(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")
	handler := RequestScope(passthrough)(func(_ context.Context, _ connect.Spec, stream connect.ServerStream) error {
		return stream.Receive(&reliantv1.ListMessagesRequest{})
	})
	spec := connect.Spec{StreamType: connect.StreamTypeBidi, Procedure: "/test.v1.Test/Stream"}
	require.NoError(t, handler(ctx, spec, &fakeStream{req: &reliantv1.ListMessagesRequest{ChatId: "chat-5"}}))
	span.End()
	require.Len(t, recorder.Ended(), 1)
	assert.Contains(t, recorder.Ended()[0].Attributes(), attribute.String("chat.id", "chat-5"))
}

func TestRequestScopeIgnoresMessagesThatNameNoRun(t *testing.T) {
	attrs := serverSpan(t, &reliantv1.ListInboxRequest{},
		func(context.Context, proto.Message) (proto.Message, error) {
			return &reliantv1.ListInboxResponse{}, nil
		})
	assert.Empty(t, attrs)
}
