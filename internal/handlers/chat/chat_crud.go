// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"go.temporal.io/api/serviceerror"
)

// startChatIDNamespace derives the id of a chat a client named its first
// message for (StartChatRequest.client_message_id).
var startChatIDNamespace = uuid.MustParse("c1d6c3a4-5f0e-4b7a-9a2d-8e3b7f4a1c59")

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
	appName, err := parseAppName(msg.AppName)
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
		AppName:     appName,
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
	// A new chat's org is fixed here and never changes; an existing chat
	// keeps its own.
	if msg.GetChatId() == "" {
		if spec.OrgID, spec.DefaultOrgID, err = s.newChatOrgs(ctx, userID, msg.ProjectId, msg.GetOrgId()); err != nil {
			return nil, err
		}
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
	// A new chat's workspace is a checkout on one machine: a chosen machine
	// must be that one, and an unattributed workspace is settled first so the
	// launcher pins a chat that names none to where its checkout is. A
	// branch's worktree was checked by its pin above.
	if msg.GetChatId() == "" && !spec.NoMachine {
		if err := s.checkWorkspaceMachine(ctx, userID, msg.GetWorktreeId(), chosen); err != nil {
			return nil, err
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
		s.trackStartedChatMessage(ctx, userID, result.Chat, result.UserMessageID, clientID, msg)
	}

	return connect.NewResponse(&reliantv1.StartChatResponse{
		Chat:       s.chatToProtoWithSelection(ctx, result.Chat),
		WorkflowId: result.WorkflowID,
		RunId:      result.RunID,
	}), nil
}

// ghostCheckTimeout bounds GetChat's Temporal describe. A var so a test can
// shrink it.
var ghostCheckTimeout = 750 * time.Millisecond

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

// ChatCompactedThroughHeader is the GetChatUpdates response header carrying
// the chat's compaction watermark (store.CompactChatUpdates). At or below it
// the log is compacted: MESSAGE, TOOL_CALL, STREAM_FINALIZED and
// AGENT_MESSAGES_DRAINED updates are gone, NODE_EXECUTION keeps each node's
// newest update and every other type each entity's newest, while THREAD,
// WORKFLOW_STATUS, ERROR and QUESTION are whole. A poll from at or above it
// gets every update. A poll from below it fails with OutOfRange, reason
// ChatHistoryCompactedReason, and this header on the error: a successful
// replay there would be a partial one, and a poller that does not know about
// compaction (every client released before it) would advance its cursor past
// transitions it never saw. A poller that does know re-reads the chat's
// current state (GetChat, ListMessages, its open gates) and resumes from the
// watermark. A header, not a response field: GetChatUpdatesResponse is public
// SDK API, which changes in its own PR (sdk/go/README.md), and a header is
// invisible to every client that does not look for it. 0: never compacted.
const ChatCompactedThroughHeader = "Reliant-Chat-Compacted-Through"

// ChatHistoryCompactedReason is the error reason (svcerr.ReasonHeader) of a
// GetChatUpdates poll from below the compaction watermark.
const ChatHistoryCompactedReason = "chat_history_compacted"
