package handlers

import (
	"context"
	"errors"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
	"google.golang.org/protobuf/types/known/structpb"
)

// HostedDisabledError is an executor's refusal that is safe to publish: hosted
// HTTP is switched off on this worker for a stated configuration reason. It is
// the one executor error executeHosted passes through to the run's history;
// every other cause (remote, storage) stays generic.
type HostedDisabledError struct{ Reason string }

func (e *HostedDisabledError) Error() string { return "hosted HTTP disabled: " + e.Reason }

// executeHosted is deliberately not a CredentialSource: tools cannot discover
// or use hosted grants through the shared saved-connection path.
func (a *ActionActivity) executeHosted(ctx context.Context, uses, grant string, params map[string]any, rtx RuntimeContext) *reliantv1.ActionOutput {
	out := &reliantv1.ActionOutput{Uses: uses}
	fail := func(code, message string) *reliantv1.ActionOutput {
		out.IsError = true
		out.ErrorCode = code
		out.Content = message
		return out
	}
	if strings.TrimSpace(grant) == "" {
		return fail("invalid_argument", "hosted HTTP use grant must not be empty")
	}
	if uses != "http/request@1" {
		return fail("invalid_argument", "hosted use grants are only accepted by fixed http/request@1 actions")
	}
	// Headers are supplied by the controller, never from a template. Do not
	// silently ignore conflicting auth or transport options.
	for name := range params {
		switch name {
		case "url", "method", "body":
		default:
			return fail("invalid_argument", "hosted HTTP actions accept only url, method and body; connection and headers are not permitted")
		}
	}
	if a.hosted == nil {
		return fail("failed_precondition", "hosted HTTP execution is not configured")
	}
	if rtx.WorkflowID == "" || rtx.StepID == "" {
		return fail("failed_precondition", "hosted HTTP execution requires a fixed workflow action")
	}
	rawURL, ok := params["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return fail("invalid_argument", "hosted HTTP action requires a URL")
	}
	method := "GET"
	if raw, exists := params["method"]; exists {
		var valid bool
		method, valid = raw.(string)
		if !valid {
			return fail("invalid_argument", "hosted HTTP action method must be a string")
		}
	}
	result, err := a.hosted.ExecuteAction(ctx, rtx.WorkflowID, rtx.StepID, grant, hostedhttp.Request{URL: rawURL, Method: method, Body: params["body"]})
	// Remote/storage errors may carry internal details. The executor's own
	// metadata-only audit records the precise rejection; history gets no cause,
	// except a HostedDisabledError, which names a configuration reason only.
	var disabled *HostedDisabledError
	if errors.As(err, &disabled) {
		return fail("failed_precondition", disabled.Error())
	}
	if err != nil || result == nil {
		return fail("failed_precondition", "hosted HTTP execution was refused or could not be completed")
	}
	out.Content = result.Content
	out.StatusCode = int32(result.StatusCode)
	out.IsError = result.IsError
	out.Retryable = result.Retryable
	if result.Data != nil {
		data, err := structpb.NewStruct(result.Data)
		if err != nil {
			return fail("internal", "hosted HTTP returned an invalid result")
		}
		out.Data = data
	}
	return out
}
