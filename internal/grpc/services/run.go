// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// RunService is the engine's execution API.
//
// It is DELIBERATELY A FAÇADE for now. Every RPC resolves the run to the chat
// that owns it and delegates to ChatService, which holds the real
// implementation. That is what makes this additive: the engine surface starts
// existing without the coding product changing behavior, because there is only
// one implementation and both services call it.
//
// The delegation direction reverses later, once a run can exist without a
// session (see research/ENGINE_SPLIT_PLAN.md — `workflows.chat_id` is NOT NULL
// today, so a chatless run is not yet representable). At that point the logic
// moves here and ChatService becomes the façade. Writing it this way round
// means no behavior is at risk in the meantime.
type RunService struct {
	reliantv1connect.UnimplementedRunServiceHandler

	database db.Repository
	chats    *ChatService
}

// NewRunService creates a RunService over an existing ChatService.
//
// Taking the ChatService rather than reconstructing its dependencies is the
// point: a second construction path would be a second set of behaviors to keep
// in sync, and the guarantee this service needs is that it cannot diverge.
func NewRunService(database db.Repository, chats *ChatService) *RunService {
	return &RunService{database: database, chats: chats}
}

// runOwnedBy loads a run and verifies the caller owns the chat it belongs to.
//
// Ownership lives on the chat, not the run, so authorization goes through it.
// A run whose chat the caller does not own reports NotFound rather than
// PermissionDenied, matching ChatService — existence is not disclosed.
func (s *RunService) runOwnedBy(ctx context.Context, userID, runID string) (*core.Workflow, *core.Chat, error) {
	if runID == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run_id is required"))
	}

	run, err := s.database.GetWorkflow(ctx, runID)
	if err != nil || run == nil {
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run not found"))
	}

	chat, err := s.database.GetChat(ctx, run.ChatID)
	if err != nil || chat == nil {
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run not found"))
	}
	if chat.UserID != userID {
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run not found"))
	}

	return run, chat, nil
}

// runToProto converts a workflow row to the wire Run.
func runToProto(run *core.Workflow) *reliantv1.Run {
	if run == nil {
		return nil
	}

	out := &reliantv1.Run{
		Id:           run.ID,
		WorkflowName: run.WorkflowName,
		Thread:       run.Thread,
		SessionId:    run.ChatID,
		State:        workflowStateToProto(run.Status.State),
		StopReason:   workflowStopReasonToProto(run.Status.StopReason),
		CreatedAtMs:  run.CreatedAt.UnixMilli(),
	}
	if run.ParentID != nil {
		out.ParentId = *run.ParentID
	}
	if run.SpawnedByNodeID != nil {
		out.SpawnedByNodeId = *run.SpawnedByNodeID
	}
	if run.LoopIteration != nil {
		out.LoopIteration = *run.LoopIteration
	}
	if run.Outcome != nil {
		out.Outcome = *run.Outcome
	}
	if run.CompletedAt != nil {
		out.CompletedAtMs = run.CompletedAt.UnixMilli()
	}
	return out
}

func workflowStateToProto(state core.WorkflowState) reliantv1.WorkflowState {
	switch state {
	case core.WorkflowStatePending:
		return reliantv1.WorkflowState_WORKFLOW_STATE_PENDING
	case core.WorkflowStateActive:
		return reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE
	case core.WorkflowStateStopped:
		return reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED
	default:
		return reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED
	}
}

func workflowStopReasonToProto(reason core.WorkflowStopReason) reliantv1.WorkflowStopReason {
	switch reason {
	case core.StopReasonCompleted:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED
	case core.StopReasonFailed:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED
	case core.StopReasonPaused:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED
	case core.StopReasonCancelled:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED
	default:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED
	}
}

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
// run with no messages at all, which CreateChat and SendMessage both reject.
func (s *RunService) StartRun(
	ctx context.Context,
	req *connect.Request[reliantv1.StartRunRequest],
) (*connect.Response[reliantv1.StartRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Workflow == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow is required"))
	}

	if req.Msg.SessionId != "" {
		return s.startInSession(ctx, userID, req.Msg)
	}
	return s.startWithNewSession(ctx, userID, req.Msg)
}

// startInSession runs a workflow inside an existing session.
func (s *RunService) startInSession(
	ctx context.Context,
	userID string,
	msg *reliantv1.StartRunRequest,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	chat, err := s.database.GetChat(ctx, msg.SessionId)
	if err != nil || chat == nil || chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
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

	if _, err := s.chats.SendMessage(ctx, connect.NewRequest(send)); err != nil {
		return nil, err
	}

	// Re-read rather than trusting the response's ids: SendMessage may have
	// resumed an existing run instead of starting one, and the run that is now
	// live is whatever the session points at.
	return s.respondWithSessionRun(ctx, msg.SessionId)
}

