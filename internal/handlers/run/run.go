// Copyright (c) 2025 Reliant Labs
package run

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/rundiagnostics"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runstateproto"
)

// errRunNotReadBack is a run StartRun started but cannot read back: the start
// succeeded, so whatever the read hit is a server fault, never the caller's
// NotFound.
var errRunNotReadBack = svcerr.Internal("run was started but could not be read back")

// runOwnedBy loads a run and verifies the caller owns the chat it belongs to.
//
// Ownership lives on the chat, not the run, so authorization goes through it.
// A run whose chat the caller does not own reports NotFound rather than
// PermissionDenied, matching ChatService — existence is not disclosed.
func (s *Service) runOwnedBy(ctx context.Context, userID, runID string) (*core.Workflow, *core.Chat, error) {
	if runID == "" {
		return nil, nil, svcerr.InvalidArgument("run_id is required")
	}

	run, err := s.deps.Repo.GetWorkflow(ctx, runID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get run"))
	}
	if run == nil {
		return nil, nil, svcerr.NotFound("run")
	}

	chat, err := s.deps.Repo.GetChat(ctx, run.ChatID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get run"))
	}
	if chat == nil || chat.UserID != userID {
		return nil, nil, svcerr.NotFound("run")
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
		State:        runstateproto.State(run.Status.State),
		StopReason:   runstateproto.StopReason(run.Status.StopReason),
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
	if run.FailureDetail != nil {
		out.FailureDetail = *run.FailureDetail
	}
	if run.CompletedAt != nil {
		out.CompletedAtMs = run.CompletedAt.UnixMilli()
	}
	return out
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
	chat, err := s.deps.Repo.GetChat(ctx, msg.SessionId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get session"))
	}
	if chat == nil || chat.UserID != userID {
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

	run, err := s.deps.Repo.GetWorkflow(ctx, resp.Msg.WorkflowId)
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
func (s *Service) defaultProjectID(ctx context.Context, userID string) (string, error) {
	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 1})
	if err != nil {
		return "", svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}
	if len(projects) == 0 {
		return "", svcerr.FailedPrecondition("no project available to host the run; supply session_id or create a project first")
	}
	return projects[0].ID, nil
}

// respondWithSessionRun reads the session's current run and returns it.
func (s *Service) respondWithSessionRun(
	ctx context.Context,
	sessionID string,
) (*connect.Response[reliantv1.StartRunResponse], error) {
	chat, err := s.deps.Repo.GetChat(ctx, sessionID)
	if err != nil || chat == nil || chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, svcerr.WithCause(errRunNotReadBack, err)
	}
	run, err := s.deps.Repo.GetWorkflow(ctx, *chat.WorkflowID)
	if err != nil || run == nil {
		return nil, svcerr.WithCause(errRunNotReadBack, err)
	}
	return connect.NewResponse(&reliantv1.StartRunResponse{Run: runToProto(run)}), nil
}

