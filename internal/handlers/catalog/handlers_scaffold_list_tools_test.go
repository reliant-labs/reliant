package catalog_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	catalog "github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestListTools_Generated(t *testing.T) {
	t.Parallel()

	svc := catalog.NewTestCatalog(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListToolsRequest, pb.ListToolsResponse]{
		{
			Name: "every registered tool but the feature-gated ones is listed once, with a category",
			Req:  connect.NewRequest(&pb.ListToolsRequest{}),
			Ctx:  catalog.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListToolsResponse]) {
				// The test catalog has no gate, so it entitles no caller to a
				// gated tool.
				registry, gated := tools.GetToolRegistry(), tools.FeatureGatedTools()
				want := len(registry) - len(gated)
				if got := resp.Msg.GetTools(); len(got) != want || int(resp.Msg.GetTotal()) != want {
					t.Fatalf("listed %d tools (total %d), want the %d registered and not gated", len(got), resp.Msg.GetTotal(), want)
				}
				seen := map[string]bool{}
				for _, tool := range resp.Msg.GetTools() {
					if seen[tool.GetName()] {
						t.Fatalf("tool %q is listed twice", tool.GetName())
					}
					seen[tool.GetName()] = true
					if tool.GetCategory() == "" {
						t.Fatalf("tool %q has no category", tool.GetName())
					}
				}
				for _, def := range registry {
					if _, isGated := gated[def.Name]; !isGated && !seen[def.Name] {
						t.Fatalf("registered tool %q is not listed", def.Name)
					}
				}
			},
		},
	}, svc.ListTools)
}
