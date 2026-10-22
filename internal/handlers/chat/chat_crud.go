// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/planproto"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// startChatIDNamespace derives the id of a chat a client named its first
// message for (StartChatRequest.client_message_id).
var startChatIDNamespace = uuid.MustParse("c1d6c3a4-5f0e-4b7a-9a2d-8e3b7f4a1c59")

// StartChat starts a chat's root run: it creates the chat, or starts an
// existing pending one (a branch's first send).
//
// The start path itself lives in internal/launch, because a scheduled trigger
// fires on the worker and the worker cannot import this package. What is left
// here is the handler's own job: read the caller from auth, translate the wire
// request into a launch.Spec, and translate launch's domain errors back into
// the connect codes this RPC has always returned.
func (s *Service) StartChat(
	ctx context.Context,
	req *connect.Request[reliantv1.StartChatRequest],
) (*connect.Response[reliantv1.StartChatResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// The launch kind is the server's decision. A client can ask for exactly
	// one non-default kind, builder.test, through builder_test; schedule,
	// agent.start_run and manual.run have no way in through this RPC.
	eventKind := core.TriggerEventKindChatStart
	if req.Msg.GetBuilderTest() {
		if err := s.validateBuilderTest(ctx, userID, req.Msg); err != nil {
			return nil, err
		}
		eventKind = core.TriggerEventKindBuilderTest
	}
	return s.startChat(ctx, userID, req.Msg, eventKind, nil)
}

// startChat is the start path every attended launch kind shares: StartChat's
// chat.start and builder.test, and RunNow's manual.run. extraPayload is merged
// into the launch event's payload next to the counts.
func (s *Service) startChat(
	ctx context.Context,
	userID string,
	msg *reliantv1.StartChatRequest,
	eventKind core.TriggerEventKind,
	extraPayload map[string]any,
) (*connect.Response[reliantv1.StartChatResponse], error) {
	clientID, err := parseClientMessageID(msg.GetClientMessageId())
	if err != nil {
		return nil, err
	}

	// An interactive chat start is a trigger event too, of kind chat.start: it
	// has no stored definition, and its dedupe key is the chat id, which is what
	// makes "a chat starts exactly once" a database invariant rather than
	// client discipline. The id is minted here so the event row and the chat
	// row agree by construction. The payload carries counts, never message
	// text: the messages table already holds that.
	chatID := msg.GetChatId()
	newChatID := ""
	if chatID == "" {
		newChatID = uuid.NewString()
		if clientID != "" {
			// A start the client named is the same chat however many times it
			// is sent: the chat id, which is also this launch's dedupe key,
			// follows from the message id. Scoped to the caller, so another
			// user's start under the same id is a different chat.
			newChatID = uuid.NewSHA1(startChatIDNamespace, []byte(userID+":"+clientID)).String()
		}
		chatID = newChatID
	}
	payload := map[string]any{
		"message_count":    len(msg.Messages),
		"attachment_count": len(msg.Attachments),
	}
	for key, value := range extraPayload {
		payload[key] = value
	}
	event := launch.Event{
		Kind:       eventKind,
		DedupeKey:  chatID,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}

	spec := launch.Spec{
		OwnerUserID: userID,
		ProjectID:   msg.ProjectId,
		WorktreeID:  msg.WorktreeId,
		ChatID:      msg.GetChatId(),
		NewChatID:   newChatID,
		Title:       msg.Title,
		Workflow:    msg.Workflow,
		Presets:     msg.SelectedPresets,
		Params:      msg.WorkflowParams,
		Mode:        msg.Mode,
		Messages:    seedMessagesFromInput(msg.Messages),
		Attachments: msg.Attachments,

		ClientMessageID: clientID,

		// An interactive start always had both of these, and still does. A
		// branch is not a first turn of new work, so it skips the probe.
		GenerateTitle:   true,
		GreenfieldProbe: msg.GetChatId() == "",
	}
	if jwt, ok := auth.GetUserJWT(userID); ok {
		spec.UserJWT = jwt
	}

	// A chat with no machine by design: there is nothing to choose, validate
	// or wake. The launcher refuses the contradictions (with a daemon_id, or
	// a branch) and a workflow that cannot run without a machine.
	spec.NoMachine = msg.GetNoMachine()

	// The chat the wake should target: a branch's own chat (pinned by its
	// worktree) or, for a new chat, one that carries the chosen daemon.
	var wakeChat *db.Chat
	chosen := msg.GetDaemonId()
	if spec.NoMachine {
		// Left for the launcher to reject alongside no_machine.
		spec.DaemonID = chosen
		chosen = ""
	}
	if id := msg.GetChatId(); id != "" && !spec.NoMachine {
		// A branch's first send.
		if branch, getErr := s.getChatForUser(ctx, id, userID); getErr == nil {
			wakeChat = branch
		}
	}
	if chosen != "" {
		projectID := msg.ProjectId
		if wakeChat != nil {
			projectID = wakeChat.ProjectID
			// The branch's worktree lives on one machine, so a pinned chat's
			// daemon is authoritative; a different choice cannot be honored.
			if pinned := wakeChat.ActiveDaemonID; pinned != nil && *pinned != "" && *pinned != chosen {
				return nil, svcerr.InvalidArgument("daemon_id conflicts with the chat's pinned daemon: a branch runs on its worktree's machine")
			}
		}
		if err := daemonchoice.Validate(ctx, s.deps.Repo, userID, projectID, chosen); err != nil {
			return nil, err
		}
		spec.DaemonID = chosen
		if wakeChat == nil {
			wakeChat = &db.Chat{ActiveDaemonID: &chosen}
		} else {
			pinnedCopy := *wakeChat
			pinnedCopy.ActiveDaemonID = &chosen
			wakeChat = &pinnedCopy
		}
	}
	// A nil wakeChat still wakes the user's default machine, so a run with no
	// machine skips the wake outright rather than relying on the chat row.
	if !spec.NoMachine {
		s.wakeDaemonForAttendedTurn(ctx, userID, wakeChat)
	}

	if clientID != "" {
		// Attempts of one named start run one after another, so the second
		// finds the first's launch finished and replays it. Without this the
		// loser of a race finishes the start itself, and only Temporal's
		// USE_EXISTING stands between that and a second run: a first run that
		// completes before the loser reaches Temporal would be started again.
		// The chat id is known up front, new or named, so the lock is the same
		// for every attempt of this send.
		defer s.runs.LockRunControl(ctx, chatID)()
	}

	result, err := s.launcher().Launch(ctx, event, spec)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start chat"))
	}

	return connect.NewResponse(&reliantv1.StartChatResponse{
		Chat:       s.chatToProtoWithSelection(ctx, result.Chat),
		WorkflowId: result.WorkflowID,
		RunId:      result.RunID,
	}), nil
}

