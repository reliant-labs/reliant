package run

import (
	"context"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// errRunNotReadBack is a run StartRun started but cannot read back: the start
// succeeded, so whatever the read hit is a server fault, never the caller's
// NotFound.
var errRunNotReadBack = svcerr.Internal("run was started but could not be read back")

// StartRun starts a workflow execution.
//
// Two paths, and which one runs depends only on whether a session was supplied:
//
//   - With a session, this is "run this workflow in that conversation", which is
//     what SendMessage does when the session has no live run.
//   - Without one, a session is created to host the run. That is a CONCESSION TO
//     TODAY'S SCHEMA, not the intended design: `workflows.chat_id` is NOT NULL,
//     so a run must belong to something. When the run container lands (see
//     research/ENGINE_SPLIT_PLAN.md) this branch stops creating a session and
//     the headless run becomes genuinely chatless.
//
// The message requirement is already gone at this layer: a caller may start a
// run with no messages at all, which StartChat and SendMessage both reject.
func (s *Service) StartRun(
	ctx context.Context,
	req *connect.Request[reliantv1.StartRunRequest],
) (*connect.Response[reliantv1.StartRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Workflow == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}

	if req.Msg.SessionId != "" {
		return s.startInSession(ctx, userID, req.Msg)
	}
	return s.startWithNewSession(ctx, userID, req.Msg)
}

// startInSession runs a workflow inside an existing session.
func (s *Service) startInSession(
	ctx context.Context,
	userID string,
	msg *reliantv1.StartRunRequest,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	chat, err := s.deps.ChatViews.GetChat(ctx, msg.SessionId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get session"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("session")
	}

	// A session that has never started (a branch) is started, not continued:
	// SendMessage rejects it.
	if chat.RootStatus.State == core.WorkflowStatePending {
		start := &reliantv1.StartChatRequest{
			ChatId:          &msg.SessionId,
			Workflow:        msg.Workflow,
			Messages:        msg.Messages,
			WorkflowParams:  msg.Inputs,
			SelectedPresets: msg.Presets,
		}
		if msg.Mode != "" {
			start.Mode = &msg.Mode
		}
		if _, err := s.deps.Chats.StartChat(ctx, connect.NewRequest(start)); err != nil {
			return nil, err
		}
		return s.respondWithSessionRun(ctx, msg.SessionId)
	}

	send := &reliantv1.SendMessageRequest{
		ChatId:          msg.SessionId,
		Messages:        msg.Messages,
		WorkflowParams:  msg.Inputs,
		SelectedPresets: msg.Presets,
	}
	if msg.Mode != "" {
		send.Mode = &msg.Mode
	}

	if _, err := s.deps.Chats.SendMessage(ctx, connect.NewRequest(send)); err != nil {
		return nil, err
	}

	// Re-read rather than trusting the response's ids: SendMessage may have
	// resumed an existing run instead of starting one, and the run that is now
	// live is whatever the session points at.
	return s.respondWithSessionRun(ctx, msg.SessionId)
}

// startWithNewSession creates a session to host a run.
//
// The project requirement is the honest edge of this API today. StartChat
// demands a project_id, so a caller who supplies none gets their default
// project rather than a failure — which keeps the headless path usable now and
// is exactly the coupling the run-container work removes.
func (s *Service) startWithNewSession(
	ctx context.Context,
	userID string,
	msg *reliantv1.StartRunRequest,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	projectID, err := s.defaultProjectID(ctx, userID)
	if err != nil {
		return nil, err
	}

	create := &reliantv1.StartChatRequest{
		ProjectId:       projectID,
		Workflow:        msg.Workflow,
		Messages:        msg.Messages,
		WorkflowParams:  msg.Inputs,
		SelectedPresets: msg.Presets,
	}
	if msg.Mode != "" {
		create.Mode = &msg.Mode
	}

	resp, err := s.deps.Chats.StartChat(ctx, connect.NewRequest(create))
	if err != nil {
		return nil, err
	}

	run, err := s.deps.Workflows.LoadWorkflow(ctx, resp.Msg.WorkflowId)
	if err != nil || run == nil {
		return nil, svcerr.WithCause(errRunNotReadBack, err)
	}
	return connect.NewResponse(&reliantv1.StartRunResponse{Run: runToProto(run)}), nil
}

// defaultProjectID resolves the project a headless run is hosted in.
//
// Most-recently-active wins. The choice barely matters and is deliberately not
// configurable, because it exists only to satisfy `chats.project_id NOT NULL`
// — a headless run does not belong to a project in any meaningful sense, and
// giving callers a knob here would make a temporary coupling look intentional.
// The branch disappears with the run container.
//
// Only a project the caller may still run work in hosts it: not one of an org
// they have left, nor of a credential's other org.
func (s *Service) defaultProjectID(ctx context.Context, userID string) (string, error) {
	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: defaultProjectScan})
	if err != nil {
		return "", svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}
	for _, p := range projects {
		if authz.CanWriteProject(ctx, userID, p) {
			return p.ID, nil
		}
	}
	return "", svcerr.FailedPrecondition("no project available to host the run; supply session_id or create a project first")
}

// defaultProjectScan bounds how many of the caller's most recently active
// projects defaultProjectID reads to find one it may host a run in.
const defaultProjectScan = 200

// respondWithSessionRun reads the session's current run and returns it.
func (s *Service) respondWithSessionRun(
	ctx context.Context,
	sessionID string,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	chat, err := s.deps.ChatViews.GetChat(ctx, sessionID)
	if err != nil || chat == nil || chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, svcerr.WithCause(errRunNotReadBack, err)
	}
	run, err := s.deps.Workflows.LoadWorkflow(ctx, *chat.WorkflowID)
	if err != nil || run == nil {
		return nil, svcerr.WithCause(errRunNotReadBack, err)
	}
	return connect.NewResponse(&reliantv1.StartRunResponse{Run: runToProto(run)}), nil
}
