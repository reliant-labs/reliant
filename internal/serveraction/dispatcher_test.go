// Copyright (c) 2025 Reliant Labs
package serveraction

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

const echo = "reliant/test.echo@1"

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := NewCatalog(
		Spec{Name: echo, Description: "echoes its params", Params: map[string]any{
			"type": "object", "required": []any{"text"}, "additionalProperties": false,
			"properties": map[string]any{"text": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"}},
		}},
		Spec{Name: "reliant/test.unbound@1", Description: "declared, never bound"},
	)
	require.NoError(t, err)
	return c
}

type fakeResolver struct {
	calls      int
	principal  Principal
	provenance Provenance
	err        error
	gotInv     Invocation
	gotRun     RunFacts
	// noOwner answers a run with no recorded owner; otherwise an empty
	// principal is filled with one.
	noOwner bool
}

func (r *fakeResolver) Resolve(_ context.Context, inv Invocation, run RunFacts) (Principal, Provenance, error) {
	r.calls++
	r.gotInv, r.gotRun = inv, run
	principal := r.principal
	if principal.UserID == "" && !r.noOwner {
		principal.UserID = "owner"
	}
	return principal, r.provenance, r.err
}

func echoCall() Call {
	return Call{
		Uses:   echo,
		Node:   &reliantv1.Node{Id: "say", Type: "action"},
		Params: map[string]any{"text": "hi"},
		Invocation: Invocation{RunID: "run-1", NodeID: "say", NodePath: "say", PositionKey: "run-1:say",
			ExecutionKey: "run-1:say/t/5", Attempt: 1},
		Run: RunFacts{WorkflowRef: "builtin://pr", Trigger: "github", CatalogPin: "pin-1", ActingIdentity: "app"},
	}
}

func TestSwitchOffRefusesEveryCallBeforeAnythingRuns(t *testing.T) {
	ran := 0
	resolver := &fakeResolver{}
	d, err := NewDispatcher(Config{Enabled: false, Catalog: testCatalog(t), Resolver: resolver,
		Handlers: map[string]Handler{echo: HandlerFunc(func(context.Context, *Request) (*Result, error) { ran++; return &Result{}, nil })}})
	require.NoError(t, err)
	for _, uses := range []string{echo, "reliant/nope.nope@1", "reliant/Malformed"} {
		call := echoCall()
		call.Uses = uses
		out := d.Dispatch(context.Background(), call)
		require.NotNil(t, out.Err, uses)
		assert.Equal(t, CodeFailedPrecondition, out.Err.Code)
		assert.Contains(t, out.Err.Message, "pipeline.enabled")
	}
	assert.Zero(t, ran, "no handler runs while the switch is off")
	assert.Zero(t, resolver.calls, "nothing about the run is read while the switch is off")

	var none *Dispatcher
	out := none.Dispatch(context.Background(), echoCall())
	require.NotNil(t, out.Err)
	assert.Equal(t, CodeFailedPrecondition, out.Err.Code)
	assert.Contains(t, out.Err.Message, "pipeline.enabled")
}

func TestDispatchPassesTheRunContextThrough(t *testing.T) {
	resolver := &fakeResolver{
		principal:  Principal{UserID: "u", OrgID: "o", ProjectID: "p", ActingIdentity: "app"},
		provenance: Provenance{WorkflowRef: "builtin://pr", Source: SourceBuiltin, Trigger: "github"},
	}
	var got *Request
	d, err := NewDispatcher(Config{Enabled: true, Catalog: testCatalog(t), Resolver: resolver,
		Handlers: map[string]Handler{echo: HandlerFunc(func(_ context.Context, req *Request) (*Result, error) {
			got = req
			return &Result{Content: "said " + req.Params["text"].(string), Data: map[string]any{"text": req.Params["text"]}}, nil
		})}})
	require.NoError(t, err)

	call := echoCall()
	out := d.Dispatch(context.Background(), call)
	require.Nil(t, out.Err)
	assert.Equal(t, "said hi", out.Result.Content)
	assert.Equal(t, map[string]any{"text": "hi"}, out.Result.Data)

	require.NotNil(t, got)
	assert.Equal(t, echo, got.Name)
	assert.Equal(t, "echoes its params", got.Spec.Description)
	assert.Same(t, call.Node, got.Node, "the handler reads the node's own definition")
	assert.Equal(t, call.Invocation, got.Invocation)
	assert.Equal(t, resolver.principal, got.Principal)
	assert.Equal(t, resolver.provenance, got.Provenance)
	assert.Equal(t, call.Invocation, resolver.gotInv)
	assert.Equal(t, call.Run, resolver.gotRun)
}