// startWithNewSession creates a session to host a run.
//
// The project requirement is the honest edge of this API today. CreateChat
// demands a project_id, so a caller who supplies none gets their default
// project rather than a failure — which keeps the headless path usable now and
// is exactly the coupling the run-container work removes.
func (s *RunService) startWithNewSession(
	ctx context.Context,
	userID string,
	msg *reliantv1.StartRunRequest,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	projectID, err := s.defaultProjectID(ctx, userID)
	if err != nil {
		return nil, err
	}

	create := &reliantv1.CreateChatRequest{
		ProjectId:       projectID,
		Workflow:        msg.Workflow,
		Messages:        msg.Messages,
		WorkflowParams:  msg.Inputs,
		SelectedPresets: msg.Presets,
	}
	if msg.Mode != "" {
		create.Mode = &msg.Mode
	}

	resp, err := s.chats.CreateChat(ctx, connect.NewRequest(create))
	if err != nil {
		return nil, err
	}

	run, err := s.database.GetWorkflow(ctx, resp.Msg.WorkflowId)
	if err != nil || run == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run was started but could not be read back"))
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
func (s *RunService) defaultProjectID(ctx context.Context, userID string) (string, error) {
	projects, err := s.database.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 1})
	if err != nil || len(projects) == 0 {
		return "", connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("no project available to host the run; supply session_id or create a project first"))
	}
	return projects[0].ID, nil
}

// respondWithSessionRun reads the session's current run and returns it.
func (s *RunService) respondWithSessionRun(
	ctx context.Context,
	sessionID string,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	chat, err := s.database.GetChat(ctx, sessionID)
	if err != nil || chat == nil || chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run was started but could not be read back"))
	}
	run, err := s.database.GetWorkflow(ctx, *chat.WorkflowID)
	if err != nil || run == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run was started but could not be read back"))
	}
	return connect.NewResponse(&reliantv1.StartRunResponse{Run: runToProto(run)}), nil
}

// SignalRun delivers an event to a run that is already going, and never starts
// one.
//
// This is the half of SendMessage that is not "start". SendMessage decides
// between the two by reading the run's status, so a caller cannot say which it
// meant; here the choice is the RPC. A run that is not live reports
// delivered=false rather than quietly starting a new one — the messages are
// still persisted, so the next run reads them.
func (s *RunService) SignalRun(
	ctx context.Context,
	req *connect.Request[reliantv1.SignalRunRequest],
) (*connect.Response[reliantv1.SignalRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	if len(req.Msg.Messages) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("messages is required"))
	}

	// Live means executing, or will execute again on its own — the same
	// predicate the send path uses to decide whether a run can receive work.
	if !run.Status.Live() {
		return connect.NewResponse(&reliantv1.SignalRunResponse{Delivered: false}), nil
	}

	thread := req.Msg.Thread
	if thread == "" {
		thread = run.Thread
	}

	send := &reliantv1.SendMessageRequest{
		ChatId:       chat.ID,
		Messages:     req.Msg.Messages,
		TargetThread: &thread,
	}

	resp, err := s.chats.SendMessage(ctx, connect.NewRequest(send))
	if err != nil {
		return nil, err
	}

	out := &reliantv1.SignalRunResponse{Delivered: true}
	if resp.Msg.MessageId != "" {
		out.MessageIds = []string{resp.Msg.MessageId}
	}
	return connect.NewResponse(out), nil
}

// GetRun returns one run's current state.
func (s *RunService) GetRun(
	ctx context.Context,
	req *connect.Request[reliantv1.GetRunRequest],
) (*connect.Response[reliantv1.GetRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, _, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetRunResponse{Run: runToProto(run)}), nil
}

