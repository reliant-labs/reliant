// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

type fakeInputOptions struct{ users []string }

func (f *fakeInputOptions) List(_ context.Context, r inputoptions.ListRequest) (*inputoptions.ListResult, error) {
	f.users = append(f.users, r.UserID)
	return &inputoptions.ListResult{State: inputoptions.StateConnected, Options: []inputoptions.Option{{Value: "acme/web", Label: "acme/web"}}}, nil
}

func (f *fakeInputOptions) Resolve(context.Context, inputoptions.ResolveRequest) (*inputoptions.Resolution, error) {
	return &inputoptions.Resolution{}, nil
}

// ListInputOptions is mounted behind the same authentication as the rest of
// the API: an anonymous caller is refused before the option service is asked,
// and an authenticated one reaches it as themselves.
func TestListInputOptionsIsMountedBehindAuth(t *testing.T) {
	opts := &fakeInputOptions{}
	_, baseURL := startTestServer(t, testCatalog(t, opts))
	client := reliantv1connect.NewCatalogServiceClient(rpcclient.New(h2cClient(), baseURL, nil))
	typ := "github.repository"

	_, err := client.ListInputOptions(context.Background(), &reliantv1.ListInputOptionsRequest{Type: &typ})
	require.Equal(t, connect.CodeUnauthenticated, svcerr.CodeV2(err), "got %v", err)
	assert.Empty(t, opts.users, "an anonymous caller never reaches the option service")

	resp, err := client.ListInputOptions(authed(context.Background()), &reliantv1.ListInputOptionsRequest{Type: &typ})
	require.NoError(t, err)
	require.Len(t, resp.GetOptions(), 1)
	assert.Equal(t, "acme/web", resp.GetOptions()[0].GetValue())
	assert.Equal(t, reliantv1.InputOptionsConnectionState_INPUT_OPTIONS_CONNECTION_STATE_CONNECTED, resp.GetConnectionState())
	require.Len(t, opts.users, 1)
	assert.NotEmpty(t, opts.users[0], "the user is the credential's, set by the server")
}

// A deployment composed without an option source answers Unimplemented, not a
// missing route.
func TestListInputOptionsWithoutAnOptionSourceIsUnimplemented(t *testing.T) {
	_, baseURL := startTestServer(t, testCatalog(t, nil))
	client := reliantv1connect.NewCatalogServiceClient(rpcclient.New(h2cClient(), baseURL, nil))
	typ := "github.repository"
	_, err := client.ListInputOptions(authed(context.Background()), &reliantv1.ListInputOptionsRequest{Type: &typ})
	require.Equal(t, connect.CodeUnimplemented, svcerr.CodeV2(err), "got %v", err)
	require.Contains(t, err.Error(), "not configured")
}
