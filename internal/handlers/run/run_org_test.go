package run_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	run "github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// orgSource: test-user is a live member of acme and home; home is the default.
type orgSource struct{}

func (orgSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

// One session's or one parent's runs are its chat's org's: listing them with
// an org verifies the org, and a chat outside it lists nothing.
func TestListRuns_SessionAndParentHonourTheOrg(t *testing.T) {
	acme := "acme"
	chat := &db.Chat{ID: "c-1", UserID: "test-user", OrgID: &acme}
	runs := []*db.Workflow{{ID: "w-1", ChatID: "c-1"}}
	repo := &run.MockRepository{
		GetChatRowFunc: func(context.Context, string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: "c-1", UserID: "test-user", OrgID: &acme}, nil
		},
		GetChatFunc:            func(context.Context, string) (*db.Chat, error) { return chat, nil },
		GetWorkflowFunc:        func(context.Context, string) (*db.Workflow, error) { return runs[0], nil },
		ListRootWorkflowsFunc:  func(context.Context, string) ([]*db.Workflow, error) { return runs, nil },
		ListChildWorkflowsFunc: func(context.Context, string) ([]*db.Workflow, error) { return runs, nil },
	}
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Orgs: orgs.New(orgSource{})}))
	ctx := run.AuthedContext(t)
	str := func(s string) *string { return &s }

	for name, req := range map[string]func(org *string) *pb.ListRunsRequest{
		"session": func(org *string) *pb.ListRunsRequest { return &pb.ListRunsRequest{SessionId: str("c-1"), OrgId: org} },
		"parent":  func(org *string) *pb.ListRunsRequest { return &pb.ListRunsRequest{ParentId: str("w-1"), OrgId: org} },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.ListRuns(ctx, connect.NewRequest(req(str("acme"))))
			if err != nil || len(got.Msg.GetRuns()) != 1 {
				t.Fatalf("its own org: %v runs, %v; want the run", len(got.Msg.GetRuns()), err)
			}
			got, err = svc.ListRuns(ctx, connect.NewRequest(req(str("home"))))
			if err != nil || len(got.Msg.GetRuns()) != 0 {
				t.Fatalf("another org: %d runs, %v; want none", len(got.Msg.GetRuns()), err)
			}
			if _, err := svc.ListRuns(ctx, connect.NewRequest(req(str("globex")))); svcerr.Code(err) != connect.CodePermissionDenied {
				t.Fatalf("an org the caller is not in: %v, want PermissionDenied", err)
			}
			got, err = svc.ListRuns(ctx, connect.NewRequest(req(nil)))
			if err != nil || len(got.Msg.GetRuns()) != 1 {
				t.Fatalf("no org named: %v, %v; want the run, as before orgs", got, err)
			}
		})
	}
}