// SignalRun delivers an event to a run that is already going, and never starts
// one.
//
// This is the half of SendMessage that is not "start". SendMessage decides
// between the two by reading the run's status, so a caller cannot say which it
// meant; here the choice is the RPC. A run that is not live reports
// delivered=false rather than quietly starting a new one. Nothing is persisted
// for an undelivered signal; the caller owns retrying.
func (s *Service) SignalRun(
	ctx context.Context,
	req *connect.Request[reliantv1.SignalRunRequest],
) (*connect.Response[reliantv1.SignalRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	if len(req.Msg.Messages) == 0 {
		return nil, svcerr.InvalidArgument("messages is required")
	}

	// Deliverable means executing, or paused and about to resume. Live() also
	// counts PENDING, but a pending run has never started and SendMessage
	// refuses to start it (the first send is StartChat's), so a signal to it
	// would surface FailedPrecondition instead of delivered=false.
	if !run.Status.Live() || run.Status == db.Pending() {
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

	resp, err := s.deps.Chats.SendMessage(ctx, connect.NewRequest(send))
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
func (s *Service) GetRun(
	ctx context.Context,
	req *connect.Request[reliantv1.GetRunRequest],
) (*connect.Response[reliantv1.GetRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, _, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	rows, err := s.deps.Repo.ListRunNodeExecutions(ctx, run.ID, rundiagnostics.RecordLimit+1)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read run nodes"))
	}
	truncated := len(rows) > rundiagnostics.RecordLimit
	if truncated {
		rows = rows[:rundiagnostics.RecordLimit]
	}
	return connect.NewResponse(&reliantv1.GetRunResponse{
		Run: runToProto(run), Nodes: rundiagnostics.Nodes(rows), NodesTruncated: truncated,
	}), nil
}

// ListRuns lists runs. With session_id or parent_id it lists that session's
// root runs or that run's children (one chat's worth, filtered in memory).
// With neither it is the cross-cutting run list: every root run the caller
// owns, narrowed by the request's filters and paged by keyset.
func (s *Service) ListRuns(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRunsRequest],
) (*connect.Response[reliantv1.ListRunsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	switch {
	case req.Msg.GetParentId() != "":
		// Ownership is checked via the parent, which resolves to a chat.
		if _, _, err := s.runOwnedBy(ctx, userID, req.Msg.GetParentId()); err != nil {
			return nil, err
		}
		children, err := s.deps.Repo.ListChildWorkflows(ctx, req.Msg.GetParentId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
		}
		return connect.NewResponse(filterRunsToProto(children, req.Msg)), nil

	case req.Msg.GetSessionId() != "":
		chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.GetSessionId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get session"))
		}
		if chat == nil || chat.UserID != userID {
			return nil, svcerr.NotFound("session")
		}
		runs, err := s.deps.Repo.ListRootWorkflows(ctx, req.Msg.GetSessionId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
		}
		return connect.NewResponse(filterRunsToProto(runs, req.Msg)), nil

	default:
		return s.listAllRuns(ctx, userID, req.Msg)
	}
}

const (
	defaultRunsPageSize = 50
	maxRunsPageSize     = 200
)

func (s *Service) listAllRuns(
	ctx context.Context,
	userID string,
	req *reliantv1.ListRunsRequest,
) (*connect.Response[reliantv1.ListRunsResponse], error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = defaultRunsPageSize
	}
	if limit > maxRunsPageSize {
		limit = maxRunsPageSize
	}

	filters := db.RunListFilters{
		UserID:          userID,
		ProjectID:       req.ProjectId,
		Workflows:       req.Workflow,
		TriggerID:       req.TriggerId,
		LaunchKinds:     req.LaunchKind,
		Query:           req.Query,
		ParentChatID:    req.ParentChatId,
		IncludeArchived: req.GetIncludeArchived(),
		Limit:           limit,
	}
	for _, st := range req.DisplayStates {
		if st == reliantv1.RunDisplayState_RUN_DISPLAY_STATE_UNSPECIFIED {
			return nil, svcerr.InvalidArgument("display_states must not contain UNSPECIFIED")
		}
		filters.DisplayStates = append(filters.DisplayStates, db.RunDisplayState(st))
	}
	if req.StartedAfter != nil {
		t := req.StartedAfter.AsTime()
		filters.StartedAfter = &t
	}
	if req.StartedBefore != nil {
		t := req.StartedBefore.AsTime()
		filters.StartedBefore = &t
	}
	if req.PageToken != nil && *req.PageToken != "" {
		cursor, err := decodeRunCursor(*req.PageToken)
		if err != nil {
			return nil, svcerr.InvalidArgument("invalid page_token")
		}
		filters.After = cursor
	}

	items, hasMore, err := s.deps.Repo.ListRuns(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
	}
	var next *db.RunCursor
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next = &db.RunCursor{CreatedAt: last.CreatedAt, ChatID: last.ChatID}
	}
	var events []*db.UnlaunchedTriggerEvent
	if req.GetIncludeUnlaunchedEvents() {
		var eventsMore bool
		events, eventsMore, err = s.deps.Repo.ListUnlaunchedTriggerEvents(ctx, filters)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
		}
		items, events, next = mergeRunLog(items, hasMore, events, eventsMore, limit)
	}

	resp := runLogResponse(items, events)
	if next != nil {
		resp.NextPageToken = encodeRunCursor(*next)
	}
	return connect.NewResponse(resp), nil
}