// validateBuilderTest checks a builder_test start: it names a workflow draft
// the caller owns, and it starts a new chat. The draft is what the builder just
// saved; running someone else's draft, or a builtin or project workflow, is not
// a test run of anything the caller is editing.
func (s *Service) validateBuilderTest(ctx context.Context, userID string, msg *reliantv1.StartChatRequest) error {
	if msg.GetChatId() != "" {
		return svcerr.InvalidArgument("builder_test starts a new chat; chat_id must be empty")
	}
	if msg.Workflow == "" {
		return svcerr.InvalidArgument("builder_test requires the workflow to run")
	}
	draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, launch.NormalizeWorkflowSlug(msg.Workflow))
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to look up workflow draft"))
	}
	if draft == nil || draft.UserID != userID {
		return svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("builder_test requires a saved workflow draft you own: %q", msg.Workflow))
	}
	return nil
}

// ListChats lists all non-archived chats for a project
func (s *Service) ListChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatsRequest],
) (*connect.Response[reliantv1.ListChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	limit := int(100)
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	filters := db.ChatFilters{
		UserID:          userID,
		ProjectID:       &req.Msg.ProjectId,
		Limit:           limit,
		ExcludeArchived: true,
		SidebarOnly:     req.Msg.GetSidebarOnly(),
	}

	chats, err := s.deps.Repo.ListChats(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list chats"))
	}

	// The background reconciler repairs stale DB status against Temporal.
	protoChats := make([]*reliantv1.Chat, len(chats))
	for i, c := range chats {
		protoChats[i] = chatToProto(c)
	}
	mainWorktree := s.attachWorktrees(ctx, userID, req.Msg.ProjectId, chats, protoChats)

	// Get latest user update sequence for stream sync —
	// the frontend uses this to connect the streaming RPC without redundant event replay.
	latestSeq, err := s.deps.Repo.GetLatestUserUpdateSequence(ctx, userID)
	if err != nil {
		// Non-fatal: log and return 0 so the frontend falls back to full replay
		logging.WarnContext(ctx, "Failed to get latest user update sequence", "error", err, "userID", userID)
	}

	return connect.NewResponse(&reliantv1.ListChatsResponse{
		Chats:                  protoChats,
		Total:                  int32(len(protoChats)),
		LastUserUpdateSequence: latestSeq,
		MainWorktree:           mainWorktree,
	}), nil
}

// GetChat retrieves a specific chat by ID
func (s *Service) GetChat(
	ctx context.Context,
	req *connect.Request[reliantv1.GetChatRequest],
) (*connect.Response[reliantv1.GetChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Verify user owns the chat
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Check for ghost workflows (DB says running, but Temporal lost it).
	// GetChat is a READ operation — it does NOT reconcile workflow status.
	// Status transitions are handled by WorkflowStatus("completed") activity
	// callbacks and EnsureWorkflowRunning belt-and-suspenders in ActivityWrapper.
	needsRecovery := false
	if workflowID := chat.MainThreadID(); workflowID != "" {
		// Check if the root workflow needs ghost recovery
		wf, wfErr := s.deps.Repo.GetWorkflow(ctx, workflowID)
		if wfErr == nil && wf != nil && wf.Status == db.Active() {
			// DB says running — verify Temporal agrees
			temporalState, err := s.runs.State(ctx, workflowID)
			if err != nil {
				logging.WarnContext(ctx, "Failed to query Temporal for workflow status",
					"error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
			} else if !temporalState.Exists {
				// Ghost workflow — Temporal lost it but DB says running
				needsRecovery = true
			}
			// If Temporal says completed but DB says running, DON'T reconcile here.
			// The WorkflowStatus("completed") activity callback is the authoritative
			// path for transitioning DB status. The EnsureWorkflowRunning belt-and-
			// suspenders in ActivityWrapper handles the reverse case.
		}
	}

	// Carries the composer's choice for this chat, so any device opens on it.
	protoChat := s.chatToProtoWithSelection(ctx, chat)
	protoChat.NeedsRecovery = needsRecovery
	return connect.NewResponse(&reliantv1.GetChatResponse{
		Chat: protoChat,
	}), nil
}

// UpdateChat updates chat metadata
func (s *Service) UpdateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatRequest],
) (*connect.Response[reliantv1.UpdateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Update fields
	// NOTE: model/temperature/max_tokens are workflow input params, not stored on chat
	if req.Msg.Title != nil {
		chat.Title = *req.Msg.Title
	}
	if req.Msg.WorktreeId != nil {
		// Same invariant as creation: a chat may be moved between the project's
		// worktrees, but never to a foreign one and never cleared to null.
		resolved, err := s.launcher().ResolveChatWorktreeID(ctx, chat.ProjectID, req.Msg.WorktreeId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve worktree"))
		}
		chat.WorktreeID = resolved
	}

	// Handle workflow name change - only allowed when root workflow is pending
	if req.Msg.WorkflowName != nil && *req.Msg.WorkflowName != "" {
		newWorkflowName := *req.Msg.WorkflowName

		// Validate workflow exists
		// A failure to read it is classified; anything else is a verdict.
		if _, err := s.launcher().LoadWorkflowForValidation(ctx, newWorkflowName, chat.ProjectID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", newWorkflowName)))
		}

		// Check if root workflow is pending (chat hasn't started)
		if chat.WorkflowID == nil || *chat.WorkflowID == "" {
			return nil, svcerr.FailedPrecondition("cannot change workflow: chat has no workflow assigned")
		}

		rootWorkflow, err := s.deps.Repo.GetWorkflow(ctx, *chat.WorkflowID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get root workflow"))
		}

		if rootWorkflow.Status != db.Pending() {
			return nil, svcerr.FailedPrecondition("cannot change workflow after chat has started - use Branch instead")
		}

		// Update workflow name on the workflow record
		if err := s.deps.Repo.UpdateWorkflowName(ctx, *chat.WorkflowID, newWorkflowName); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow"))
		}

		// Update chat's workflow name
		chat.WorkflowName = &newWorkflowName
	}

	chat.UpdatedAt = time.Now().UTC()
	chat.LastActive = time.Now().UTC()

	if err := s.deps.Repo.UpdateChat(ctx, chat); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat"))
	}

	// Emit chat_title_changed if title was updated
	if req.Msg.Title != nil {
		titleData, _ := json.Marshal(map[string]string{
			"chat_id": chat.ID,
			"title":   chat.Title,
		})
		if err := s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatTitleChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       titleData,
		}); err != nil {
			logging.WarnContext(ctx, "Failed to emit chat_title_changed", "error", err)
		}
	}

	// Emit chat_config_changed to user_updates when workflow_name changes.
	// (Replaces the dropped user_updates_chat_config_update SQL trigger.)
	if req.Msg.WorkflowName != nil && *req.Msg.WorkflowName != "" {
		configData, _ := json.Marshal(map[string]interface{}{
			"chat_id":       chat.ID,
			"workflow_name": *chat.WorkflowName,
		})
		if err := s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       configData,
		}); err != nil {
			logging.WarnContext(ctx, "Failed to emit chat_config_changed", "error", err)
		}
	}

	// Emit a chat-level update when any of workflow_name/state/title change.
	// (Replaces the dropped chat_updates_chat_update SQL trigger.)
	// State and title changes already produce their own specific chat_updates
	// via UpdateChatState and the message paths, but workflow_name changes
	// previously only had the trigger. Emit a general CHAT_UPDATE for it.
	if req.Msg.WorkflowName != nil || req.Msg.Title != nil {
		var wfName string
		if chat.WorkflowName != nil {
			wfName = *chat.WorkflowName
		}
		chatUpdateData, _ := json.Marshal(map[string]interface{}{
			"update_type":   int(db.UpdateTypeChat),
			"chat_id":       chat.ID,
			"workflow_name": wfName,
			"state":         int(chat.State),
			"title":         chat.Title,
		})
		if err := s.deps.Repo.CreateChatUpdate(ctx, chat.ID, db.UpdateTypeChat, chat.ID, string(chatUpdateData)); err != nil {
			logging.WarnContext(ctx, "Failed to emit chat_update for config change", "error", err)
		}
	}

	updateChatProto := chatToProto(chat)
	return connect.NewResponse(&reliantv1.UpdateChatResponse{
		Chat: updateChatProto,
	}), nil
}

