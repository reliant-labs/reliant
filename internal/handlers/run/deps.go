// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package run

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// Repository is the slice of *db.Repo the run handlers use.
// OrgResolver answers the org questions of these rpcs. *orgs.Resolver is
// one; nil (or a nil *orgs.Resolver) is a deployment with no orgs.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
	DefaultOrg(ctx context.Context, userID string) (string, error)
	ActingOrg(ctx context.Context, userID, requested string) (string, error)
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
}

type Repository interface {
	// AssignUnassignedOrg stamps the caller's rows that have no org with
	// their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*db.StepExecution, error)

	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	ListChildWorkflows(ctx context.Context, parentID string) ([]*db.Workflow, error)
	ListRootWorkflows(ctx context.Context, chatID string) ([]*db.Workflow, error)
	ListRuns(ctx context.Context, f db.RunListFilters) ([]*db.RunListItem, bool, error)
}

// RunLists reads the run log's other halves: the trigger events that started
// no run, and each workflow's newest run. store.RunListExtStore satisfies it.
type RunLists interface {
	ListUnlaunchedTriggerEvents(ctx context.Context, f db.RunListFilters) ([]*db.UnlaunchedTriggerEvent, bool, error)
	LastRunPerWorkflowAndOrg(ctx context.Context, f db.RunListFilters) ([]*db.RunListItem, error)
}

// WorkflowLoader reads one workflow row by id. store.WorkflowExtStore
// satisfies it.
type WorkflowLoader interface {
	LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}

// ChatRows reads a chat's own row, for the access check. store.ChatExtStore
// satisfies it.
type ChatRows interface {
	GetChatRow(ctx context.Context, id string) (*core.ChatRow, error)
}

// ChatViews reads a chat with its computed state (the root run's status).
// store.ChatsWithActivityExtStore satisfies it.
type ChatViews interface {
	GetChat(ctx context.Context, id string) (*core.Chat, error)
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
