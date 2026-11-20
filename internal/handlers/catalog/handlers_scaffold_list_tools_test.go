package catalog_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	catalog "github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestListTools_Generated(t *testing.T) {
	t.Parallel()

	svc := catalog.NewTestCatalog(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ListToolsRequest, pb.ListToolsResponse]{
		{
			Name: "every registered tool is listed once, with a category",
			Req:  &pb.ListToolsRequest{},
			Ctx:  catalog.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.ListToolsResponse) {
				registry := tools.GetToolRegistry()
				if got := resp.GetTools(); len(got) != len(registry) || int(resp.GetTotal()) != len(registry) {
					t.Fatalf("listed %d tools (total %d), want the %d registered", len(got), resp.GetTotal(), len(registry))
				}
				seen := map[string]bool{}
				for _, tool := range resp.GetTools() {
					if seen[tool.GetName()] {
						t.Fatalf("tool %q is listed twice", tool.GetName())
					}
					seen[tool.GetName()] = true
					if tool.GetCategory() == "" {
						t.Fatalf("tool %q has no category", tool.GetName())
					}
				}
				for _, def := range registry {
					if !seen[def.Name] {
						t.Fatalf("registered tool %q is not listed", def.Name)
					}
				}
			},
		},
	}, svc.ListTools)
}