// DeleteChat archives or permanently deletes a chat
func (s *Service) DeleteChat(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteChatRequest],
) (*connect.Response[reliantv1.DeleteChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// If already archived, hard delete permanently
	if chat.State == db.ChatStateArchived {
		// Cancel workflow if still running (belt-and-suspenders for hard delete)
		if workflowID := chat.MainThreadID(); workflowID != "" {
			_ = s.deps.Temporal.CancelWorkflow(ctx, workflowID, "")
		}

		if err := s.deps.Repo.DeleteChat(ctx, req.Msg.ChatId); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to permanently delete chat"))
		}

		return connect.NewResponse(&reliantv1.DeleteChatResponse{
			Success:            true,
			Message:            "Chat permanently deleted",
			PermanentlyDeleted: true,
		}), nil
	}

	// Otherwise, archive the chat
	if err := s.deps.Repo.UpdateChatState(ctx, req.Msg.ChatId, db.ChatStateArchived, "user_archived"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to archive chat"))
	}

	s.cancelArchivedChatWorkflow(ctx, chat)

	return connect.NewResponse(&reliantv1.DeleteChatResponse{
		Success:            true,
		Message:            "Chat archived successfully",
		PermanentlyDeleted: false,
	}), nil
}

// cancelArchivedChatWorkflow stops the run of a chat that was just archived.
//
// Archiving wants the run gone, so a run that is already gone is success:
// Temporal answers NotFound both for a run that finished ("workflow execution
// already completed") and for one it no longer has ("workflow execution not
// found"). Archiving an idle chat — the common case — is exactly that, and
// logging it at ERROR made every archive of a finished chat a Sentry event
// (GO-M). Any other failure leaves a run going for a chat the user put away,
// so it stays an ERROR.
func (s *Service) cancelArchivedChatWorkflow(ctx context.Context, chat *db.Chat) {
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return
	}
	runID := ""
	if chat.RunID != nil {
		runID = *chat.RunID
	}
	err := s.deps.Temporal.CancelWorkflow(ctx, workflowID, runID)
	var notFound *serviceerror.NotFound
	if err == nil || errors.As(err, &notFound) {
		return
	}
	logging.ErrorContext(ctx, "Failed to cancel workflow for archived chat",
		"error", err,
		"chatID", chat.ID,
		"workflowID", workflowID)
}

