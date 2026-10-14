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

func TestSaveProjectPrompts_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	store.GetSettingFunc = func(context.Context, string, *string, string) (*db.Setting, error) {
		return nil, errors.New("setting not found")
	}
	var created *db.Setting
	store.CreateSettingFunc = func(_ context.Context, s *db.Setting) error {
		created = s
		return nil
	}
	svc := newService(t, store, nil)
	prompts := []*pb.Prompt{{Id: "pr-1", Name: "review", Content: "Review this diff"}}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SaveProjectPromptsRequest, pb.SaveProjectPromptsResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.SaveProjectPromptsRequest{ProjectId: "p-2", Prompts: prompts}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the first save creates the caller's project setting",
			Req:  connect.NewRequest(&pb.SaveProjectPromptsRequest{ProjectId: "p-1", Prompts: prompts}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.SaveProjectPromptsResponse]) {
				if created == nil || created.UserID != "test-user" || created.ProjectID == nil || *created.ProjectID != "p-1" || created.Key != "project.prompts" {
					t.Fatalf("created = %+v, want test-user's project.prompts on p-1", created)
				}
				const want = `[{"id":"pr-1","name":"review","content":"Review this diff","description":"","created_at":"","updated_at":""}]`
				if created.Value != want {
					t.Fatalf("value = %s, want %s", created.Value, want)
				}
				if got := resp.Msg.GetPrompts(); len(got) != 1 || got[0].GetId() != "pr-1" {
					t.Fatalf("prompts = %v, want pr-1 echoed", got)
				}
			},
		},
	}, svc.SaveProjectPrompts)
}
