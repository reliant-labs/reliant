package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

type hostedFake struct {
	calls                  int
	runID, nodeID, grantID string
	req                    hostedhttp.Request
	err                    error
}

func (f *hostedFake) ExecuteAction(_ context.Context, run, node, grant string, req hostedhttp.Request) (*hostedhttp.Result, error) {
	f.calls++
	f.runID = run
	f.nodeID = node
	f.grantID = grant
	f.req = req
	return &hostedhttp.Result{StatusCode: 200, Content: "ok", Data: map[string]any{"json": map[string]any{"ready": true}}}, f.err
}
func TestHostedActionRejectsConnectionHeadersAndOtherActions(t *testing.T) {
	for _, extra := range []string{"connection", "headers", "query", "follow_redirects", "timeout"} {
		t.Run(extra, func(t *testing.T) {
			f := &hostedFake{}
			a := NewActionActivity(nil).WithHostedExecutor(f)
			out := a.executeHosted(context.Background(), "http/request@1", "grant", map[string]any{"url": "https://example.com/rpc", extra: "bad"}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
			require.True(t, out.IsError)
			require.Zero(t, f.calls)
		})
	}
	f := &hostedFake{}
	a := NewActionActivity(nil).WithHostedExecutor(f)
	out := a.executeHosted(context.Background(), "github/issue.create@1", "grant", nil, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
	require.True(t, out.IsError)
	require.Zero(t, f.calls)
}
func TestHostedActionMapsSafeOutputAndFixedIdentity(t *testing.T) {
	f := &hostedFake{}
	a := NewActionActivity(nil).WithHostedExecutor(f)
	out := a.executeHosted(context.Background(), "http/request@1", "grant", map[string]any{"url": "https://example.com/rpc", "method": "POST", "body": map[string]any{"proposal": true}}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
	require.False(t, out.IsError)
	require.Equal(t, int32(200), out.StatusCode)
	require.Equal(t, "run", f.runID)
	require.Equal(t, "fixed", f.nodeID)
	require.Equal(t, "grant", f.grantID)
	require.Equal(t, "POST", f.req.Method)
	require.Equal(t, map[string]any{"json": map[string]any{"ready": true}}, out.Data.AsMap())
}

// publicErr stands in for hosteduse.SecretRefusedError.
type publicErr struct{ msg string }

func (e *publicErr) Error() string         { return "internal: " + e.msg }
func (e *publicErr) PublicRefusal() string { return e.msg }

// A refusal the executor wrote for the user (control-plane#2765: the workflow's
// secret is missing or no longer granted) reaches the run's history; every
// other executor error stays generic, even one marked as a user error.
func TestHostedActionShowsPublicRefusal(t *testing.T) {
	const generic = "hosted HTTP execution was refused or could not be completed"
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"public refusal":   {fmt.Errorf("execute: %w", &publicErr{"re-save the secret"}), "re-save the secret"},
		"plain error":      {errors.New("internal detail"), generic},
		"user-class error": {svcerr.WithClass(errors.New("internal detail"), svcerr.ClassUser), generic},
	} {
		t.Run(name, func(t *testing.T) {
			a := NewActionActivity(nil).WithHostedExecutor(&hostedFake{err: tc.err})
			out := a.executeHosted(context.Background(), "http/request@1", "grant", map[string]any{"url": "https://example.com/rpc"}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
			require.True(t, out.IsError)
			require.Equal(t, "failed_precondition", out.ErrorCode)
			require.Equal(t, tc.want, out.Content)
		})
	}
}

func TestHostedActionEmptyGrantNeverFallsBack(t *testing.T) {
	f := &hostedFake{}
	a := NewActionActivity(nil).WithHostedExecutor(f)
	out := a.executeHosted(context.Background(), "http/request@1", "", map[string]any{"url": "https://example.com/rpc"}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
	require.True(t, out.IsError)
	require.Zero(t, f.calls)
}

func TestHostedActionPublishesOnlyTheDisabledReason(t *testing.T) {
	f := &hostedFake{err: &HostedDisabledError{Reason: "control-plane URL is not HTTPS"}}
	out := NewActionActivity(nil).WithHostedExecutor(f).executeHosted(context.Background(), "http/request@1", "grant", map[string]any{"url": "https://example.com/rpc"}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
	require.True(t, out.IsError)
	require.Equal(t, "failed_precondition", out.ErrorCode)
	require.Equal(t, "hosted HTTP disabled: control-plane URL is not HTTPS", out.Content)
}

func TestHostedActionFailsClosedAndDoesNotPublishRemoteError(t *testing.T) {
	a := NewActionActivity(nil)
	out := a.executeHosted(context.Background(), "http/request@1", "grant", map[string]any{"url": "https://example.com/rpc"}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
	require.True(t, out.IsError)
	f := &hostedFake{err: errors.New("remote secret must never enter history")}
	out = a.WithHostedExecutor(f).executeHosted(context.Background(), "http/request@1", "grant", map[string]any{"url": "https://example.com/rpc"}, RuntimeContext{WorkflowID: "run", StepID: "fixed"})
	require.True(t, out.IsError)
	require.NotContains(t, out.Content, "remote secret")
}