// SearchChats searches chats by title and message content
func (s *Service) SearchChats(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchChatsRequest],
) (*connect.Response[reliantv1.SearchChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	// Return empty results if no query
	if req.Msg.Query == "" {
		return connect.NewResponse(&reliantv1.SearchChatsResponse{
			Chats: []*reliantv1.Chat{},
			Total: 0,
		}), nil
	}

	limit := 100
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	filters := db.ChatSearchFilters{
		UserID:      userID,
		ProjectID:   req.Msg.ProjectId,
		SearchQuery: req.Msg.Query,
		Limit:       limit,
	}

	chats, err := s.deps.Repo.SearchChats(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to search chats"))
	}

	protoChats := make([]*reliantv1.Chat, len(chats))
	for i, c := range chats {
		protoChats[i] = chatToProto(c)
	}

	return connect.NewResponse(&reliantv1.SearchChatsResponse{
		Chats: protoChats,
		Total: int32(len(protoChats)),
	}), nil
}

// ListArchivedChats lists all archived chats with worktree info
func (s *Service) ListArchivedChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListArchivedChatsRequest],
) (*connect.Response[reliantv1.ListArchivedChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	archivedChats, err := s.deps.Repo.ListArchivedChats(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list archived chats"))
	}

	protoChats := make([]*reliantv1.ArchivedChat, len(archivedChats))
	for i, ac := range archivedChats {
		acProto := chatToProto(&ac.Chat)
		protoChats[i] = &reliantv1.ArchivedChat{
			Chat: acProto,
		}
		if ac.WorktreeName != nil {
			protoChats[i].WorktreeName = ac.WorktreeName
		}
		if ac.WorktreeDeletedAt != nil {
			deletedAt := ac.WorktreeDeletedAt.Format(time.RFC3339)
			protoChats[i].WorktreeDeletedAt = &deletedAt
		}
	}

	return connect.NewResponse(&reliantv1.ListArchivedChatsResponse{
		Chats: protoChats,
		Total: int32(len(protoChats)),
	}), nil
}

// ListMessages lists messages for a chat with content blocks
// For branched chats, this resolves inherited messages via the context window chain.
func (s *Service) ListMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.ListMessagesRequest],
) (*connect.Response[reliantv1.ListMessagesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Verify user owns the chat
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Parse pagination params
	recentLimit := 0
	if req.Msg.Recent != nil && *req.Msg.Recent > 0 {
		recentLimit = int(*req.Msg.Recent)
	}

	var beforeSeq int64
	if req.Msg.BeforeSeq != nil && *req.Msg.BeforeSeq > 0 {
		beforeSeq = *req.Msg.BeforeSeq
	}

	// Determine the main thread ID for this chat
	mainThreadID := req.Msg.ChatId
	if id := chat.MainThreadID(); id != "" {
		mainThreadID = id
	}

	threadsSvc := threads.NewService(s.deps.Repo)

	var messages []*db.Message
	var totalCount int
	var oldestSeq int64
	var hasMore bool

	// The thread whose point of view these messages are read from; governs
	// tool-call status resolution (see messageproto.Options.ViewingThreadID).
	viewingThreadID := mainThreadID

	switch {
	case req.Msg.ThreadId != nil && *req.Msg.ThreadId != "":
		// Single-thread read: return exactly this thread's visual thread. No
		// merge with siblings and no main-thread window — the caller asked for
		// one thread, so the answer is that thread or an error, never a
		// best-effort slice of something else.
		threadID := *req.Msg.ThreadId
		viewingThreadID = threadID

		thread, err := s.deps.Repo.GetThread(ctx, threadID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get thread"))
		}
		// Ownership is transitive through the chat, which is already
		// authorized above. A thread from another chat is NotFound rather
		// than a silent fall back to the chat-wide list: answering a
		// different question than the one asked is how the spawn preview
		// came to render the wrong thing in the first place.
		if thread.ChatID != req.Msg.ChatId {
			return nil, svcerr.NotFound("thread")
		}

		messages, hasMore, err = s.listThreadMessages(ctx, threadsSvc, threadID, beforeSeq, recentLimit)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}
		// The display count, not the LLM one: listThreadMessages reads the
		// transcript (which crosses compaction boundaries), so a total that
		// stopped at the newest summary would describe a different list than
		// the one being returned.
		totalCount, err = threadsSvc.CountDisplayMessages(ctx, threadID)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}
		if len(messages) > 0 {
			oldestSeq = messages[0].Seq
		}

	case recentLimit > 0:
		messages, totalCount, oldestSeq, hasMore, err = s.listMessagesBounded(
			ctx, threadsSvc, req.Msg.ChatId, mainThreadID, beforeSeq, recentLimit)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}
	default:
		// No page size given: the caller wants the whole chat (e.g. the cold
		// initial load before any pagination has happened). Resolve every
		// thread's full history and hand it to paginateBySeq unbounded.
		messages, totalCount, err = s.listMessagesUnbounded(ctx, threadsSvc, req.Msg.ChatId, mainThreadID)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}

		var page messagePage
		messages, page = paginateBySeq(messages, beforeSeq, recentLimit, mainThreadID)
		oldestSeq, hasMore = page.OldestSeq, page.HasMore
	}

	// Batch-fetch content blocks for the paginated page of messages in one
	// query, then group by message id. This replaces three separate
	// ListContentBlocks (single-message) round trips per message with a
	// single ListContentBlocksForMessages call.
	messageIDs := make([]string, len(messages))
	for i, msg := range messages {
		messageIDs[i] = msg.ID
	}
	allBlocks, err := s.deps.Repo.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		return nil, messagesReadError(ctx, err)
	}
	blocksByMessageID := make(map[string][]*db.MessageContentBlock, len(messages))
	for _, block := range allBlocks {
		blocksByMessageID[block.MessageID] = append(blocksByMessageID[block.MessageID], block)
	}

	// Collect all attachment IDs from image and file_reference blocks
	attachmentIDSet := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		for _, block := range blocksByMessageID[msg.ID] {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				attachmentIDSet[*block.Content] = true
			}
		}
	}

	// Fetch all attachments in bulk
	attachmentIDs := make([]string, 0, len(attachmentIDSet))
	for id := range attachmentIDSet {
		attachmentIDs = append(attachmentIDs, id)
	}
	attachmentMap := make(map[string]*db.Attachment)
	if len(attachmentIDs) > 0 {
		attachmentsData, err := s.deps.Repo.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err == nil {
			for _, att := range attachmentsData {
				attachmentMap[att.ID] = att
			}
		}
	}

	protoMessages := messageproto.AssembleForDisplay(
		ctx, s.deps.Repo, messages, messageIDs, blocksByMessageID, attachmentMap, viewingThreadID, 0)

	return connect.NewResponse(&reliantv1.ListMessagesResponse{
		Messages:  protoMessages,
		Total:     int32(totalCount),
		Count:     int32(len(protoMessages)),
		HasMore:   hasMore,
		OldestSeq: oldestSeq,
	}), nil
}

