package httpaction

import (
	"context"
	"errors"
	"net"
	"strconv"

	"go.opentelemetry.io/otel/attribute"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// traced runs call under the call's "integration.call <integration>.<action>"
// span. The span sits here, at the runner, rather than in each caller, so the
// action node, the agent's integration tools and the workflow checks all get
// it from one place; the workflow, node and run come from ctx's run scope.
func traced(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, call func(context.Context) (*Result, error)) (*Result, error) {
	ctx, span := telemetry.StartIntegrationCall(ctx, m.GetId(), a.GetId(), usesOf(m, a))
	res, err := call(ctx)
	outcome, code := callOutcome(ctx, res, err)
	span.End(outcome, code, resultAttributes(res)...)
	return res, err
}

// RunOrgSource is a CredentialSource that can say which org a run belongs to
// (connauth's is one): its recorded org, else its chat's, "" for none.
type RunOrgSource interface {
	RunOrgOf(ctx context.Context, runID string) (string, error)
}

// WithCallOrg is ctx with the org of runID in its run scope, for the
// integration.call span of a call made in that run. A worker's run scope
// already names the org of the run's chat (observability's run-scope
// interceptor), so src is asked only when it names none: a run whose org is
// recorded on the run rather than on its chat. ctx is returned as it is when
// there is no run, src cannot tell, or the run belongs to no org; a failed
// lookup never fails the call, it only leaves the span without an org.
func WithCallOrg(ctx context.Context, src CredentialSource, runID string) context.Context {
	if runID == "" || telemetry.RunScopeFromContext(ctx).OrgID != "" {
		return ctx
	}
	orgs, ok := src.(RunOrgSource)
	if !ok {
		return ctx
	}
	org, err := orgs.RunOrgOf(ctx, runID)
	if err != nil || org == "" {
		return ctx
	}
	return telemetry.WithRunScope(ctx, telemetry.RunScope{OrgID: org})
}

// usesOf is the reference a workflow names the action by:
// "<integration>/<action>@<major>". "" for a manifest without a version.
func usesOf(m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec) string {
	if m.GetVersion() < 1 {
		return ""
	}
	return m.GetId() + "/" + a.GetId() + "@" + strconv.Itoa(int(m.GetVersion()))
}

// callOutcome classifies how a call ended (telemetry.IntegrationOutcome*),
// with the failure's code. It reads only types and codes, never an error's
// text, which can carry what the provider said.
func callOutcome(ctx context.Context, res *Result, err error) (outcome, code string) {
	if err == nil {
		if res == nil {
			return telemetry.IntegrationOutcomeOK, ""
		}
		return telemetry.IntegrationAnswerOutcome(res.IsError, res.StatusCode)
	}
	var ce *CredentialError
	if errors.As(err, &ce) {
		// A Reason names a refusal (the org's policy does not allow the
		// integration); a bare code is a credential that could not be had.
		if ce.Reason != "" {
			return telemetry.IntegrationOutcomeRefused, ce.Reason
		}
		return telemetry.IntegrationOutcomeCredentialError, ce.Code
	}
	// An error that went through a credential's scrub is text only, so the
	// caller's context is asked too.
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return telemetry.IntegrationOutcomeCanceled, ""
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout():
		return telemetry.IntegrationOutcomeFailed, "timeout"
	}
	return telemetry.IntegrationOutcomeFailed, "request_failed"
}

// resultAttributes are what a result may put on the call's span: its status,
// host, connection id, page count and rate limit. Never its content or data.
func resultAttributes(res *Result) []attribute.KeyValue {
	if res == nil {
		return nil
	}
	attrs := []attribute.KeyValue{attribute.Bool(telemetry.AttrIntegrationTruncated, res.Truncated)}
	if res.StatusCode != 0 {
		attrs = append(attrs, attribute.Int(telemetry.AttrHTTPResponseStatusCode, res.StatusCode))
	}
	if res.IsError {
		attrs = append(attrs, attribute.Bool(telemetry.AttrIntegrationRetryable, res.Retryable))
	}
	if res.Host != "" {
		attrs = append(attrs, attribute.String(telemetry.AttrServerAddress, res.Host))
	}
	if res.ConnectionID != "" {
		attrs = append(attrs, attribute.String(telemetry.AttrIntegrationConnectionID, res.ConnectionID))
	}
	if res.Pages > 0 {
		attrs = append(attrs, attribute.Int(telemetry.AttrIntegrationPages, res.Pages))
	}
	return append(attrs, rateLimitAttributes(res.RateLimit)...)
}

func rateLimitAttributes(rl *RateLimit) []attribute.KeyValue {
	if rl == nil {
		return nil
	}
	var attrs []attribute.KeyValue
	count := func(name string, v *int64) {
		if v != nil {
			attrs = append(attrs, attribute.Int64(name, *v))
		}
	}
	count(telemetry.AttrIntegrationRateLimitLimit, rl.Limit)
	count(telemetry.AttrIntegrationRateLimitRemaining, rl.Remaining)
	count(telemetry.AttrIntegrationRateLimitUsed, rl.Used)
	if rl.Reset != nil {
		attrs = append(attrs, attribute.Int64(telemetry.AttrIntegrationRateLimitReset, rl.Reset.Unix()))
	}
	if rl.RetryAfter != nil {
		attrs = append(attrs, attribute.Int64(telemetry.AttrIntegrationRateLimitRetryAfter, int64(rl.RetryAfter.Seconds())))
	}
	if rl.Resource != "" {
		attrs = append(attrs, attribute.String(telemetry.AttrIntegrationRateLimitResource, rl.Resource))
	}
	return attrs
}
