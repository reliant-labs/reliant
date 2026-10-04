// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// The run-management tools (start_run, list_runs, get_run, control_run,
// send_to_run) act on detached, top-level runs — chats — that the calling
// chat's user owns. That is a different object from a sub-agent: a sub-agent is
// a goroutine inside the caller's own execution and is managed with spawn_*;
// a run is its own chat with its own root workflow, visible to the human in
// the sidebar.
//
// A tool has no business knowing what a launcher or a workflow engine is, and
// the packages that implement these (internal/launch, internal/runs) depend on
// this one, so the tools take narrow interfaces declared here and the wiring
// (internal/serverworker, internal/serverapi) supplies the implementation. A
// nil implementation is valid: the daemon runtime has no Temporal connection,
// and each tool then says it is unavailable rather than pretending.

// RunStarter launches a new top-level run.
type RunStarter interface {
	StartRun(ctx context.Context, req StartRunRequest) (StartedRun, error)
}

// StartRunRequest is what start_run asks the launcher for. The owner is always
// explicit: the tool derives it from the calling chat, never from a parameter.
type StartRunRequest struct {
	OwnerUserID string
	ProjectID   string
	Workflow    string
	Message     string
	Inputs      map[string]any
	Presets     map[string]string
	Title       string
	// DaemonID is the daemon the new run executes its tools on. start_run
	// inherits the calling chat's active daemon, the same way a trigger pins
	// the daemon it names: the run must land on the user's machine the caller
	// is already using, not on whichever daemon the runtime would pick.
	DaemonID string

	// ParentChatID is the chat whose agent is starting this run. It is
	// recorded on the launch event, and the fork-bomb guard walks it.
	ParentChatID string
	// DedupeKey makes the launch idempotent: the same key never launches
	// twice, so a retried tool call attaches to the run it already started.
	DedupeKey string
}

// StartedRun is the run that exists after a StartRun call.
type StartedRun struct {
	ChatID string
	// RunID is the Temporal run id; empty when the launch had already
	// happened and this call only attached to it.
	RunID string
	// AlreadyStarted reports that DedupeKey had launched before this call.
	AlreadyStarted bool
}

// RunLifecycle is the pause/resume/cancel machinery for an existing run. The
// tool checks ownership before calling it; userID is the run's owner, passed so
// the implementation can act as them.
type RunLifecycle interface {
	PauseRun(ctx context.Context, userID, chatID string) error
	ResumeRun(ctx context.Context, userID, chatID string) (RunResumeResult, error)
	CancelRun(ctx context.Context, userID, chatID string) error
}

// RunResumeResult is what a resume attempt settled as.
type RunResumeResult struct {
	// Resumed reports the run is executing again.
	Resumed bool
	// Detail explains a run that could not be resumed in place.
	Detail string
}

// RunMessenger delivers a message to a run that is already going. It must
// never start a run: a run that is not live reports Delivered=false.
type RunMessenger interface {
	DeliverToRun(ctx context.Context, userID, chatID, message string) (RunDelivery, error)
}

// RunDelivery is what a delivery did. It never claims more than happened: a
// saved message is not a read message.
type RunDelivery struct {
	// Delivered is true when the message was durably saved to the run's
	// thread. False means the run was not live and nothing was written.
	Delivered bool
	MessageID string
}

// Limits on agents starting agents. An agent that can start a run that can
// start a run is a fork bomb with a billing meter attached, so lineage depth
// and the number of simultaneously live agent-started runs are both capped.
const (
	// MaxAgentStartRunDepth is how many start_run links may chain below a
	// human-started run. A human-started run has depth 0; a run it starts with
	// start_run has depth 1; a run THAT starts one has depth 2; and so on. A
	// run at depth MaxAgentStartRunDepth may not start another.
	MaxAgentStartRunDepth = 3

	// MaxConcurrentAgentStartedRuns bounds how many agent-started runs one
	// user may have live (pending, active or paused) at once, across every
	// chain. It is a user-wide cap rather than per-chain because the cost is
	// the user's, not the chain's.
	MaxConcurrentAgentStartedRuns = 10
)

// runStateLabel renders a chat's root-run state for these tools. A chat with
// no root workflow row yet (an unstarted branch) reads as pending: both mean
// "has not started".
func runStateLabel(chat *db.Chat) string {
	if chat.RootStatus.State == core.WorkflowStateUnspecified {
		return "pending"
	}
	return chat.RootStatus.Label()
}

func formatRunTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// runCaller is the chat a run-management tool is executing in.
type runCaller struct {
	chat   *db.Chat
	userID string
}

// resolveRunCaller loads the calling chat, whose UserID is the identity every
// run-management tool acts as. The owner is never a parameter: a model must not
// be able to name someone else's account.
func resolveRunCaller(rctx *rctx.ToolContext, repo db.Repository) (*runCaller, *ToolResponse) {
	if repo == nil {
		resp := NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode")
		return nil, &resp
	}
	if rctx.ChatID == "" {
		resp := NewTextErrorResponse("Chat context required")
		return nil, &resp
	}
	chat, err := repo.GetChat(rctx.Context, rctx.ChatID)
	if err != nil || chat == nil {
		resp := NewTextErrorResponse("Could not resolve the calling chat: " + errText(err))
		return nil, &resp
	}
	return &runCaller{chat: chat, userID: chat.UserID}, nil
}

// resolveOwnedRun loads the run runID and verifies the caller's user owns it.
//
// A run that does not exist and a run someone else owns produce the SAME
// response, so the tool cannot be used to probe for other users' chat ids.
func resolveOwnedRun(rctx *rctx.ToolContext, repo db.Repository, caller *runCaller, runID string) (*db.Chat, *ToolResponse) {
	notFound := func() (*db.Chat, *ToolResponse) {
		resp := NewTextErrorResponse(fmt.Sprintf("Run %q not found. list_runs shows the runs you can manage.", runID))
		return nil, &resp
	}
	if strings.TrimSpace(runID) == "" {
		resp := NewTextErrorResponse("run_id is required")
		return nil, &resp
	}
	chat, err := repo.GetChat(rctx.Context, runID)
	if err != nil {
		// The store reports a missing chat as an ordinary error. Only that
		// reads as "not found"; anything else is a failure the caller should
		// not mistake for absence.
		if strings.Contains(err.Error(), "chat not found") {
			return notFound()
		}
		resp := NewTextErrorResponse("Could not look up the run: " + err.Error())
		return nil, &resp
	}
	if chat == nil || chat.UserID != caller.userID {
		return notFound()
	}
	return chat, nil
}

// rejectSelfTarget stops an agent managing the run it is executing in. Pausing
// or cancelling yourself ends the work mid-call, and messaging yourself just
// loops; your own children are managed with spawn_*.
func rejectSelfTarget(rctx *rctx.ToolContext, runID, verb string) *ToolResponse {
	if runID != rctx.ChatID {
		return nil
	}
	resp := NewTextErrorResponse(fmt.Sprintf(
		"run_id %q is the run you are executing in, and an agent may not %s its own run. "+
			"To manage sub-agents you spawned, use spawn_status, spawn_send and spawn_stop.", runID, verb))
	return &resp
}

func errText(err error) string {
	if err == nil {
		return "chat not found"
	}
	return err.Error()
}