// UpdateChatState changes chat state (idle/archived)
func (s *Service) UpdateChatState(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatStateRequest],
) (*connect.Response[reliantv1.UpdateChatStateResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Validate state value
	newState := db.ChatState(req.Msg.State)
	switch newState {
	case db.ChatStateIdle, db.ChatStateArchived:
		// Valid user-initiated transitions
	default:
		return nil, svcerr.InvalidArgument("invalid state, must be 'idle' or 'archived'")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	previousState := chat.State

	// Determine reason for state change
	var reason string
	switch newState {
	case db.ChatStateIdle:
		reason = "user_dismissed"
	case db.ChatStateArchived:
		reason = "user_archived"
	}

	// Update the state
	if err := s.deps.Repo.UpdateChatState(ctx, req.Msg.ChatId, newState, reason); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat state"))
	}

	// If unarchiving a chat, also unarchive the associated workspace
	if newState == db.ChatStateIdle && previousState == db.ChatStateArchived && chat.WorktreeID != nil {
		worktree, err := s.deps.Repo.GetWorktree(ctx, *chat.WorktreeID)
		if err == nil && worktree.DeletedAt != nil {
			if err := s.deps.Repo.UnarchiveWorktree(ctx, *chat.WorktreeID); err != nil {
				logging.ErrorContext(ctx, "Failed to unarchive workspace when unarchiving chat",
					"error", err,
					"chatID", req.Msg.ChatId,
					"worktreeID", *chat.WorktreeID)
			}
		}
	}

	// If archiving, also cancel any running workflow
	if newState == db.ChatStateArchived {
		s.cancelArchivedChatWorkflow(ctx, chat)
	}

	return connect.NewResponse(&reliantv1.UpdateChatStateResponse{
		State:         newState,
		PreviousState: previousState,
		Message:       fmt.Sprintf("Chat state updated to %v", newState),
	}), nil
}

// TerminateChat forcefully terminates the running workflow for a chat.
func (s *Service) TerminateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.TerminateChatRequest],
) (*connect.Response[reliantv1.TerminateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	if err := s.runs.Terminate(ctx, req.Msg.ChatId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to terminate workflow"))
	}

	return connect.NewResponse(&reliantv1.TerminateChatResponse{
		Success: true,
		Message: "Chat terminated successfully",
	}), nil
}

// PauseChat pauses the running workflow by sending a pause signal and cancelling in-flight activities.
// The workflow stays alive in Temporal (blocked at the next step boundary) and is resumable.
// In-flight activities are cancelled immediately via the shared cancellable activity context.
func (s *Service) PauseChat(
	ctx context.Context,
	req *connect.Request[reliantv1.PauseChatRequest],
) (*connect.Response[reliantv1.PauseChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, svcerr.FailedPrecondition("chat has no workflow")
	}

	// From here to the paused-status write is one critical section: a
	// SendMessage that arrives while it runs waits, then sees the run paused
	// and resumes it. Taken before the tool cancel, not just around the
	// signal, so the order the server received the two requests in is the
	// order they take effect. See runs.Service.LockRunControl.
	release := s.runs.LockRunControl(ctx, req.Msg.ChatId)
	defer release()

	// Kill the tools BEFORE freeing the workflow to re-dispatch. The pause
	// signal below cancels the ExecuteTools activity at its next step
	// boundary, which frees the workflow to move on -- if that happened
	// first, the successor step could start while a tool from the current
	// step is still provably running on the daemon, and tools are not
	// idempotent. Push the same immediate daemon cancel InterruptThread uses,
	// scoped to every thread of the chat instead of one, and do it first.
	threadsSvc := s.threads
	if threadsSvc == nil {
		threadsSvc = threads.NewService(s.deps.Repo,
			threads.WithTemporalSignaler(s.deps.Temporal),
			threads.WithToolCanceler(s.deps.Router),
		)
	}
	if _, err := threadsSvc.CancelChatToolCalls(ctx, threads.CancelChatToolCallsOpts{
		UserID: userID,
		ChatID: req.Msg.ChatId,
	}); err != nil {
		logging.WarnContext(ctx, "Failed to push tool cancel while pausing; the workflow-level pause still applies",
			"error", err, "chatID", req.Msg.ChatId)
	}

	// A run that had already finished is not an error — the service reconciles
	// the stale row and reports success, because the user's intent is met.
	// A failed cancel push above must not strand the workflow un-paused, so
	// this always runs regardless of that outcome.
	if err := s.runs.Pause(ctx, req.Msg.ChatId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to pause workflow"))
	}

	return connect.NewResponse(&reliantv1.PauseChatResponse{
		Success: true,
		Message: "Chat paused successfully",
	}), nil
}

// ResumeChat resumes a paused workflow by restarting its dedicated worker
// The workflow continues exactly where it left off - all local state is preserved.
func (s *Service) ResumeChat(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeChatRequest],
) (*connect.Response[reliantv1.ResumeChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, svcerr.FailedPrecondition("chat has no workflow")
	}

	// A resume is a signal plus a status write, like a pause; interleaved
	// with one, the row can end up saying the opposite of what the run is
	// doing. See runs.Service.LockRunControl.
	release := s.runs.LockRunControl(ctx, req.Msg.ChatId)
	defer release()

	outcome, err := s.runs.Resume(ctx, req.Msg.ChatId, nil)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resume workflow"))
	}

	switch outcome.Kind {
	case runs.OutcomeUnresumable:
		return nil, svcerr.FailedPrecondition("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message")

	case runs.OutcomeNeedsRecovery, runs.OutcomeNeedsRestart:
		// The frontend prompts the user to confirm starting a new conversation.
		return connect.NewResponse(&reliantv1.ResumeChatResponse{
			Success:       false,
			Message:       "The workflow session was interrupted and cannot be resumed. Your message history is preserved. Would you like to start a new conversation?",
			WorkflowId:    outcome.WorkflowID,
			NeedsRecovery: true,
			RecoveryType:  reliantv1.RecoveryType_RECOVERY_TYPE_WORKFLOW_LOST,
		}), nil
	}

	return connect.NewResponse(&reliantv1.ResumeChatResponse{
		Success:      true,
		Message:      "Chat resumed successfully",
		WorkflowId:   outcome.WorkflowID,
		RunId:        outcome.RunID,
		RecoveryType: reliantv1.RecoveryType_RECOVERY_TYPE_RESUMED,
	}), nil
}

// REMOVED: UpdateChatAgent - agent is now a workflow param, use UpdateWorkflowParams instead

// ============================================
// Phase 4: Extended Chat Operations
// ============================================

// DismissChat clears needs_attention state when user views a chat
func (s *Service) DismissChat(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissChatRequest],
) (*connect.Response[reliantv1.DismissChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Only dismiss if currently unread
	if !chat.Unread {
		return connect.NewResponse(&reliantv1.DismissChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat does not need dismissal",
		}), nil
	}

	// Clear unread flag
	if err := s.deps.Repo.UpdateChatUnread(ctx, req.Msg.ChatId, false, "user_dismissed"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to dismiss chat"))
	}

	return connect.NewResponse(&reliantv1.DismissChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat dismissed",
	}), nil
}

// MarkUnreadChat marks a chat as needing attention (mark as unread)
func (s *Service) MarkUnreadChat(
	ctx context.Context,
	req *connect.Request[reliantv1.MarkUnreadChatRequest],
) (*connect.Response[reliantv1.MarkUnreadChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Don't allow marking archived chats as unread
	if chat.State == db.ChatStateArchived {
		return nil, svcerr.FailedPrecondition("cannot mark archived chat as unread")
	}

	// If already unread, no change needed
	if chat.Unread {
		return connect.NewResponse(&reliantv1.MarkUnreadChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat already marked as unread",
		}), nil
	}

	// Set unread flag
	if err := s.deps.Repo.UpdateChatUnread(ctx, req.Msg.ChatId, true, "user_marked_unread"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to mark chat as unread"))
	}

	return connect.NewResponse(&reliantv1.MarkUnreadChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat marked as unread",
	}), nil
}

