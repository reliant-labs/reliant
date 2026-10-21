// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
)

func newInputOptionsService(t *testing.T, io *StubInputOptions) *Service {
	t.Helper()
	deps := Deps{}
	if io != nil {
		deps.InputOptions = io
	}
	return NewTestCatalog(t, WithCatalogDeps(deps))
}

func listInputOptions(ctx context.Context, svc *Service, req *reliantv1.ListInputOptionsRequest) (*reliantv1.ListInputOptionsResponse, error) {
	resp, err := svc.ListInputOptions(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func codeOf(t *testing.T, err error) connect.Code {
	t.Helper()
	var ce *connect.Error
	require.True(t, errors.As(err, &ce), "want a connect error, got %v", err)
	return ce.Code()
}

func TestListInputOptions_ProjectsTheListingOntoTheWire(t *testing.T) {
	io := &StubInputOptions{ListFunc: func(_ context.Context, r inputoptions.ListRequest) (*inputoptions.ListResult, error) {
		return &inputoptions.ListResult{
			State: inputoptions.StateConnected, NextPageToken: "next",
			Options: []inputoptions.Option{{
				Value: "acme/app", Label: "acme/app", Description: "private", Group: "acme",
				Annotations: map[string]any{"can_push": false, "installation_id": 11.0},
				Warnings:    []string{"the bot cannot write here"},
			}},
		}, nil
	}}
	svc := newInputOptionsService(t, io)

	scope, err := structpb.NewValue("22")
	require.NoError(t, err)
	identity := reliantv1.ActingIdentity_ACTING_IDENTITY_APP
	resp, err := listInputOptions(asUser("alice"), svc, &reliantv1.ListInputOptionsRequest{
		Type: ptr("github.repository"), ConnectionId: ptr("conn-1"), Query: ptr("acme"), PageToken: "tok", PageSize: 25,
		Scope: map[string]*structpb.Value{"installation": scope}, ActingIdentity: &identity,
	})
	require.NoError(t, err)

	got := io.Calls("List")
	require.Len(t, got, 1)
	req := got[0].Args[1].(inputoptions.ListRequest)
	assert.Equal(t, inputoptions.ListRequest{
		UserID: "alice", Type: "github.repository", ConnectionID: "conn-1", Query: "acme", PageToken: "tok", PageSize: 25,
		Scope: map[string]any{"installation": "22"}, ActingIdentity: "app",
	}, req)

	assert.Equal(t, "next", resp.GetNextPageToken())
	assert.Equal(t, reliantv1.InputOptionsConnectionState_INPUT_OPTIONS_CONNECTION_STATE_CONNECTED, resp.GetConnectionState())
	require.Len(t, resp.GetOptions(), 1)
	o := resp.GetOptions()[0]
	assert.Equal(t, "acme/app", o.GetValue())
	assert.Equal(t, "private", o.GetDescription())
	assert.Equal(t, "acme", o.GetGroup())
	assert.Equal(t, []string{"the bot cannot write here"}, o.GetWarnings())
	assert.Equal(t, false, o.GetAnnotations()["can_push"].AsInterface())
	assert.Equal(t, 11.0, o.GetAnnotations()["installation_id"].AsInterface())
}

// The default acting identity is the owner.
func TestListInputOptions_DefaultsToTheOwnerIdentity(t *testing.T) {
	io := &StubInputOptions{ListFunc: func(context.Context, inputoptions.ListRequest) (*inputoptions.ListResult, error) {
		return &inputoptions.ListResult{State: inputoptions.StateConnected}, nil
	}}
	_, err := listInputOptions(asUser("alice"), newInputOptionsService(t, io), &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
	require.NoError(t, err)
	assert.Equal(t, "owner", io.Calls("List")[0].Args[1].(inputoptions.ListRequest).ActingIdentity)
}

func TestListInputOptions_NeedsConnection(t *testing.T) {
	io := &StubInputOptions{ListFunc: func(context.Context, inputoptions.ListRequest) (*inputoptions.ListResult, error) {
		return &inputoptions.ListResult{State: inputoptions.StateNeedsConnection}, nil
	}}
	resp, err := listInputOptions(asUser("alice"), newInputOptionsService(t, io), &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
	require.NoError(t, err)
	assert.Equal(t, reliantv1.InputOptionsConnectionState_INPUT_OPTIONS_CONNECTION_STATE_NEEDS_CONNECTION, resp.GetConnectionState())
	assert.Empty(t, resp.GetOptions())
}

func TestListInputOptions_ResolveBypassesListing(t *testing.T) {
	io := &StubInputOptions{ResolveFunc: func(_ context.Context, r inputoptions.ResolveRequest) (*inputoptions.Resolution, error) {
		return &inputoptions.Resolution{State: inputoptions.StateConnected, Options: []inputoptions.Option{{Value: "583231", Label: "octocat"}}}, nil
	}}
	resp, err := listInputOptions(asUser("alice"), newInputOptionsService(t, io), &reliantv1.ListInputOptionsRequest{
		Type: ptr("github.user"), Resolve: []string{"583231", "1"}, Query: ptr("ignored"), PageToken: "ignored",
	})
	require.NoError(t, err)
	assert.Empty(t, io.Calls("List"), "resolve does not list")
	got := io.Calls("Resolve")[0].Args[1].(inputoptions.ResolveRequest)
	assert.Equal(t, "alice", got.UserID)
	assert.Equal(t, []string{"583231", "1"}, got.Values)
	require.Len(t, resp.GetOptions(), 1)
	assert.Equal(t, "octocat", resp.GetOptions()[0].GetLabel())
}

// --- auth ---------------------------------------------------------------------

func TestListInputOptions_RequiresASignedInUser(t *testing.T) {
	io := &StubInputOptions{}
	svc := newInputOptionsService(t, io)
	_, err := listInputOptions(context.Background(), svc, &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
	assert.Equal(t, connect.CodeUnauthenticated, codeOf(t, err))
	_, err = listInputOptions(context.WithValue(context.Background(), auth.UserIDContextKey, ""), svc, &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
	assert.Equal(t, connect.CodeUnauthenticated, codeOf(t, err))
	assert.Empty(t, io.Calls("List"))
}

// The listing is the person's own view of their provider account: a machine
// credential (an agent's) is refused, however it is scoped.
func TestListInputOptions_RefusesAMachineCredential(t *testing.T) {
	io := &StubInputOptions{}
	svc := newInputOptionsService(t, io)
	ctx := auth.WithMachineToken(asUser("alice"), &fat.Principal{ActingUserID: "alice", Scopes: fat.SetOf(fat.ScopeReliantAPI)})
	_, err := listInputOptions(ctx, svc, &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
	assert.Equal(t, connect.CodePermissionDenied, codeOf(t, err))
	_, err = listInputOptions(ctx, svc, &reliantv1.ListInputOptionsRequest{Type: ptr("github.user"), Resolve: []string{"1"}})
	assert.Equal(t, connect.CodePermissionDenied, codeOf(t, err))
	assert.Empty(t, io.Calls("List"))
	assert.Empty(t, io.Calls("Resolve"))
}

// The user the service acts for is the credential's, whatever else the
// request says.
func TestListInputOptions_ActsForTheAuthenticatedUserOnly(t *testing.T) {
	io := &StubInputOptions{ListFunc: func(context.Context, inputoptions.ListRequest) (*inputoptions.ListResult, error) {
		return &inputoptions.ListResult{State: inputoptions.StateConnected}, nil
	}}
	svc := newInputOptionsService(t, io)
	_, err := listInputOptions(asUser("bob"), svc, &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository"), ConnectionId: ptr("alices-connection")})
	require.NoError(t, err)
	got := io.Calls("List")[0].Args[1].(inputoptions.ListRequest)
	assert.Equal(t, "bob", got.UserID)
	assert.Equal(t, "alices-connection", got.ConnectionID, "the id is passed on to be looked up among bob's connections")
}

func TestListInputOptions_UnimplementedWithoutAnOptionSource(t *testing.T) {
	_, err := listInputOptions(asUser("alice"), newInputOptionsService(t, nil), &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
	assert.Equal(t, connect.CodeUnimplemented, codeOf(t, err))
}

// --- error mapping --------------------------------------------------------------

func TestListInputOptions_MapsTypedFailuresToCodes(t *testing.T) {
	type tc struct {
		err  error
		code connect.Code
		msg  string
	}
	cases := map[string]tc{
		"unknown type":       {inputoptions.ErrUnknownType, connect.CodeNotFound, ""},
		"missing connection": {inputoptions.ErrConnectionNotFound, connect.CodeNotFound, ""},
		"no options":         {inputoptions.ErrNoOptions, connect.CodeInvalidArgument, ""},
		"bad scope":          {inputoptions.ErrBadScope, connect.CodeInvalidArgument, ""},
		"bad token":          {inputoptions.ErrBadPageToken, connect.CodeInvalidArgument, ""},
		"bad request":        {inputoptions.ErrBadRequest, connect.CodeInvalidArgument, ""},
		"rate limited":       {&inputoptions.UpstreamError{Message: "GitHub is rate limiting", Retryable: true}, connect.CodeUnavailable, "GitHub is rate limiting"},
		"upstream refused":   {&inputoptions.UpstreamError{Message: "GitHub refused the request"}, connect.CodeFailedPrecondition, "GitHub refused the request"},
		"cancelled":          {context.Canceled, connect.CodeCanceled, ""},
		"deadline":           {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"anything else":      {errors.New("secret internals: token=abc"), connect.CodeInternal, "listing input options failed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			io := &StubInputOptions{ListFunc: func(context.Context, inputoptions.ListRequest) (*inputoptions.ListResult, error) { return nil, c.err }}
			_, err := listInputOptions(asUser("alice"), newInputOptionsService(t, io), &reliantv1.ListInputOptionsRequest{Type: ptr("github.repository")})
			require.Error(t, err)
			assert.Equal(t, c.code, codeOf(t, err))
			if c.msg != "" {
				assert.Contains(t, err.Error(), c.msg)
			}
			assert.NotContains(t, err.Error(), "token=abc", "internals never reach the caller")
		})
	}
}

func ptr[T any](v T) *T { return &v }
