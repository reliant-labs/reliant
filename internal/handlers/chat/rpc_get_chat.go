package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatproto"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GetChat retrieves a specific chat by ID
func (s *Service) GetChat(
	ctx context.Context,
	req *reliantv1.GetChatRequest,
) (*reliantv1.GetChatResponse, error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat (chatproto.ForViewer).
	ctx = authz.WithViewerReads(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChat(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// The owner, or a viewer of a shared chat.
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil {
		return nil, svcerr.NotFound("chat")
	}
	// A viewer reads the chat, never the owner's composer, workspace or run
	// recovery: no selection read, no Temporal describe.
	if access.Viewer() {
		return &reliantv1.GetChatResponse{Chat: chatproto.ForViewer(chatToProto(chat))}, nil
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
	return &reliantv1.GetChatResponse{
		Chat: protoChat,
	}, nil
}

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
