package project_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProjectPrompts_Generated(t *testing.T) {
	t.Parallel()

	none := ownedProjectRepo(t)
	none.GetSettingFunc = func(context.Context, string, *string, string) (*db.Setting, error) {
		return nil, errors.New("setting not found")
	}
	empty := newService(t, none, nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectPromptsRequest, pb.GetProjectPromptsResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectPromptsRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a project with no saved prompts has none",
			Req:  connect.NewRequest(&pb.GetProjectPromptsRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectPromptsResponse]) {
				if got := resp.Msg.GetPrompts(); len(got) != 0 {
					t.Fatalf("prompts = %v, want none", got)
				}
			},
		},
	}, empty.GetProjectPrompts)

	saved := ownedProjectRepo(t)
	saved.GetSettingFunc = func(_ context.Context, userID string, projectID *string, key string) (*db.Setting, error) {
		if userID != "test-user" || projectID == nil || *projectID != "p-1" || key != "project.prompts" {
			return nil, errors.New("setting not found")
		}
		return &db.Setting{Value: `[{"id":"pr-1","name":"review","content":"Review this diff"}]`}, nil
	}
	svc := newService(t, saved, nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectPromptsRequest, pb.GetProjectPromptsResponse]{
		{
			Name: "the caller's saved prompts for the project are returned",
			Req:  connect.NewRequest(&pb.GetProjectPromptsRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectPromptsResponse]) {
				got := resp.Msg.GetPrompts()
				if len(got) != 1 || got[0].GetId() != "pr-1" || got[0].GetName() != "review" || got[0].GetContent() != "Review this diff" {
					t.Fatalf("prompts = %v, want pr-1", got)
				}
			},
		},
	}, svc.GetProjectPrompts)
}
