// Copyright (c) 2026 Reliant Labs
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordAISpans installs an in-memory tracer provider for the AI span helpers,
// which resolve the global one per span. It swaps a global, so its tests do
// not run in parallel.
func recordAISpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// errSecret is an error whose text carries what a provider or a machine might
// echo into one: none of it may reach a span.
var errSecret = errors.New(`provider said: prompt "my password is hunter2" at /home/alice/.ssh/id_rsa`)

// assertNoErrorText fails if any of the span's attributes, events or status
// description carries the error's text.
func assertNoErrorText(t *testing.T, s sdktrace.ReadOnlySpan, err error) {
	t.Helper()
	assert.Empty(t, s.Events(), "no exception event: RecordError records the error's text")
	assert.NotContains(t, s.Status().Description, "hunter2")
	assert.NotContains(t, s.Status().Description, err.Error())
	for _, kv := range s.Attributes() {
		assert.NotContains(t, kv.Value.Emit(), "hunter2", "attribute %s", kv.Key)
	}
}

func attrsOf(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value.Emit()
	}
	return out
}

// The node, agent-turn and tool-call spans end a failure with a code: the
// class as error.type and as the status description, and nothing of the
// error's text. A cancellation is error.type=canceled with no error status.
func TestAISpans_EndAFailureWithACodeNeverItsText(t *testing.T) {
	rec := recordAISpans(t)
	scope := RunScope{ChatID: "chat-1", OrgID: "org-1", RunID: "run-1", NodeID: "call_llm", NodeType: "call_llm"}
	ctx := WithRunScope(context.Background(), scope)

	for name, tc := range map[string]struct {
		end        func(error)
		err        error
		wantType   string
		wantStatus codes.Code
	}{
		"a node that failed": {
			end:      func(err error) { _, span := StartNodeSpan(ctx); EndAISpan(span, err) },
			err:      errSecret,
			wantType: ErrorTypeError, wantStatus: codes.Error,
		},
		"a node that timed out": {
			end:      func(err error) { _, span := StartNodeSpan(ctx); EndAISpan(span, err) },
			err:      fmt.Errorf("%w: %w", context.DeadlineExceeded, errSecret),
			wantType: ErrorTypeTimeout, wantStatus: codes.Error,
		},
		"a node that was canceled": {
			end:      func(err error) { _, span := StartNodeSpan(ctx); EndAISpan(span, err) },
			err:      fmt.Errorf("%w: %w", context.Canceled, errSecret),
			wantType: ErrorTypeCanceled, wantStatus: codes.Unset,
		},
		"a turn its caller classed": {
			end:      func(err error) { _, turn := StartAgentTurn(ctx); turn.EndAs(err, ErrorTypeContextOverflow) },
			err:      errSecret,
			wantType: ErrorTypeContextOverflow, wantStatus: codes.Error,
		},
		"a turn classed outside the vocabulary": {
			end:      func(err error) { _, turn := StartAgentTurn(ctx); turn.EndAs(err, "hunter2 leaked") },
			err:      errSecret,
			wantType: ErrorTypeError, wantStatus: codes.Error,
		},
		"a turn with no class": {
			end:      func(err error) { _, turn := StartAgentTurn(ctx); turn.End(err) },
			err:      errSecret,
			wantType: ErrorTypeError, wantStatus: codes.Error,
		},
		"a tool call that could not run": {
			end: func(err error) {
				_, call := StartToolCall(ctx, "grep", "tc-1", ToolLocationDaemon, "")
				call.End(false, err)
			},
			err:      errSecret,
			wantType: ErrorTypeError, wantStatus: codes.Error,
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec.Reset()
			tc.end(tc.err)
			require.Len(t, rec.Ended(), 1)
			s := rec.Ended()[0]
			a := attrsOf(s)
			assert.Equal(t, tc.wantType, a[AttrErrorType])
			assert.Equal(t, tc.wantStatus, s.Status().Code)
			if tc.wantStatus == codes.Error {
				assert.Equal(t, tc.wantType, s.Status().Description, "the status description is the code")
			} else {
				assert.Empty(t, s.Status().Description)
			}
			assert.Equal(t, "org-1", a[AttrOrgID])
			assertNoErrorText(t, s, tc.err)
		})
	}
}

// A tool that ran and reported an error is the tool's outcome: tool_error,
// and the span's status untouched.
func TestToolCall_AToolErrorIsNotAFailedCall(t *testing.T) {
	rec := recordAISpans(t)
	_, call := StartToolCall(context.Background(), "grep", "tc-1", ToolLocationDaemon, "")
	call.End(true, nil)
	require.Len(t, rec.Ended(), 1)
	s := rec.Ended()[0]
	assert.Equal(t, ErrorTypeToolError, attrsOf(s)[AttrErrorType])
	assert.Equal(t, codes.Unset, s.Status().Code)
}

// The node span names the chat as gen_ai.conversation.id beside
// reliant.chat.id, so a per-chat read keyed on the conversation finds it; it
// names no agent (a node is not one).
func TestStartNodeSpan_NamesTheConversation(t *testing.T) {
	rec := recordAISpans(t)
	ctx := WithRunScope(context.Background(), RunScope{ChatID: "chat-1", ThreadID: "thread-1", OrgID: "org-1", NodeID: "build", NodeType: "run"})
	_, span := StartNodeSpan(ctx)
	EndAISpan(span, nil)
	require.Len(t, rec.Ended(), 1)
	a := attrsOf(rec.Ended()[0])
	assert.Equal(t, "chat-1", a[AttrGenAIConversationID])
	assert.Equal(t, "chat-1", a[AttrChatID])
	assert.Equal(t, "org-1", a[AttrOrgID])
	assert.NotContains(t, a, attribute.Key(AttrGenAIAgentID))
	assert.NotContains(t, a, attribute.Key(AttrGenAIAgentName))

	rec.Reset()
	_, span = StartNodeSpan(WithRunScope(context.Background(), RunScope{NodeID: "build", NodeType: "run"}))
	EndAISpan(span, nil)
	assert.NotContains(t, attrsOf(rec.Ended()[0]), attribute.Key(AttrGenAIConversationID), "no chat, no conversation")
}

// The vocabulary is closed: its named values, and http_<status> for a status
// of 400-599 only.
func TestIsErrorType(t *testing.T) {
	t.Parallel()
	for _, v := range []string{ErrorTypeCanceled, ErrorTypeTimeout, ErrorTypeToolError, ErrorTypeError,
		ErrorTypeContextOverflow, ErrorTypeCreditExhausted, ErrorTypePromptRefused, ErrorTypeStreamStalled,
		ErrorTypeStreamEndedEarly, ErrorTypeMalformedOutput, ErrorTypeEmptyInput, ErrorTypeNoProvider,
		"http_400", "http_429", "http_599"} {
		assert.True(t, IsErrorType(v), v)
	}
	for _, v := range []string{"", "Error", "http_200", "http_600", "http_04", "http_0429", "http_", "http_42x", "my password"} {
		assert.False(t, IsErrorType(v), v)
	}
	assert.Equal(t, "http_429", ErrorTypeHTTP(429))
	assert.Equal(t, ErrorTypeError, ErrorTypeHTTP(200))
	assert.True(t, strings.HasPrefix(ErrorTypeHTTP(503), "http_"))
}
