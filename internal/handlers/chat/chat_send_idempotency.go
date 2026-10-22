// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A user's send names its message. The client mints client_message_id once per
// logical send and reuses it when it sends again, so a send whose response was
// lost (a dropped connection, a client timeout that cancelled the handler after
// it had committed) can be repeated without saying the same thing twice. The id
// IS the message's row id, in the transcript or in the mailbox, so the primary
// key is what arbitrates two repeats racing each other; SendMessage's per-chat
// run-control lock already serializes them before they get that far.
//
// What a repeat means is decided by what the first attempt left behind:
//
//   - the message was read (a turn follows it, or its mailbox row was drained):
//     answer with the receipt the first attempt got. Never a second run.
//   - the message is saved or queued but may not have reached a run (the first
//     attempt was cut off after its write): finish the send around the message
//     that is already there, without writing it again.
//   - the message was queued and never delivered (its thread ended first): it
//     was never read, so the repeat is a fresh send that takes the id back.
//   - the id names a different message, or one in another chat: refused, with
//     the same answer for both so it discloses nothing about the other message.

// parseClientMessageID returns the canonical form of a send's client message
// id, or "" when the client sent none. Anything else that is not a UUID is
// refused: the id becomes a row id, and an id the server quietly replaced would
// defeat the one thing it is for.
func parseClientMessageID(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", svcerr.InvalidArgument("client_message_id must be a UUID")
	}
	return id.String(), nil
}

// canonicalClientMessageID is parseClientMessageID for a value the handler
// already vetted at its door.
func canonicalClientMessageID(raw string) string {
	id, _ := parseClientMessageID(raw)
	return id
}

// priorSend is what an earlier send under the same client message id left.
type priorSend struct {
	// history is the user message the earlier send wrote to the transcript.
	history *db.Message
	// mailbox is the row the earlier send queued for a running thread.
	mailbox *db.AgentMessage
	// settled: the message was read, so nothing is owed.
	settled bool
	// undelivered: the row was queued and its thread ended before reading it.
	undelivered bool
}

// messageID is the id the earlier send's receipt carried.
func (p *priorSend) messageID() string {
	if p.history != nil {
		return p.history.ID
	}
	return p.mailbox.ID
}

// queued is what the earlier send's receipt said: a message that went through
// the mailbox was reported queued, and stays so even after a drain has read it
// (the client already drops a queued receipt for a row it saw drained).
func (p *priorSend) queued() bool {
	return p.mailbox != nil
}

// findPriorSend looks up what an earlier send under id left in chat. It reads:
// it returns (nil, nil) for an id nothing has used, and launch.ErrClientMessageReused
// for one that names a different message.
//
// userContent is the text this send would save and attachments its attachment
// ids; together they are the message's identity. Params, presets and the like
// steer the run, they are not the message.
func (s *Service) findPriorSend(ctx context.Context, chat *db.Chat, id, userContent string, attachments []string) (*priorSend, error) {
	if id == "" {
		return nil, nil
	}

	msg, err := s.deps.Repo.FindMessage(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("check for an earlier send: %w", err)
	}
	if msg != nil {
		if msg.ChatID != chat.ID || msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_USER {
			return nil, launch.ErrClientMessageReused
		}
		same, err := s.historyMessageIs(ctx, msg.ID, userContent, attachments)
		if err != nil {
			return nil, err
		}
		if !same {
			return nil, launch.ErrClientMessageReused
		}
		latest, err := s.deps.Repo.IsLatestTurnInThread(ctx, msg.ThreadID, msg.ID)
		if err != nil {
			return nil, fmt.Errorf("check whether the earlier send was answered: %w", err)
		}
		return &priorSend{history: msg, settled: !latest}, nil
	}

	row, err := s.deps.Repo.GetAgentMessage(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("check for an earlier queued send: %w", err)
	}
	if row == nil {
		return nil, nil
	}
	if !humanMessageIs(row, chat.ID, userContent, attachments) {
		return nil, launch.ErrClientMessageReused
	}
	return &priorSend{
		mailbox:     row,
		settled:     row.Status == core.AgentMessageStatusDelivered,
		undelivered: row.Status == core.AgentMessageStatusUndelivered,
	}, nil
}

func humanMessageIs(row *db.AgentMessage, chatID, body string, attachments []string) bool {
	return row.ChatID == chatID && row.Kind == core.AgentMessageKindHumanMessage &&
		row.Body == body && sameStrings(row.Attachments, attachments)
}