// CompactChat triggers manual context compaction for a chat
func (s *Service) CompactChat(
	ctx context.Context,
	req *connect.Request[reliantv1.CompactChatRequest],
) (*connect.Response[reliantv1.CompactChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.ThreadId == "" {
		return nil, svcerr.InvalidArgument("thread_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Check if there are any messages to compact on the specified thread
	threadToCompact := req.Msg.ThreadId
	messages, err := s.deps.Repo.ListMessages(ctx, req.Msg.ChatId, db.MessageListOptions{
		Thread: &threadToCompact,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check messages"))
	}

	if len(messages) == 0 {
		return nil, svcerr.FailedPrecondition("cannot compact: thread has no messages")
	}

	// Check if a workflow is already running - use Temporal as source of truth
	if workflowID := chat.MainThreadID(); workflowID != "" {
		temporalState, err := s.runs.State(ctx, workflowID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Unavailable("failed to check workflow status"))
		}
		if temporalState.Exists && temporalState.IsRunning {
			return nil, svcerr.FailedPrecondition("cannot compact while a workflow is running")
		}
	}

	// Start the compact workflow with a new UUID
	workflowID := workflow.NewWorkflowID()

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.deps.TaskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}

	// Compact workflow uses the specified thread
	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: "builtin://compact",
		Inputs: map[string]interface{}{
			"thread": threadToCompact,
		},
		ExecContext: &v2.ExecutionContext{
			WorkflowID:   workflowID,
			ChatID:       req.Msg.ChatId,
			WorkflowName: "builtin://compact",
			Thread:       threadToCompact,
			ThreadMode:   model.ThreadModeInherit,
		},
		Trigger: launch.LoadChatTrigger(ctx, s.deps.Repo, req.Msg.ChatId),
	}

	workflowRun, err := s.deps.Temporal.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start compaction"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)

	return connect.NewResponse(&reliantv1.CompactChatResponse{
		ChatId:     req.Msg.ChatId,
		WorkflowId: workflowID,
		RunId:      runID,
		Status:     "compacting",
		Message:    "Context compaction started",
	}), nil
}

// GetChatUpdates fetches chat updates since a sequence number
func (s *Service) GetChatUpdates(
	ctx context.Context,
	req *connect.Request[reliantv1.GetChatUpdatesRequest],
) (*connect.Response[reliantv1.GetChatUpdatesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat with ownership check (defense-in-depth via single query)
	_, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Get updates since sequence
	updates, err := s.deps.Repo.GetUpdatesSince(ctx, req.Msg.ChatId, req.Msg.SinceSeq, 0)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to fetch updates"))
	}

	// Get latest sequence for client
	latestSeq, err := s.deps.Repo.GetLatestUpdateSequence(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get latest sequence"))
	}

	// Convert to proto messages
	protoUpdates := make([]*reliantv1.ChatUpdate, len(updates))
	for i, update := range updates {
		protoUpdates[i] = &reliantv1.ChatUpdate{
			SequenceNumber: update.SequenceNumber,
			UpdateType:     update.UpdateType.String(),
			EntityId:       update.EntityID,
			Data:           string(update.Data),
			CreatedAt:      update.CreatedAt.Format(time.RFC3339),
		}
	}

	return connect.NewResponse(&reliantv1.GetChatUpdatesResponse{
		Updates:        protoUpdates,
		Total:          int32(len(updates)),
		LatestSequence: latestSeq,
	}), nil
}

// SetChatDaemon sets the active daemon for a chat session.
func (s *Service) SetChatDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.SetChatDaemonRequest],
) (*connect.Response[reliantv1.SetChatDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat and verify ownership
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Set or clear the active daemon. A daemon must be one the caller owns and,
	// when the project tracks installs, one it is installed on — the same rule
	// StartChat applies to a chosen daemon.
	var daemonID *string
	if req.Msg.DaemonId != "" {
		if err := daemonchoice.Validate(ctx, s.deps.Repo, userID, chat.ProjectID, req.Msg.DaemonId); err != nil {
			return nil, err
		}
		daemonID = &req.Msg.DaemonId
	}

	// Pinning a daemon is also how a chat with no machine gets one ("Connect a
	// machine", research/NO_MACHINE_CHATS.md): UpdateChatActiveDaemon clears
	// no_machine in the same write, and the next call_llm re-reads the row and
	// offers the full tool set. Clearing never sets it, so a chat on a machine
	// cannot become one without. Other clients learn of it through the same
	// chat_config_changed update UpdateChat emits.
	connecting := chat.NoMachine && daemonID != nil
	if err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := s.deps.Repo.UpdateChatActiveDaemon(txCtx, chat.ID, daemonID); err != nil {
			return err
		}
		configData, err := json.Marshal(map[string]interface{}{
			"chat_id":          chat.ID,
			"active_daemon_id": req.Msg.DaemonId,
			"no_machine":       chat.NoMachine && !connecting,
		})
		if err != nil {
			return err
		}
		return s.deps.Repo.CreateUserUpdate(txCtx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       configData,
		})
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to set chat daemon"))
	}
	if connecting {
		logging.InfoContext(ctx, "[SetChatDaemon] chat with no machine connected to a machine",
			"chatID", chat.ID, "daemonID", req.Msg.DaemonId)
	}

	// Re-fetch chat with updated daemon
	updatedChat, err := s.deps.Repo.GetChat(ctx, chat.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
	}

	return connect.NewResponse(&reliantv1.SetChatDaemonResponse{
		Chat: chatToProto(updatedChat),
	}), nil
}

// AdoptChat takes a run into the caller's own chats. Idempotent: adopting an
// adopted chat keeps its original adopted_at. A chat that is not the caller's
// reads as not found, the same as any other owner-scoped chat RPC.
func (s *Service) AdoptChat(
	ctx context.Context,
	req *connect.Request[reliantv1.AdoptChatRequest],
) (*connect.Response[reliantv1.AdoptChatResponse], error) {
	chat, err := s.setChatAdopted(ctx, req.Msg.GetChatId(), true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.AdoptChatResponse{Chat: chatToProto(chat)}), nil
}

// UnadoptChat clears adoption. Idempotent; the run stays in Runs.
func (s *Service) UnadoptChat(
	ctx context.Context,
	req *connect.Request[reliantv1.UnadoptChatRequest],
) (*connect.Response[reliantv1.UnadoptChatResponse], error) {
	chat, err := s.setChatAdopted(ctx, req.Msg.GetChatId(), false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.UnadoptChatResponse{Chat: chatToProto(chat)}), nil
}

