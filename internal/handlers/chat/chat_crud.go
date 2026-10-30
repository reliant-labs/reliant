// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

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
	"go.temporal.io/api/serviceerror"
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
	// two non-default kinds, builder.test through builder_test and cli.run
	// through cli_run; schedule, agent.start_run and manual.run have no way in
	// through this RPC.
	eventKind := core.TriggerEventKindChatStart
	msg := req.Msg
	switch {
	case msg.GetBuilderTest() && msg.GetCliRun():
		return nil, svcerr.InvalidArgument("builder_test and cli_run are different launch kinds; set at most one")
	case msg.GetBuilderTest():
		if err := s.validateBuilderTest(ctx, userID, msg); err != nil {
			return nil, err
		}
		eventKind = core.TriggerEventKindBuilderTest
	case msg.GetCliRun():
		if msg.GetChatId() != "" {
			return nil, svcerr.InvalidArgument("cli_run starts a new run; chat_id must be empty")
		}
		eventKind = core.TriggerEventKindCLIRun
		if msg.Title == nil {
			// A command-line run's first message is usually an instruction
			// to the workflow, not a subject a generated title could name.
			msg = proto.Clone(msg).(*reliantv1.StartChatRequest)
			title := launch.RunTitle(msg.GetWorkflow(), firstUserContent(msg.GetMessages()), time.Now())
			msg.Title = &title
		}
	}
	return s.startChat(ctx, userID, msg, eventKind, nil)
}

// firstUserContent is the text of the first user message, or "".
func firstUserContent(messages []*reliantv1.InputMessage) string {
	for _, m := range messages {
		if m.GetRole() == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			return m.GetContent()
		}
	}
	return ""
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
		// branch is not a first turn of new work, so it skips the probe. A
		// command-line run is always titled by StartChat (RunTitle), and a
		// generated title would replace it with "Run X Workflow".
		GenerateTitle:   eventKind != core.TriggerEventKindCLIRun,
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
	// A new chat has no row yet; the wake still has to target the machine the
	// launcher will pin it to, so it is asked about the chat it will be. A run
	// with no machine skips the wake outright rather than relying on the row.
	if !spec.NoMachine {
		if wakeChat == nil {
			wakeChat = &db.Chat{UserID: userID, ProjectID: msg.ProjectId, WorktreeID: msg.WorktreeId}
		}
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

	if eventKind == core.TriggerEventKindChatStart {
		s.trackStartedChatMessage(ctx, userID, result.Chat, clientID, msg)
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

// ListChats lists the caller's non-archived chats: in one project, or across
// all of their projects when project_id is empty (the sidebar's "All
// projects" view). Chats are filtered by owner in both cases.
func (s *Service) ListChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatsRequest],
) (*connect.Response[reliantv1.ListChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	limit := int(100)
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	var projectID *string
	if req.Msg.ProjectId != "" {
		projectID = &req.Msg.ProjectId
	}
	filters := db.ChatFilters{
		UserID:          userID,
		ProjectID:       projectID,
		Limit:           limit,
		ExcludeArchived: true,
		// The chat list holds conversations: USER-origin chats and adopted
		// runs. Every other run lists under Runs (RunService.ListRuns).
		SidebarOnly: true,
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
	mainWorktrees := s.attachWorktrees(ctx, userID, req.Msg.ProjectId, chats, protoChats)

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
		MainWorktrees:          mainWorktrees,
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

	// Ghost detection (DB says running, but Temporal lost it) is advisory
	// here: it only sets NeedsRecovery so the client offers "resume". The
	// recovery itself is SendMessage's, which re-asks Temporal before it
	// acts. So the Temporal describe neither gates this read nor can fail
	// it: it runs beside the selection and workspace reads it would
	// otherwise queue behind, and an answer that does not arrive within
	// ghostCheckTimeout is "unknown" -- reported as not needing recovery,
	// like a failed query always was.
	ghost := make(chan bool, 1)
	go func() { ghost <- s.chatNeedsRecovery(ctx, chat) }()

	// Carries the composer's choice for this chat, so any device opens on it.
	protoChat := s.chatToProtoWithSelection(ctx, chat)
	protoChat.NeedsRecovery = <-ghost
	return connect.NewResponse(&reliantv1.GetChatResponse{
		Chat: protoChat,
	}), nil
}

// ghostCheckTimeout bounds GetChat's Temporal describe. A var so a test can
// shrink it.
var ghostCheckTimeout = 750 * time.Millisecond

// chatNeedsRecovery reports whether the chat's root run is a ghost: the
// database says it is running and Temporal has no such execution. GetChat is
// a READ -- it does not reconcile workflow status. Transitions belong to the
// WorkflowStatus("completed") activity callback, and EnsureWorkflowRunning in
// ActivityWrapper handles the reverse case.
//
// Anything short of a definite "Temporal lost it" -- no root run, a run the
// database does not call active, a failed or timed-out query -- is false.
func (s *Service) chatNeedsRecovery(ctx context.Context, chat *db.Chat) bool {
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return false
	}
	wf, err := s.deps.Repo.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil || wf.Status != db.Active() {
		return false
	}

	// DB says running -- verify Temporal agrees.
	describeCtx, cancel := context.WithTimeout(ctx, ghostCheckTimeout)
	defer cancel()
	state, err := s.runs.State(describeCtx, workflowID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to query Temporal for workflow status",
			"error", err, "chatID", chat.ID, "workflowID", workflowID)
		return false
	}
	// Temporal says completed but the database says running: not reconciled
	// here. See above.
	return !state.Exists
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
	if err := s.archiveChat(ctx, chat, "user_archived"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to archive chat"))
	}

	return connect.NewResponse(&reliantv1.DeleteChatResponse{
		Success:            true,
		Message:            "Chat archived successfully",
		PermanentlyDeleted: false,
	}), nil
}