// historyMessageIs reports whether the transcript message id holds exactly this
// text and these attachments. A reply to a run that died parked on a question
// is stored with the resume marker in front (markResumeAnswer), so that form of
// the text is the same message too.
func (s *Service) historyMessageIs(ctx context.Context, id, userContent string, attachments []string) (bool, error) {
	blocks, err := s.deps.Repo.ListContentBlocks(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read the earlier send: %w", err)
	}
	var text string
	var stored []string
	for _, block := range blocks {
		if block.Content == nil {
			continue
		}
		switch block.BlockType {
		case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
			text = *block.Content
		case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE,
			reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE,
			reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT:
			stored = append(stored, *block.Content)
		}
	}
	if text != userContent && text != markResumeAnswer(userContent) {
		return false, nil
	}
	return sameStrings(stored, attachments), nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// replayResponse is the receipt for a send that was already accepted: the
// message id the first receipt carried, and the chat's run as it stands.
func replayResponse(chat *db.Chat, prior *priorSend) *connect.Response[reliantv1.SendMessageResponse] {
	resp := &reliantv1.SendMessageResponse{
		ChatId:     chat.ID,
		WorkflowId: chat.MainThreadID(),
		Status:     "processing",
		MessageId:  prior.messageID(),
		Queued:     prior.queued(),
	}
	if chat.RunID != nil {
		resp.RunId = *chat.RunID
	}
	return connect.NewResponse(resp)
}

// priorSendOrReplay is SendMessage's first look at a send that carries an id,
// taken inside the run-control lock so a twin that is still finishing has
// committed by the time this reads. It returns the earlier send (nil when this
// is the first), or a response when the send is already complete.
func (s *Service) priorSendOrReplay(ctx context.Context, chat *db.Chat, id, userContent string, attachments []string) (*priorSend, *connect.Response[reliantv1.SendMessageResponse], error) {
	prior, err := s.findPriorSend(ctx, chat, id, userContent, attachments)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to check for an earlier send"))
	}
	if prior == nil {
		return nil, nil, nil
	}
	if prior.undelivered {
		// Never read, so it is not a message the agent has: the send starts over
		// under the same id.
		if _, err := s.deps.Repo.DeleteUndeliveredAgentMessage(ctx, prior.mailbox.ID, chat.ID); err != nil {
			return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to retry an undelivered message"))
		}
		return nil, nil, nil
	}
	if prior.settled {
		return prior, replayResponse(chat, prior), nil
	}
	return prior, nil, nil
}

// enqueueHumanMessage queues a human message for a thread. When the message
// is named by the client it is queued once: an id that is already taken by this
// same message returns that row with inserted=false instead of an error, and
// one that is taken by a different message is refused.
func (s *Service) enqueueHumanMessage(ctx context.Context, msg *db.AgentMessage, named bool) (row *db.AgentMessage, inserted bool, err error) {
	if !named {
		if err := s.deps.Repo.EnqueueAgentMessage(ctx, msg); err != nil {
			return nil, false, err
		}
		return msg, true, nil
	}

	// Twice: the first look can find an undelivered row to clear, and the second
	// insert is then the only thing left that can lose to a racing twin.
	for attempt := 0; attempt < 3; attempt++ {
		ok, err := s.deps.Repo.EnqueueAgentMessageOnce(ctx, msg)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return msg, true, nil
		}
		existing, err := s.deps.Repo.GetAgentMessage(ctx, msg.ID)
		if err != nil {
			return nil, false, err
		}
		if existing == nil {
			// Claimed or withdrawn between the insert and the read.
			continue
		}
		if !humanMessageIs(existing, msg.ChatID, msg.Body, msg.Attachments) {
			return nil, false, launch.ErrClientMessageReused
		}
		if existing.Status == core.AgentMessageStatusUndelivered {
			if _, err := s.deps.Repo.DeleteUndeliveredAgentMessage(ctx, existing.ID, msg.ChatID); err != nil {
				return nil, false, err
			}
			continue
		}
		return existing, false, nil
	}
	logging.WarnContext(ctx, "Could not settle which row holds a client message id", "messageID", msg.ID, "chatID", msg.ChatID)
	return nil, false, fmt.Errorf("could not queue message %s", msg.ID)
}

// errSendAlreadySaved aborts a save transaction whose message turned out to be
// written already by a twin that committed first, which rolls back whatever the
// transaction had written ahead of it (the send's system notes).
var errSendAlreadySaved = errors.New("send already saved")

// savedSendID is the id of the message an earlier attempt of this send saved
// in chat, "" when there is none: the transcript row, or the mailbox row for a
// message queued to a running thread (the thread's next turn drains that into
// history, so it must not be saved a second time).
func (s *Service) savedSendID(ctx context.Context, chatID, id string) (string, error) {
	msg, err := s.deps.Repo.FindMessage(ctx, id)
	if err != nil {
		return "", fmt.Errorf("check for an earlier send: %w", err)
	}
	if msg != nil {
		if msg.ChatID != chatID {
			return "", launch.ErrClientMessageReused
		}
		return msg.ID, nil
	}
	row, err := s.deps.Repo.GetAgentMessage(ctx, id)
	if err != nil {
		return "", fmt.Errorf("check for an earlier queued send: %w", err)
	}
	if row == nil {
		return "", nil
	}
	if row.ChatID != chatID {
		return "", launch.ErrClientMessageReused
	}
	return row.ID, nil
}

// runStartedForPriorSend reports whether an earlier attempt of this send saved
// its message to the transcript and the chat's run is open now. The workflow
// row lags a run that has just started — it keeps its old status until the
// run's own first activity writes "started" — so the row alone cannot tell a
// repeat from a first send, and Temporal is asked instead. A repeat that finds
// the run open has nothing left to do: starting another would terminate it.
func (s *Service) runStartedForPriorSend(ctx context.Context, prior *priorSend, workflowID string) bool {
	if prior == nil || prior.history == nil {
		return false
	}
	state, err := s.runs.State(ctx, workflowID)
	return err == nil && state.Exists && state.IsRunning
}
