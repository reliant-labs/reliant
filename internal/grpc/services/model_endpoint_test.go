// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
)

func ctxFor(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

func openAIModels(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			body := `{"data":[`
			for i, id := range ids {
				if i > 0 {
					body += ","
				}
				body += `{"id":"` + id + `"}`
			}
			_, _ = w.Write([]byte(body + `]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// No control plane configured == self-hosted: loopback httptest servers are
// legitimate targets.
func TestModelEndpointServiceCRUDOverTheHandler(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "")
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	svc := NewModelEndpointService(repo, nil, nil)
	srv := openAIModels(t, "llama", "mixtral")

	create, err := svc.CreateModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
		Endpoint: &reliantv1.ModelEndpointInput{Name: "Lab", BaseUrl: srv.URL + "/v1/", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT},
	}))
	require.NoError(t, err)
	ep := create.Msg.GetEndpoint()
	assert.Equal(t, srv.URL+"/v1", ep.GetBaseUrl(), "the URL is normalized")
	assert.Len(t, ep.GetProbe().GetModels(), 2)
	assert.False(t, ep.GetHasApiKey())

	t.Run("another user sees an empty list and gets NotFound", func(t *testing.T) {
		list, err := svc.ListModelEndpoints(ctxFor("svc-user-b"), connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
		require.NoError(t, err)
		assert.Empty(t, list.Msg.GetEndpoints())

		_, err = svc.DeleteModelEndpoint(ctxFor("svc-user-b"), connect.NewRequest(&reliantv1.DeleteModelEndpointRequest{Id: ep.GetId()}))
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		_, err = svc.TestModelEndpoint(ctxFor("svc-user-b"), connect.NewRequest(&reliantv1.TestModelEndpointRequest{Target: &reliantv1.TestModelEndpointRequest_Id{Id: ep.GetId()}}))
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("an API key is rejected with a clear message and nothing is stored", func(t *testing.T) {
		key := "sk-live"
		_, err := svc.CreateModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{Name: "Keyed", BaseUrl: srv.URL + "/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT, ApiKey: &key},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "API keys for custom endpoints need the sealed credential store; coming with the next sync")
		list, _ := svc.ListModelEndpoints(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
		assert.Len(t, list.Msg.GetEndpoints(), 1)
	})

	t.Run("validation errors are InvalidArgument", func(t *testing.T) {
		_, err := svc.CreateModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{Name: "Bad", BaseUrl: "https://u:p@x.example.com/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT},
		}))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("duplicate name is AlreadyExists", func(t *testing.T) {
		_, err := svc.CreateModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{Name: "Lab", BaseUrl: srv.URL + "/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT},
		}))
		assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	})

	t.Run("unauthenticated", func(t *testing.T) {
		_, err := svc.ListModelEndpoints(context.Background(), connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("via_daemon needs a daemon the user owns", func(t *testing.T) {
		_, err := svc.CreateModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{Name: "Via", BaseUrl: "http://10.0.0.5:8000/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON, DaemonId: "not-mine"},
		}))
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("the catalog lists the endpoint's models grouped under its name, hidden ones omitted", func(t *testing.T) {
		upd := &reliantv1.ModelEndpointInput{
			Name: "Lab", BaseUrl: srv.URL + "/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT,
			Models: []*reliantv1.ModelEndpointModel{{Name: "mixtral", Hidden: true}, {Name: "llama", ContextWindow: 65536}},
		}
		_, err := svc.UpdateModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.UpdateModelEndpointRequest{Id: ep.GetId(), Endpoint: upd}))
		require.NoError(t, err)

		catalog := NewCatalogService(nil).WithLocalModels(local.NewRepoDirectory(repo))
		infos := catalog.localModelInfos(ctxFor("svc-user-a"), "svc-user-a")
		require.Len(t, infos, 1)
		got := infos[0]
		assert.Equal(t, "llama@local", got.GetId())
		assert.Equal(t, int64(65536), got.GetContextWindow())
		assert.Equal(t, "Lab", got.GetLocal().GetMachineName(), "grouped by endpoint name")
		assert.Equal(t, ep.GetId(), got.GetLocal().GetEndpointId())
		assert.Empty(t, got.GetLocal().GetDaemonId(), "a direct endpoint has no daemon")
		assert.True(t, got.GetLocal().GetOnline())

		other := catalog.localModelInfos(ctxFor("svc-user-b"), "svc-user-b")
		assert.Empty(t, other, "another user never sees it")
	})

	t.Run("delete", func(t *testing.T) {
		_, err := svc.DeleteModelEndpoint(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.DeleteModelEndpointRequest{Id: ep.GetId()}))
		require.NoError(t, err)
		list, _ := svc.ListModelEndpoints(ctxFor("svc-user-a"), connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
		assert.Empty(t, list.Msg.GetEndpoints())
	})
}

func TestModelEndpointServiceRefusesPrivateTargetsWhenHosted(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "http://admin-server.cluster.local:8090")
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	svc := NewModelEndpointService(repo, nil, nil)
	srv := openAIModels(t, "m") // 127.0.0.1

	for _, url := range []string{srv.URL + "/v1", "http://169.254.169.254/v1", "http://10.0.0.1/v1", "http://localhost:11434/v1"} {
		_, err := svc.CreateModelEndpoint(ctxFor("hosted-user"), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{Name: url, BaseUrl: url, Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT},
		}))
		require.Error(t, err, url)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), url)
		assert.Contains(t, err.Error(), "through one of your machines", url)
	}
	list, _ := svc.ListModelEndpoints(ctxFor("hosted-user"), connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
	assert.Empty(t, list.Msg.GetEndpoints(), "a refused endpoint is not stored")
}