func runLogResponse(items []*db.RunListItem, events []*db.UnlaunchedTriggerEvent) *reliantv1.ListRunsResponse {
	resp := &reliantv1.ListRunsResponse{Runs: make([]*reliantv1.Run, 0, len(items))}
	for _, item := range items {
		resp.Runs = append(resp.Runs, runItemToProto(item))
	}
	for _, event := range events {
		resp.UnlaunchedEvents = append(resp.UnlaunchedEvents, unlaunchedEventToProto(event))
	}
	return resp
}

// mergeRunLog makes one page of the run log from a page of runs and a page
// of unlaunched trigger events, each fetched with the same keyset cursor and
// up to limit rows: the newest limit of the two together, by (time, id)
// descending, and the cursor the next page continues from — nil on the last
// page. A run's time is its start, an event's the time its firing was for.
//
// Either source having more rows than it returned means the page may be cut
// short; so does the merge dropping rows past limit. In both cases the
// cursor is the last row KEPT, so the next page resumes exactly where this
// one stopped and nothing either source returned past it is lost: it comes
// back on the next page.
func mergeRunLog(
	runs []*db.RunListItem, runsMore bool,
	events []*db.UnlaunchedTriggerEvent, eventsMore bool,
	limit int,
) ([]*db.RunListItem, []*db.UnlaunchedTriggerEvent, *db.RunCursor) {
	keptRuns := make([]*db.RunListItem, 0, len(runs))
	keptEvents := make([]*db.UnlaunchedTriggerEvent, 0, len(events))
	var last *db.RunCursor
	i, j := 0, 0
	for len(keptRuns)+len(keptEvents) < limit && (i < len(runs) || j < len(events)) {
		takeRun := j >= len(events)
		if i < len(runs) && j < len(events) {
			run, event := runs[i], events[j]
			takeRun = run.CreatedAt.After(event.OccurredAt) ||
				(run.CreatedAt.Equal(event.OccurredAt) && run.ChatID > event.ID)
		}
		if takeRun {
			keptRuns = append(keptRuns, runs[i])
			last = &db.RunCursor{CreatedAt: runs[i].CreatedAt, ChatID: runs[i].ChatID}
			i++
		} else {
			keptEvents = append(keptEvents, events[j])
			last = &db.RunCursor{CreatedAt: events[j].OccurredAt, ChatID: events[j].ID}
			j++
		}
	}
	more := runsMore || eventsMore || i < len(runs) || j < len(events)
	if !more || last == nil {
		return keptRuns, keptEvents, nil
	}
	return keptRuns, keptEvents, last
}

func unlaunchedEventToProto(event *db.UnlaunchedTriggerEvent) *reliantv1.UnlaunchedTriggerEvent {
	outcome := reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_UNSPECIFIED
	switch event.Outcome {
	case core.TriggerEventSkipped:
		outcome = reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_SKIPPED
	case core.TriggerEventFailed:
		outcome = reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_FAILED
	}
	return &reliantv1.UnlaunchedTriggerEvent{
		Id:              event.ID,
		TriggerId:       event.TriggerID,
		TriggerName:     event.TriggerName,
		WorkflowName:    event.WorkflowName,
		WorkflowTrigger: event.WorkflowTrigger,
		ProjectId:       event.ProjectID,
		LaunchKind:      event.Kind,
		OccurredAtMs:    event.OccurredAt.UnixMilli(),
		Outcome:         outcome,
		OutcomeDetail:   event.OutcomeDetail,
	}
}

