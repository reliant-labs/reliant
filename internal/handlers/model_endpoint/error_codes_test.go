package model_endpoint_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	model_endpoint "github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
)

// What a store failure tells the caller: a classified error keeps its code
// and text, and an unclassified one is Internal with its text withheld.
func TestModelEndpointStoreErrorCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		"not found":         {db.ErrModelEndpointNotFound, connect.CodeNotFound, "model endpoint not found"},
		"deadline":          {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"canceled":          {context.Canceled, connect.CodeCanceled, ""},
		"opaque store text": {errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal, "model endpoint request failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := &model_endpoint.StubRepo{
				ListModelEndpointsFunc: func(context.Context, string) ([]*db.ModelEndpoint, error) { return nil, tc.err },
			}
			svc := model_endpoint.NewTestModelEndpoint(t, model_endpoint.WithModelEndpointDeps(model_endpoint.Deps{Repo: repo}), model_endpoint.WithPlaceholderDeps())
			_, err := svc.ListModelEndpoints(model_endpoint.AuthedContext(t), connect.NewRequest(&pb.ListModelEndpointsRequest{}))
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret")
			if tc.wantMsg != "" {
				assert.Equal(t, tc.wantMsg, msg)
			}
		})
	}
}

// Over a real Connect client: the interceptor sends a validation failure's own
// text as InvalidArgument, and an opaque store failure as Internal without it.
func TestModelEndpointErrorsOnTheWire(t *testing.T) {
	t.Parallel()

	repo := &model_endpoint.StubRepo{
		ListModelEndpointsFunc: func(context.Context, string) ([]*db.ModelEndpoint, error) {
			return nil, errors.New("dsn=postgres://u:secret@h")
		},
	}
	_, client := model_endpoint.NewTestModelEndpointServer(t, model_endpoint.WithModelEndpointDeps(model_endpoint.Deps{Repo: repo}), model_endpoint.WithPlaceholderDeps())

	_, err := client.ListModelEndpoints(context.Background(), connect.NewRequest(&pb.ListModelEndpointsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err))
	assert.Equal(t, "model endpoint request failed", svcerr.ToConnect(err).Message())

	_, err = client.CreateModelEndpoint(context.Background(), connect.NewRequest(&pb.CreateModelEndpointRequest{
		Endpoint: &pb.ModelEndpointInput{Name: "gpu box", BaseUrl: "ftp://llm.example.com"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Equal(t, "The URL must start with http:// or https://.", svcerr.ToConnect(err).Message())
}