func (s *Service) setChatAdopted(ctx context.Context, chatID string, adopted bool) (*db.Chat, error) {
	userID := auth.MustGetUserID(ctx)
	if chatID == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	owned, err := s.deps.Repo.SetChatAdopted(ctx, chatID, userID, adopted)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat"))
	}
	if !owned {
		return nil, svcerr.NotFound("chat")
	}
	chat, err := s.getChatForUser(ctx, chatID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
	}
	// Adopting is taking the run into the chat list, so an archived one comes
	// back out of the archive: an automation archives every run it finishes
	// successfully, and "Open as chat" on one of those must land somewhere.
	if adopted && chat.State == db.ChatStateArchived {
		if err := s.deps.Repo.UpdateChatState(ctx, chatID, db.ChatStateIdle, "adopted"); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat"))
		}
		if chat, err = s.getChatForUser(ctx, chatID, userID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
		}
	}
	return chat, nil
}

// ListChatPlans lists plans associated with a chat
func (s *Service) ListChatPlans(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatPlansRequest],
) (*connect.Response[reliantv1.ListChatPlansResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// List plans for the chat (across all threads)
	plans, err := s.deps.Repo.ListPlansByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list plans"))
	}

	// Convert to proto messages
	protoPlans := make([]*reliantv1.ChatPlan, len(plans))
	for i, p := range plans {
		protoPlan := &reliantv1.ChatPlan{
			Id:        p.ID,
			ChatId:    req.Msg.ChatId,
			Title:     p.Title,
			Status:    planproto.PlanStatus(p.Status),
			CreatedAt: p.CreatedAt.Format(time.RFC3339),
			UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		}
		if p.Description != nil {
			protoPlan.Description = p.Description
		}
		if p.CompletedAt != nil {
			completedAt := p.CompletedAt.Format(time.RFC3339)
			protoPlan.CompletedAt = &completedAt
		}
		protoPlans[i] = protoPlan
	}

	return connect.NewResponse(&reliantv1.ListChatPlansResponse{
		Plans: protoPlans,
		Total: int32(len(plans)),
	}), nil
}

// listMessagesUnbounded resolves EVERY message this chat has -- the main
// thread's full CW-chain-resolved history plus every child thread's full
// history -- for the legacy "recent not specified" path. This is the
// pre-existing (unbounded) ListMessages behavior, extracted unchanged so
// paginateBySeq keeps operating over a truly complete slice for that one
// caller shape.
func (s *Service) listMessagesUnbounded(
	ctx context.Context, threadsSvc *threads.Service, chatID, mainThreadID string,
) ([]*db.Message, int, error) {
	mainThreadMessages, err := threadsSvc.LoadDisplayMessages(ctx, mainThreadID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve main thread messages: %w", err)
	}
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID
		}
	}

	allThreads, err := s.deps.Repo.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to list threads for conversation", "error", err, "chatID", chatID)
		allThreads = []*db.Thread{}
	}

	var childThreadMessages []*db.Message
	for _, thread := range allThreads {
		if thread.ID == mainThreadID {
			continue
		}
		childMsgs, err := threadsSvc.LoadDisplayMessages(ctx, thread.ID)
		if err != nil {
			logging.WarnContext(ctx, "Failed to load child thread messages", "error", err, "threadID", thread.ID)
			continue
		}
		childThreadMessages = append(childThreadMessages, childMsgs...)
	}

	messageMap := make(map[string]*db.Message, len(mainThreadMessages)+len(childThreadMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, msg := range childThreadMessages {
		if _, exists := messageMap[msg.ID]; !exists {
			messageMap[msg.ID] = msg
		}
	}
	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	return messages, len(messages), nil
}

// listThreadMessages reads exactly one thread's visual thread — the read
// behind ListMessagesRequest.thread_id.
//
// It is the whole implementation of a single-thread read, and it is this
// short on purpose: one thread's history is a thing the threads service
// already knows how to resolve (fork points, compaction chains and visual-
// thread normalization included). The chat-wide paths above are long because
// they merge N threads and then have to decide what to keep; there is nothing
// to merge here, so there is nothing to decide.
//
// recentLimit <= 0 means "the whole thread", which is what a spawn preview
// wants: the child is bounded by its own length, not by a page size borrowed
// from the main transcript.
func (s *Service) listThreadMessages(
	ctx context.Context, threadsSvc *threads.Service, threadID string, beforeSeq int64, recentLimit int,
) ([]*db.Message, bool, error) {
	if recentLimit > 0 {
		return threadsSvc.LoadRecentMessagesBefore(ctx, threadID, beforeSeq, recentLimit)
	}

	messages, err := threadsSvc.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load thread messages: %w", err)
	}
	if beforeSeq > 0 {
		filtered := messages[:0]
		for _, msg := range messages {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}
	// The whole thread was read, so nothing precedes it.
	return messages, false, nil
}

// listMessagesBounded is the SQL-bounded counterpart to
// listMessagesUnbounded, for the normal (recentLimit > 0) path. It reads
// only the window a page actually needs instead of the chat's entire
// history:
//
//   - Main thread: LoadRecentMessagesBefore bounds the read in SQL for the
//     common (unforked) case, falling back to the full resolution only for a
//     forked/compacted main thread.
//   - Sibling threads: bounded to the seq span the main-thread window
//     resolved to (LoadMessagesInSeqRange), rather than each sibling's whole
//     history -- exactly mirroring what windowByMainThread would have kept
//     from an unbounded read, at a fraction of the read cost.
//   - Total: CountDisplayMessages(main) covers the main thread's own
//     CW-chain-resolved count -- the DISPLAY resolution, matching the
//     transcript this page is a window into; the LLM count stops at a
//     compaction summary and would undercount a compacted chat by its whole
//     summarized history. CountMessagesInChat(chat) - CountMessagesInThread(main)
//     covers every sibling thread's messages (siblings don't inherit from
//     elsewhere, so their own chat_id-scoped rows are their whole count).
//   - HasMore: taken directly from the main thread's own hasMoreOlder. This
//     is sufficient because seq is a chat-global, densely-allocated order
//     (GetNextSeqByChat walks the full CW chain when allocating) and every
//     message a branch chat displays -- inherited or local -- carries a seq
//     from that same allocator, so no sibling thread's oldest message can
//     precede the resolved main thread's oldest message.
func (s *Service) listMessagesBounded(
	ctx context.Context, threadsSvc *threads.Service, chatID, mainThreadID string, beforeSeq int64, recentLimit int,
) ([]*db.Message, int, int64, bool, error) {
	mainThreadMessages, mainHasMoreOlder, err := threadsSvc.LoadRecentMessagesBefore(ctx, mainThreadID, beforeSeq, recentLimit)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to load recent main thread messages: %w", err)
	}
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID
		}
	}

	// The seq span this page's main-thread window actually covers. Sibling
	// threads are bounded to this range rather than their whole history --
	// same effect as windowByMainThread's "every sibling message inside that
	// seq range". On the newest page the range is open above, like the chat
	// snapshot's own window (buildChatSnapshot): a sibling thread can
	// out-write and out-live the main thread, finishing after the main
	// thread's newest message in this page.
	// The cursor itself is still the right upper bound on a page OTHER than
	// the newest -- everything at or after beforeSeq belongs to a page
	// already served.
	var fromSeq int64
	var toSeq *int64
	if len(mainThreadMessages) > 0 {
		fromSeq = mainThreadMessages[0].Seq
	} else if beforeSeq > 0 {
		fromSeq = beforeSeq
	}
	if beforeSeq > 0 {
		toSeq = &beforeSeq
	}

	allThreads, err := s.deps.Repo.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to list threads for conversation", "error", err, "chatID", chatID)
		allThreads = []*db.Thread{}
	}

	messageMap := make(map[string]*db.Message, len(mainThreadMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, thread := range allThreads {
		if thread.ID == mainThreadID {
			continue
		}
		childMsgs, err := threadsSvc.LoadMessagesInSeqRange(ctx, thread.ID, fromSeq, toSeq)
		if err != nil {
			logging.WarnContext(ctx, "Failed to load child thread messages in range", "error", err, "threadID", thread.ID)
			continue
		}
		for _, msg := range childMsgs {
			if _, exists := messageMap[msg.ID]; !exists {
				messageMap[msg.ID] = msg
			}
		}
	}

	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	// The window above is already the correctly-sized page (the same shape
	// windowByMainThread would produce), but re-apply it for safety: a
	// sibling thread with messages exactly AT the boundary could otherwise
	// push the reported page past recentLimit main-thread messages.
	messages = windowByMainThread(messages, mainThreadID, recentLimit)

	mainCount, err := threadsSvc.CountDisplayMessages(ctx, mainThreadID)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to count main thread messages: %w", err)
	}
	chatCount, err := s.deps.Repo.CountMessagesInChat(ctx, chatID)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to count chat messages: %w", err)
	}
	mainOwnCount, err := s.deps.Repo.CountMessagesInThread(ctx, mainThreadID)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to count main thread's own messages: %w", err)
	}
	totalCount := mainCount + chatCount - mainOwnCount

	var oldestSeq int64
	if len(messages) > 0 {
		oldestSeq = messages[0].Seq
	}

	return messages, totalCount, oldestSeq, mainHasMoreOlder, nil
}