// archiveChat archives a chat and stops its run.
//
// The state change is made under the chat's run-control lock, the lock every
// path that starts a run for the chat holds while it decides to (a send,
// ContinueQueued's delivery of a message queued for the machine,
// ResumePausedForMachine). So a delivery either finishes starting its run
// before the archive — and the cancel below stops it — or reads the chat as
// archived and drops the queued message. Without the lock a delivery that had
// read the chat a moment earlier could start a run after the cancel, on a
// chat the user had put away. The archive itself drops the chat's queued and
// paused-for-machine markers (Repo.UpdateChatState), so nothing redelivers it
// later either.
func (s *Service) archiveChat(ctx context.Context, chat *db.Chat, reason string) error {
	if s.runs != nil {
		release := s.runs.LockRunControl(ctx, chat.ID)
		defer release()
	}
	if err := s.deps.Repo.UpdateChatState(ctx, chat.ID, db.ChatStateArchived, reason); err != nil {
		return err
	}
	s.cancelArchivedChatWorkflow(ctx, chat)
	return nil
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
	// The current run (empty run id): the chat row's run id is stale after a
	// continue-as-new, and cancelling that closed run would leave the live
	// one going.
	err := s.deps.Temporal.CancelWorkflow(ctx, workflowID, "")
	var notFound *serviceerror.NotFound
	if err == nil || errors.As(err, &notFound) {
		return
	}
	logging.ErrorContext(ctx, "Failed to cancel workflow for archived chat",
		"error", err,
		"chatID", chat.ID,
		"workflowID", workflowID)
}

// SearchChats searches the caller's chats by title and message content: in
// one project, or in all of their projects when project_id is empty.
func (s *Service) SearchChats(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchChatsRequest],
) (*connect.Response[reliantv1.SearchChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

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
		data := []byte(update.Data)
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
			// The same tool-result preview every client-facing read sends.
			data = messageproto.PreviewMessageUpdate(data)
		}
		protoUpdates[i] = &reliantv1.ChatUpdate{
			SequenceNumber: update.SequenceNumber,
			UpdateType:     update.UpdateType.String(),
			EntityId:       update.EntityID,
			Data:           string(data),
			CreatedAt:      update.CreatedAt.Format(time.RFC3339),
		}
	}

	return connect.NewResponse(&reliantv1.GetChatUpdatesResponse{
		Updates:        protoUpdates,
		Total:          int32(len(updates)),
		LatestSequence: latestSeq,
	}), nil
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