func TestDispatchRefusals(t *testing.T) {
	handler := HandlerFunc(func(context.Context, *Request) (*Result, error) { return nil, nil })
	d, err := NewDispatcher(Config{Enabled: true, Catalog: testCatalog(t), Resolver: &fakeResolver{},
		Handlers: map[string]Handler{echo: handler}})
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		mutate func(*Call)
		code   string
		says   string
	}{
		{"unknown", func(c *Call) { c.Uses = "reliant/test.nope@1" }, CodeInvalidArgument, "unknown server action"},
		{"malformed", func(c *Call) { c.Uses = "reliant/Test.echo@1" }, CodeInvalidArgument, "lowercase"},
		{"declared but not bound here", func(c *Call) { c.Uses = "reliant/test.unbound@1" }, CodeFailedPrecondition, "not available on this server"},
		{"missing param", func(c *Call) { c.Params = nil }, CodeInvalidArgument, "text"},
		{"unknown param", func(c *Call) { c.Params["bogus"] = 1 }, CodeInvalidArgument, "bogus"},
		{"wrong type", func(c *Call) { c.Params["n"] = "seven" }, CodeInvalidArgument, "n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := echoCall()
			tc.mutate(&call)
			out := d.Dispatch(context.Background(), call)
			require.NotNil(t, out.Err)
			assert.Equal(t, tc.code, out.Err.Code)
			assert.Contains(t, out.Err.Message, tc.says)
		})
	}

	ok := echoCall()
	ok.Params["n"] = float64(3) // a structpb number
	assert.Nil(t, d.Dispatch(context.Background(), ok).Err, "a JSON number with an integral value is an integer")

	noResolver, err := NewDispatcher(Config{Enabled: true, Catalog: testCatalog(t), Handlers: map[string]Handler{echo: handler}})
	require.NoError(t, err)
	out := noResolver.Dispatch(context.Background(), echoCall())
	require.NotNil(t, out.Err)
	assert.Equal(t, CodeFailedPrecondition, out.Err.Code, "with no way to establish the principal nothing runs")
}

func TestDispatchNeverLeaksInternalErrors(t *testing.T) {
	const secret = "pq: password authentication failed for user cp_admin at 10.0.0.7"
	for _, tc := range []struct {
		name     string
		resolver *fakeResolver
		handler  HandlerFunc
		code     string
	}{
		{"resolver", &fakeResolver{err: errors.New(secret)}, func(context.Context, *Request) (*Result, error) { return nil, nil }, CodeUnavailable},
		{"handler error", &fakeResolver{}, func(context.Context, *Request) (*Result, error) { return nil, errors.New(secret) }, CodeInternal},
		{"handler panic", &fakeResolver{}, func(context.Context, *Request) (*Result, error) { panic(secret) }, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDispatcher(Config{Enabled: true, Catalog: testCatalog(t), Resolver: tc.resolver,
				Handlers: map[string]Handler{echo: tc.handler}})
			require.NoError(t, err)
			out := d.Dispatch(context.Background(), echoCall())
			require.NotNil(t, out.Err)
			assert.Equal(t, tc.code, out.Err.Code)
			assert.NotContains(t, out.Err.Message, "password")
			assert.NotContains(t, out.Err.Message, "10.0.0.7")
			assert.Contains(t, out.Err.Message, echo)
		})
	}
}

func TestDispatchPublishesAHandlersOwnError(t *testing.T) {
	d, err := NewDispatcher(Config{Enabled: true, Catalog: testCatalog(t), Resolver: &fakeResolver{},
		Handlers: map[string]Handler{echo: HandlerFunc(func(context.Context, *Request) (*Result, error) {
			return nil, &Error{Code: CodeAborted, Message: "the PR's head moved; decide again", Retryable: true}
		})}})
	require.NoError(t, err)
	out := d.Dispatch(context.Background(), echoCall())
	require.NotNil(t, out.Err)
	assert.Equal(t, &Error{Code: CodeAborted, Message: "the PR's head moved; decide again", Retryable: true}, out.Err)
}

func TestDispatchRefusesARunWithNoOwner(t *testing.T) {
	ran := false
	d, err := NewDispatcher(Config{Enabled: true, Catalog: testCatalog(t), Resolver: &fakeResolver{noOwner: true},
		Handlers: map[string]Handler{echo: HandlerFunc(func(context.Context, *Request) (*Result, error) { ran = true; return nil, nil })}})
	require.NoError(t, err)
	out := d.Dispatch(context.Background(), echoCall())
	require.NotNil(t, out.Err)
	assert.Equal(t, CodeFailedPrecondition, out.Err.Code)
	assert.Contains(t, out.Err.Message, "owner")
	assert.False(t, ran)
}

func TestNewDispatcherRefusesHandlersForUndeclaredActions(t *testing.T) {
	h := HandlerFunc(func(context.Context, *Request) (*Result, error) { return nil, nil })
	_, err := NewDispatcher(Config{Catalog: testCatalog(t), Handlers: map[string]Handler{"reliant/test.nope@1": h}})
	assert.ErrorIs(t, err, ErrUnknown)
	_, err = NewDispatcher(Config{Catalog: testCatalog(t), Handlers: map[string]Handler{echo: nil}})
	assert.Error(t, err)
	d, err := NewDispatcher(Config{})
	require.NoError(t, err, "the builtin catalog with nothing bound is a valid, inert dispatcher")
	assert.False(t, d.Enabled())
}