// LastRunPerWorkflow returns the newest run of each workflow the caller has run.
func (s *Service) LastRunPerWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.LastRunPerWorkflowRequest],
) (*connect.Response[reliantv1.LastRunPerWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)

	items, err := s.deps.Repo.LastRunPerWorkflow(ctx, db.RunListFilters{
		UserID:    userID,
		ProjectID: req.Msg.ProjectId,
		Workflows: req.Msg.Workflow,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
	}
	resp := &reliantv1.LastRunPerWorkflowResponse{Runs: make([]*reliantv1.Run, 0, len(items))}
	for _, item := range items {
		resp.Runs = append(resp.Runs, runItemToProto(item))
	}
	return connect.NewResponse(resp), nil
}

// runItemToProto converts a run-list row, which carries the chat and trigger
// columns on top of the workflow row's.
func runItemToProto(item *db.RunListItem) *reliantv1.Run {
	out := &reliantv1.Run{
		Id:           item.RunID,
		WorkflowName: item.WorkflowName,
		Thread:       item.RunID,
		SessionId:    item.ChatID,
		State:        runstateproto.State(item.RootStatus.State),
		StopReason:   runstateproto.StopReason(item.RootStatus.StopReason),
		Outcome:      item.Outcome,
		CreatedAtMs:  item.CreatedAt.UnixMilli(),
		Title:        item.Title,
		ProjectId:    item.ProjectID,
		LaunchKind:   item.LaunchKind,
		TriggerId:    item.TriggerID,
		TriggerName:  item.TriggerName,
		DaemonId:     item.DaemonID,
		Activity:     reliantv1.ChatActivity(item.Activity),
		DisplayState: reliantv1.RunDisplayState(item.DisplayState),
		Archived:     item.Archived,
	}
	if item.CompletedAt != nil {
		out.CompletedAtMs = item.CompletedAt.UnixMilli()
	}
	if item.ParentChatID != "" {
		out.ParentChatId = &item.ParentChatID
		if item.ParentChatTitle != "" {
			out.ParentChatTitle = &item.ParentChatTitle
		}
	}
	return out
}

// The page token is the last row's (created_at, chat id), base64url-encoded.
// It is opaque to callers; the keyset it encodes is the list's sort key, so a
// row inserted mid-pagination can neither repeat nor be skipped.
func encodeRunCursor(c db.RunCursor) string {
	raw := strconv.FormatInt(c.CreatedAt.UnixMicro(), 10) + "|" + c.ChatID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeRunCursor(token string) (*db.RunCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	micros, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, fmt.Errorf("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, err
	}
	return &db.RunCursor{CreatedAt: time.UnixMicro(n).UTC(), ChatID: id}, nil
}

// filterRunsToProto applies the state filter and paging, then converts.
func filterRunsToProto(runs []*core.Workflow, req *reliantv1.ListRunsRequest) *reliantv1.ListRunsResponse {
	filtered := make([]*core.Workflow, 0, len(runs))
	for _, run := range runs {
		if run == nil {
			continue
		}
		if req.GetState() != reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED &&
			runstateproto.State(run.Status.State) != req.GetState() {
			continue
		}
		filtered = append(filtered, run)
	}

	total := int32(len(filtered))

	windowed := filtered
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
func (s *Service) PauseRun(
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
	resp, err := s.deps.Chats.PauseChat(ctx, connect.NewRequest(&reliantv1.PauseChatRequest{
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
func (s *Service) ResumeRun(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeRunRequest],
) (*connect.Response[reliantv1.ResumeRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	resp, err := s.deps.Chats.ResumeChat(ctx, connect.NewRequest(&reliantv1.ResumeChatRequest{
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
		if current, err := s.deps.Repo.GetWorkflow(ctx, *chat.WorkflowID); err == nil {
			out.Run = runToProto(current)
		}
	}
	return connect.NewResponse(out), nil
}

// CancelRun hard-stops a run. Terminal by intent.
func (s *Service) CancelRun(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelRunRequest],
) (*connect.Response[reliantv1.CancelRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	resp, err := s.deps.Chats.TerminateChat(ctx, connect.NewRequest(&reliantv1.TerminateChatRequest{
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
func (s *Service) InterruptRun(
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

	resp, err := s.deps.Chats.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{
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