// messagePage carries the cursor facts a paginated message read has to report
// back to the client.
type messagePage struct {
	// OldestSeq is the lowest seq in the returned page, i.e. the cursor the
	// client passes as before_seq to ask for the page before this one.
	OldestSeq int64
	// HasMore reports whether any message older than this page exists.
	HasMore bool
}

// windowByMainThread returns the tail of messages containing the newest `limit`
// main-thread messages, plus every sibling-thread message inside that range.
//
// Counting the window across every thread makes a page of a spawn-heavy chat
// almost entirely spawn messages, which render collapsed inside their tool
// call rather than in the transcript — so a full page loads and the visible
// conversation barely moves. Below one cursor in a real chat there were 5,675
// messages chat-wide but only 781 on the main thread, so a 200-message page
// advanced the transcript by ~27 rows. Counting the MAIN thread and carrying
// siblings along keeps both the transcript's N rows and the spawn messages
// the tool-call previews render from.
//
// messages must be sorted by seq ascending. mainThreadID == "" falls back to
// a plain newest-N cut. If limit <= 0 or there are already at most `limit`
// messages, messages is returned unchanged.
func windowByMainThread(messages []*db.Message, mainThreadID string, limit int) []*db.Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	if mainThreadID == "" {
		return messages[len(messages)-limit:]
	}
	// Walk back until we have `limit` main-thread messages, then cut there —
	// sibling messages above that point come along for free.
	mainSeen, cutoff := 0, -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ThreadID == mainThreadID {
			mainSeen++
			if mainSeen == limit {
				cutoff = i
				break
			}
		}
	}
	if cutoff > 0 {
		return messages[cutoff:]
	}
	// cutoff < 0 means the remaining history holds fewer than `limit`
	// main-thread messages; return all of it.
	return messages
}

// paginateBySeq narrows a seq-ascending message slice to one page and reports
// the cursor facts for it.
//
// hasMore is derived by comparing the page's oldest seq against the oldest seq
// in the whole chat, which is the only formulation that answers the question the
// client is actually asking ("is there anything before what I'm holding?").
//
// The previous implementation was `len(messages) < totalCount || beforeSeq > 0`
// with totalCount taken AFTER the before_seq filter. Both halves were wrong.
// totalCount measured the already-filtered set, so it shrank in step with the
// slice it was compared against; and `beforeSeq > 0` forced hasMore true for
// every paginated request, so scrollback could never terminate. Together they
// made the client unable to distinguish "more history exists" from "you have
// reached the beginning" — the user-visible symptom being a chat that refuses
// to load older messages no matter how far you scroll.
//
// The page size is counted in MAIN-THREAD messages via windowByMainThread; see
// its doc comment for why.
//
// messages must already be sorted by seq ascending.
func paginateBySeq(messages []*db.Message, beforeSeq int64, recentLimit int, mainThreadID string) ([]*db.Message, messagePage) {
	if len(messages) == 0 {
		return messages, messagePage{}
	}

	// The oldest seq the chat has, captured before either filter runs.
	chatOldestSeq := messages[0].Seq

	if beforeSeq > 0 {
		filtered := make([]*db.Message, 0, len(messages))
		for _, msg := range messages {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	// Messages are oldest-first, so the most recent N are at the end.
	messages = windowByMainThread(messages, mainThreadID, recentLimit)

	if len(messages) == 0 {
		// Paginated past the beginning: nothing to return and nothing older.
		return messages, messagePage{}
	}

	oldest := messages[0].Seq
	return messages, messagePage{
		OldestSeq: oldest,
		HasMore:   oldest > chatOldestSeq,
	}
}

// messagesReadError is a failed message read as the client reads it. A read
// the client abandoned (tab closed, page resumed, navigation) fails with the
// request's own cancellation or deadline even when the driver's error does not
// say so; that is the caller's doing, not a server fault, and reporting it as
// internal made every phone-browser suspend look like an outage. So the
// request's end, when it has ended, is part of the error.
func messagesReadError(ctx context.Context, err error) error {
	return svcerr.WrapOr(errors.Join(ctx.Err(), err), svcerr.Internal("failed to load messages"))
}
