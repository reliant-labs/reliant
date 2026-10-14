package catalog_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	catalog "github.com/reliant-labs/reliant/internal/handlers/catalog"
)

func TestListAvailableModels_Generated(t *testing.T) {
	t.Parallel()

	svc := catalog.NewTestCatalog(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListAvailableModelsRequest, pb.ListAvailableModelsResponse]{
		{
			Name: "a caller with no provider credentials has no models",
			Req:  connect.NewRequest(&pb.ListAvailableModelsRequest{}),
			Ctx:  catalog.AuthedContext(t, testkit.WithUserID(noProvidersUser)),
			Check: func(t *testing.T, resp *connect.Response[pb.ListAvailableModelsResponse]) {
				if got := resp.Msg.GetModels(); len(got) != 0 {
					t.Fatalf("models = %v, want none", got)
				}
			},
		},
	}, svc.ListAvailableModels)
}
