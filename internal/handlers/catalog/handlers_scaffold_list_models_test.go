package catalog_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	catalog "github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
)

func TestListModels_Generated(t *testing.T) {
	t.Parallel()

	noLocal := catalog.NewTestCatalog(t)
	withLocal := catalog.NewTestCatalog(t, catalog.WithCatalogDeps(catalog.Deps{
		LocalModels: localDaemons{ollamaDaemon("d-1", "qwen3:latest")},
	}))
	ctx := catalog.AuthedContext(t, testkit.WithUserID(noProvidersUser))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ListModelsRequest, pb.ListModelsResponse]{
		{
			Name: "a caller with no provider credentials and no machines has no models",
			Req:  &pb.ListModelsRequest{},
			Ctx:  ctx,
			Check: func(t *testing.T, resp *pb.ListModelsResponse) {
				if got := resp.GetModels(); len(got) != 0 || resp.GetTotal() != 0 {
					t.Fatalf("models = %v (total %d), want none", got, resp.GetTotal())
				}
			},
		},
	}, noLocal.ListModels)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ListModelsRequest, pb.ListModelsResponse]{
		{
			Name: "the caller's machines' local chat models are listed",
			Req:  &pb.ListModelsRequest{},
			Ctx:  ctx,
			Check: func(t *testing.T, resp *pb.ListModelsResponse) {
				got := resp.GetModels()
				if len(got) != 1 || resp.GetTotal() != 1 {
					t.Fatalf("models = %v (total %d), want the one local model", got, resp.GetTotal())
				}
				if got[0].GetId() != "qwen3:latest@local" || got[0].GetDriverId() != string(local.Family) || got[0].GetLocal().GetDaemonId() != "d-1" {
					t.Fatalf("model = %v, want qwen3:latest@local on d-1", got[0])
				}
			},
		},
	}, withLocal.ListModels)
}
