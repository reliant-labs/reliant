package catalog_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	catalog "github.com/reliant-labs/reliant/internal/handlers/catalog"
)

func TestListAvailableModels_Generated(t *testing.T) {
	t.Parallel()

	svc := catalog.NewTestCatalog(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ListAvailableModelsRequest, pb.ListAvailableModelsResponse]{
		{
			Name: "a caller with no provider credentials has no models",
			Req:  &pb.ListAvailableModelsRequest{},
			Ctx:  catalog.AuthedContext(t, testkit.WithUserID(noProvidersUser)),
			Check: func(t *testing.T, resp *pb.ListAvailableModelsResponse) {
				if got := resp.GetModels(); len(got) != 0 {
					t.Fatalf("models = %v, want none", got)
				}
			},
		},
	}, svc.ListAvailableModels)
}
