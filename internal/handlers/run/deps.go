// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package run

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the run handlers use.
type Repository interface {
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*db.StepExecution, error)
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	ListChildWorkflows(ctx context.Context, parentID string) ([]*db.Workflow, error)
	ListRootWorkflows(ctx context.Context, chatID string) ([]*db.Workflow, error)
	ListRuns(ctx context.Context, f db.RunListFilters) ([]*db.RunListItem, bool, error)
	ListUnlaunchedTriggerEvents(ctx context.Context, f db.RunListFilters) ([]*db.UnlaunchedTriggerEvent, bool, error)
	LastRunPerWorkflow(ctx context.Context, f db.RunListFilters) ([]*db.RunListItem, error)
}

// Chat is the slice of the chat service a run is controlled through. The
// chat service implements it; run never imports it.
type Chat interface {
	StartChat(ctx context.Context, req *connect.Request[pb.StartChatRequest]) (*connect.Response[pb.StartChatResponse], error)
	SendMessage(ctx context.Context, req *connect.Request[pb.SendMessageRequest]) (*connect.Response[pb.SendMessageResponse], error)
	PauseChat(ctx context.Context, req *connect.Request[pb.PauseChatRequest]) (*connect.Response[pb.PauseChatResponse], error)
	ResumeChat(ctx context.Context, req *connect.Request[pb.ResumeChatRequest]) (*connect.Response[pb.ResumeChatResponse], error)
	TerminateChat(ctx context.Context, req *connect.Request[pb.TerminateChatRequest]) (*connect.Response[pb.TerminateChatResponse], error)
	InterruptThread(ctx context.Context, req *connect.Request[pb.InterruptThreadRequest]) (*connect.Response[pb.InterruptThreadResponse], error)
}
