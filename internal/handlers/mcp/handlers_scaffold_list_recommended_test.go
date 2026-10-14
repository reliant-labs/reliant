package mcp_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
)

func TestListRecommended_Generated(t *testing.T) {
	t.Parallel()

	// context7 is configured in the project; the rest of the catalog is not.
	store := ownedProjectRepo(t, `{"project":"{\"mcpServers\":{\"context7\":{\"command\":\"npx\",\"enabled\":true}}}"}`)
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListRecommendedRequest, pb.ListRecommendedResponse]{
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.ListRecommendedRequest{}),
			Ctx:     mcp.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.ListRecommendedRequest{ProjectId: "p-2"}),
			Ctx:     mcp.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the catalog marks what the project already has installed",
			Req:  connect.NewRequest(&pb.ListRecommendedRequest{ProjectId: "p-1"}),
			Ctx:  mcp.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListRecommendedResponse]) {
				installed := map[string]bool{}
				for _, rec := range resp.Msg.GetRecommended() {
					installed[rec.GetName()] = rec.GetInstalled()
				}
				if int(resp.Msg.GetTotal()) != len(resp.Msg.GetRecommended()) || len(installed) < 2 {
					t.Fatalf("recommended = %v, want the catalog with its total", resp.Msg.GetRecommended())
				}
				if v, ok := installed["context7"]; !ok || !v {
					t.Fatalf("context7 installed = %v (listed %v), want listed and installed", v, ok)
				}
				if installed["supabase"] {
					t.Fatal("supabase is marked installed; the project does not configure it")
				}
			},
		},
	}, svc.ListRecommended)
}
