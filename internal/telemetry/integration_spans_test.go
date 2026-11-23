// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recordSpans swaps the global tracer provider for one that records, so the
// tests that use it do not run in parallel.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

// The span is named by the integration and its action, is a client span under
// whatever span ctx carries, and names the run it was made in with the GenAI
// names the AI dashboards read: org.id and the chat as gen_ai.conversation.id.
func TestStartIntegrationCall_NamesTheCallAndItsRun(t *testing.T) {
	rec := recordSpans(t)
	ctx, parent := otel.Tracer("test").Start(context.Background(), "workflow.node merge")
	ctx = WithRunScope(ctx, RunScope{
		UserID: "user-1", ChatID: "chat-1", ThreadID: "thread-1", WorkflowID: "wf-1", OrgID: "org-1",
		WorkflowName: "pr-review", RunID: "run-1", NodeID: "merge", NodeType: "action",
	})

	_, call := StartIntegrationCall(ctx, "github", "pr.merge", "github/pr.merge@1", attribute.Bool(AttrIntegrationHosted, false))
	call.End(IntegrationOutcomeOK, "", attribute.Int(AttrHTTPResponseStatusCode, 200))
	parent.End()

	spans := rec.Ended()
	require.Len(t, spans, 2)
	s := spans[0]
	assert.Equal(t, "integration.call github.pr.merge", s.Name())
	assert.Equal(t, trace.SpanKindClient, s.SpanKind())
	assert.Equal(t, parent.SpanContext().SpanID(), s.Parent().SpanID())
	assert.Equal(t, codes.Unset, s.Status().Code)
	got := attrMap(s.Attributes())
	for k, want := range map[string]string{
		AttrIntegrationID:          "github",
		AttrIntegrationOperation:   "pr.merge",
		AttrIntegrationUses:        "github/pr.merge@1",
		AttrIntegrationOutcome:     "ok",
		AttrIntegrationHosted:      "false",
		AttrHTTPResponseStatusCode: "200",
		AttrOrgID:                  "org-1",
		AttrUserID:                 "user-1",
		AttrGenAIConversationID:    "chat-1",
		AttrGenAIAgentID:           "thread-1",
		AttrChatID:                 "chat-1",
		AttrAIRunID:                "run-1",
		AttrAIRootRunID:            "run-1",
		AttrAIWorkflowName:         "pr-review",
		AttrAINodeID:               "merge",
		AttrAINodeType:             "action",
	} {
		assert.Equal(t, want, got[k], k)
	}
	assert.NotContains(t, got, AttrIntegrationErrorCode)
	assert.NotContains(t, got, AttrErrorType)
}

// A call made outside a run carries only what its caller put in the scope;
// with nothing there, the span still opens and names the call.
func TestStartIntegrationCall_OutsideARun(t *testing.T) {
	rec := recordSpans(t)
	_, call := StartIntegrationCall(context.Background(), "http", "request", "")
	call.End(IntegrationOutcomeOK, "")

	spans := rec.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "integration.call http.request", spans[0].Name())
	assert.False(t, spans[0].Parent().IsValid())
	got := attrMap(spans[0].Attributes())
	assert.NotContains(t, got, AttrIntegrationUses)
	assert.NotContains(t, got, AttrOrgID)
}

// How a call ended is its outcome and code; the span's status says Error for
// a failure, described by the code alone, and records no error event. A
// cancellation is not a failure.
func TestIntegrationCallEnd_OutcomeMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		outcome, code   string
		wantStatus      codes.Code
		wantDescription string
		wantErrorType   string
	}{
		"ok":                         {outcome: IntegrationOutcomeOK, wantStatus: codes.Unset},
		"canceled":                   {outcome: IntegrationOutcomeCanceled, wantStatus: codes.Unset, wantErrorType: ErrorTypeCanceled},
		"an answered error":          {outcome: IntegrationOutcomeError, code: "http_404", wantStatus: codes.Error, wantDescription: "http_404", wantErrorType: "http_404"},
		"a credential error":         {outcome: IntegrationOutcomeCredentialError, code: "needs_reauth", wantStatus: codes.Error, wantDescription: "needs_reauth", wantErrorType: "needs_reauth"},
		"a refusal":                  {outcome: IntegrationOutcomeRefused, code: "integration_not_allowed", wantStatus: codes.Error, wantDescription: "integration_not_allowed", wantErrorType: "integration_not_allowed"},
		"a failure with no code":     {outcome: IntegrationOutcomeFailed, wantStatus: codes.Error, wantDescription: "failed", wantErrorType: "failed"},
		"a failure with a code (go)": {outcome: IntegrationOutcomeFailed, code: "timeout", wantStatus: codes.Error, wantDescription: "timeout", wantErrorType: "timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := recordSpans(t)
			_, call := StartIntegrationCall(context.Background(), "github", "pr.get", "")
			call.End(tc.outcome, tc.code)
			spans := rec.Ended()
			require.Len(t, spans, 1)
			s := spans[0]
			assert.Equal(t, tc.wantStatus, s.Status().Code)
			assert.Equal(t, tc.wantDescription, s.Status().Description)
			assert.Empty(t, s.Events(), "a call's span records no error event: its text can carry the response")
			got := attrMap(s.Attributes())
			assert.Equal(t, tc.outcome, got[AttrIntegrationOutcome])
			if tc.code == "" {
				assert.NotContains(t, got, AttrIntegrationErrorCode)
			} else {
				assert.Equal(t, tc.code, got[AttrIntegrationErrorCode])
			}
			if tc.wantErrorType == "" {
				assert.NotContains(t, got, AttrErrorType)
			} else {
				assert.Equal(t, tc.wantErrorType, got[AttrErrorType])
			}
		})
	}
}

func TestIntegrationCallEnd_NilIsANoOp(t *testing.T) {
	var call *IntegrationCall
	call.End(IntegrationOutcomeOK, "")
}

func TestIntegrationAnswerOutcome(t *testing.T) {
	for _, tc := range []struct {
		isError       bool
		status        int
		outcome, code string
	}{
		{false, 200, IntegrationOutcomeOK, ""},
		{false, 302, IntegrationOutcomeOK, ""}, // a redirect the caller asked to see
		{true, 404, IntegrationOutcomeError, "http_404"},
		{true, 429, IntegrationOutcomeError, "http_429"},
		{true, 503, IntegrationOutcomeError, "http_503"},
		{true, 200, IntegrationOutcomeError, "provider_error"}, // Slack's ok:false
		{true, 0, IntegrationOutcomeError, "provider_error"},
	} {
		outcome, code := IntegrationAnswerOutcome(tc.isError, tc.status)
		assert.Equal(t, tc.outcome, outcome, "%v/%d", tc.isError, tc.status)
		assert.Equal(t, tc.code, code, "%v/%d", tc.isError, tc.status)
	}
}