// ListRuns lists runs for a session, or the children of a parent run.
//
// One of session_id or parent_id is required. An unscoped list is deliberately
// refused rather than returning every run the caller owns: until runs carry a
// tenant of their own, "all runs" would mean a full scan filtered in memory,
// which is the kind of endpoint that looks fine until a workspace is large.
func (s *RunService) ListRuns(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRunsRequest],
) (*connect.Response[reliantv1.ListRunsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	switch {
	case req.Msg.ParentId != "":
		// Ownership is checked via the parent, which resolves to a chat.
		if _, _, err := s.runOwnedBy(ctx, userID, req.Msg.ParentId); err != nil {
			return nil, err
		}
		children, err := s.database.ListChildWorkflows(ctx, req.Msg.ParentId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list runs"))
		}
		return connect.NewResponse(filterRunsToProto(children, req.Msg)), nil

	case req.Msg.SessionId != "":
		chat, err := s.database.GetChat(ctx, req.Msg.SessionId)
		if err != nil || chat == nil || chat.UserID != userID {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
		}
		runs, err := s.database.ListRootWorkflows(ctx, req.Msg.SessionId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list runs"))
		}
		return connect.NewResponse(filterRunsToProto(runs, req.Msg)), nil

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("one of session_id or parent_id is required"))
	}
}

// filterRunsToProto applies the state filter and paging, then converts.
func filterRunsToProto(runs []*core.Workflow, req *reliantv1.ListRunsRequest) *reliantv1.ListRunsResponse {
	filtered := make([]*core.Workflow, 0, len(runs))
	for _, run := range runs {
		if run == nil {
			continue
		}
		if req.State != reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED &&
			workflowStateToProto(run.Status.State) != req.State {
			continue
		}
		filtered = append(filtered, run)
	}

	total := int32(len(filtered))

	offset := int(req.Offset)
	if offset > len(filtered) {
		offset = len(filtered)
	}
	windowed := filtered[offset:]
	if req.Limit > 0 && int(req.Limit) < len(windowed) {
		windowed = windowed[:req.Limit]
	}

	out := make([]*reliantv1.Run, 0, len(windowed))
	for _, run := range windowed {
		out = append(out, runToProto(run))
	}
	return &reliantv1.ListRunsResponse{Runs: out, Total: total}
}

// PauseRun parks a run, leaving it live and resumable.
func (s *RunService) PauseRun(
	ctx context.Context,
	req *connect.Request[reliantv1.PauseRunRequest],
) (*connect.Response[reliantv1.PauseRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	// Delegated so the tool-cancel-before-pause ordering is not duplicated:
	// tools must be cancelled BEFORE the workflow is freed to re-dispatch, or a
	// successor step can start while a non-idempotent tool is still running.
	resp, err := s.chats.PauseChat(ctx, connect.NewRequest(&reliantv1.PauseChatRequest{
		ChatId: chat.ID,
	}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PauseRunResponse{
		Success: resp.Msg.Success,
		Message: resp.Msg.Message,
	}), nil
}

// ResumeRun continues a paused run from its recorded position.
func (s *RunService) ResumeRun(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeRunRequest],
) (*connect.Response[reliantv1.ResumeRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	resp, err := s.chats.ResumeChat(ctx, connect.NewRequest(&reliantv1.ResumeChatRequest{
		ChatId: chat.ID,
	}))
	if err != nil {
		return nil, err
	}

	// Re-read rather than echoing the request's run id: resuming an interrupted
	// run can start a NEW execution that continues from the checkpoint, so the
	// live run is whatever the chat now points at.
	out := &reliantv1.ResumeRunResponse{
		Success: resp.Msg.Success,
		Message: resp.Msg.Message,
	}
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		if current, err := s.database.GetWorkflow(ctx, *chat.WorkflowID); err == nil {
			out.Run = runToProto(current)
		}
	}
	return connect.NewResponse(out), nil
}

// CancelRun hard-stops a run. Terminal by intent.
func (s *RunService) CancelRun(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelRunRequest],
) (*connect.Response[reliantv1.CancelRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	resp, err := s.chats.TerminateChat(ctx, connect.NewRequest(&reliantv1.TerminateChatRequest{
		ChatId: chat.ID,
	}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CancelRunResponse{
		Success: resp.Msg.Success,
		Message: resp.Msg.Message,
	}), nil
}

// InterruptRun stops what a run is doing now without ending it.
func (s *RunService) InterruptRun(
	ctx context.Context,
	req *connect.Request[reliantv1.InterruptRunRequest],
) (*connect.Response[reliantv1.InterruptRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	// Empty thread means "this run's own thread", which is the useful default
	// for a run-addressed API. ChatService's equivalent is chat-addressed and
	// takes the thread explicitly.
	thread := req.Msg.Thread
	if thread == "" {
		thread = run.Thread
	}

	resp, err := s.chats.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{
		ChatId:   chat.ID,
		ThreadId: thread,
	}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.InterruptRunResponse{
		CancelledToolCalls:     resp.Msg.CancelledToolCalls,
		UndeliverableToolCalls: resp.Msg.UndeliverableToolCalls,
	}), nil
}
