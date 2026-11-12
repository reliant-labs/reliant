package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The chat rpcs that read one chat, asked as the owner, a viewer (a live
// member of the org the chat is shared with) and an outsider, on a chat whose
// visibility is 'org' in the database. At this head no read site serves
// viewers (authz.WithViewerReads), so a viewer is refused exactly as an
// outsider: sharing reaches nothing until a site opts in with its redaction
// (org-sharing S3 flips the Viewer column per rpc, deliberately).
func TestChatReadsRefuseAViewerUntilTheyServeViewers(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: f.Membership}))
	chat := f.ChatID
	viewertest.Run(t, f, []viewertest.Case{
		{Name: "GetChat", Call: func(ctx context.Context) error {
			_, err := svc.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: chat}))
			return err
		}},
		{Name: "ListMessages", Call: func(ctx context.Context) error {
			_, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: chat}))
			return err
		}},
		{Name: "ListChatPlans", Call: func(ctx context.Context) error {
			_, err := svc.ListChatPlans(ctx, connect.NewRequest(&reliantv1.ListChatPlansRequest{ChatId: chat}))
			return err
		}},
		{Name: "ListStepExecutions", Call: func(ctx context.Context) error {
			_, err := svc.ListStepExecutions(ctx, connect.NewRequest(&reliantv1.ListStepExecutionsRequest{ChatId: chat}))
			return err
		}},
		{Name: "GetWorkflowExecutions", Call: func(ctx context.Context) error {
			_, err := svc.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{ChatId: chat}))
			return err
		}},
		{Name: "ListBranches", Call: func(ctx context.Context) error {
			_, err := svc.ListBranches(ctx, connect.NewRequest(&reliantv1.ListBranchesRequest{ChatId: chat}))
			return err
		}},
		{Name: "GetChatUpdates", Call: func(ctx context.Context) error {
			_, err := svc.GetChatUpdates(ctx, connect.NewRequest(&reliantv1.GetChatUpdatesRequest{ChatId: chat}))
			return err
		}},
	})
}
