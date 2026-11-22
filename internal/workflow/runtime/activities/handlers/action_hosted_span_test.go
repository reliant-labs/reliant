package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

// A hosted_use call is made by the controller, not the worker's runner, so the
// action node opens its integration.call span itself. These tests swap the
// global tracer provider, so none runs in parallel.

const hostedBodyMarker = "hosted-body-marker-3c1"

// hostedReply answers every hosted call with result and err.
type hostedReply struct {
	result *hostedhttp.Result
	err    error
}

func (h *hostedReply) ExecuteAction(context.Context, string, string, string, hostedhttp.Request) (*hostedhttp.Result, error) {
	return h.result, h.err
}

// runOrgCreds is a credential source that knows the org of a run; a hosted
// call never asks it for a credential.
type runOrgCreds struct{ org string }

func (runOrgCreds) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return nil, errors.New("a hosted call must not resolve a credential")
}

func (c runOrgCreds) RunOrgOf(context.Context, string) (string, error) { return c.org, nil }

func hostedNode() *reliantv1.Node {
	literal := func(s string) *reliantv1.CelString {
		return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
	}
	return &reliantv1.Node{Id: "notify", Type: "action", Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses: literal("http/request@1"), HostedUse: literal("grant"),
		With: map[string]*structpb.Value{
			"url":    structpb.NewStringValue("https://hooks.example.com/notify?token=" + hostedBodyMarker),
			"method": structpb.NewStringValue("POST"),
		},
	}}}
}

func TestHostedActionNodeOpensAnIntegrationCallSpan(t *testing.T) {
	for name, tc := range map[string]struct {
		reply      *hostedReply
		creds      httpaction.CredentialSource
		scopeOrg   string
		want       map[attribute.Key]string
		absent     []attribute.Key
		wantStatus codes.Code
		wantDesc   string
	}{
		"answered": {
			reply:    &hostedReply{result: &hostedhttp.Result{StatusCode: 200, Content: hostedBodyMarker}},
			scopeOrg: "org-1",
			want: map[attribute.Key]string{
				telemetry.AttrIntegrationOutcome:     "ok",
				telemetry.AttrHTTPResponseStatusCode: "200",
				telemetry.AttrOrgID:                  "org-1",
			},
			absent:     []attribute.Key{telemetry.AttrIntegrationRetryable, telemetry.AttrIntegrationErrorCode},
			wantStatus: codes.Unset,
		},
		"an answered failure": {
			reply:    &hostedReply{result: &hostedhttp.Result{StatusCode: 502, IsError: true, Retryable: true, Content: hostedBodyMarker}},
			scopeOrg: "org-1",
			want: map[attribute.Key]string{
				telemetry.AttrIntegrationOutcome:     "error",
				telemetry.AttrIntegrationErrorCode:   "http_502",
				telemetry.AttrIntegrationRetryable:   "true",
				telemetry.AttrHTTPResponseStatusCode: "502",
			},
			wantStatus: codes.Error, wantDesc: "http_502",
		},
		"refused by the controller": {
			reply:    &hostedReply{err: &publicErr{"re-save the secret " + hostedBodyMarker}},
			scopeOrg: "org-1",
			want: map[attribute.Key]string{
				telemetry.AttrIntegrationOutcome:   "refused",
				telemetry.AttrIntegrationErrorCode: "failed_precondition",
			},
			absent:     []attribute.Key{telemetry.AttrHTTPResponseStatusCode},
			wantStatus: codes.Error, wantDesc: "failed_precondition",
		},
		"a result that would not convert": {
			reply:    &hostedReply{result: &hostedhttp.Result{Data: map[string]any{"bad": make(chan int)}}},
			scopeOrg: "org-1",
			want: map[attribute.Key]string{
				telemetry.AttrIntegrationOutcome:   "failed",
				telemetry.AttrIntegrationErrorCode: "internal",
			},
			wantStatus: codes.Error, wantDesc: "internal",
		},
		"the run's org when the scope names none": {
			reply: &hostedReply{result: &hostedhttp.Result{StatusCode: 200}},
			creds: runOrgCreds{org: "org-run"},
			want:  map[attribute.Key]string{telemetry.AttrOrgID: "org-run"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := recordHandlerSpans(t)
			ctx, node := otel.Tracer("test").Start(context.Background(), "workflow.node notify")
			ctx = telemetry.WithRunScope(ctx, telemetry.RunScope{ChatID: "chat-1", WorkflowID: "run", OrgID: tc.scopeOrg,
				RunID: "run", NodeID: "notify", NodeType: "action"})
			a := NewActionActivity(tc.creds).WithHostedExecutor(tc.reply)

			_, err := a.Execute(ctx, types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run", StepID: "notify"}, Node: hostedNode()})
			require.NoError(t, err)
			node.End()

			var call []string
			for _, s := range rec.Ended() {
				if !strings.HasPrefix(s.Name(), telemetry.SpanNameIntegrationCall) {
					continue
				}
				call = append(call, s.Name())
				assert.Equal(t, "integration.call http.request", s.Name())
				assert.Equal(t, trace.SpanKindClient, s.SpanKind())
				assert.Equal(t, node.SpanContext().SpanID(), s.Parent().SpanID())
				assert.Equal(t, tc.wantStatus, s.Status().Code)
				assert.Equal(t, tc.wantDesc, s.Status().Description)
				assert.Empty(t, s.Events())
				got := attrsOf(s)
				for k, v := range got {
					assert.NotContains(t, v, hostedBodyMarker, "attribute %s carries the request or response", k)
				}
				want := map[attribute.Key]string{
					telemetry.AttrIntegrationID:        "http",
					telemetry.AttrIntegrationOperation: "request",
					telemetry.AttrIntegrationUses:      "http/request@1",
					telemetry.AttrIntegrationHosted:    "true",
					telemetry.AttrServerAddress:        "hooks.example.com",
					telemetry.AttrGenAIConversationID:  "chat-1",
					telemetry.AttrAINodeID:             "notify",
				}
				for k, v := range tc.want {
					want[k] = v
				}
				for k, v := range want {
					assert.Equal(t, v, got[k], k)
				}
				for _, k := range tc.absent {
					assert.NotContains(t, got, k)
				}
			}
			require.Len(t, call, 1)
		})
	}
}
