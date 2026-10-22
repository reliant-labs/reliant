// Copyright (c) 2025 Reliant Labs
package question

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/askuser"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// An async question (ask_user async: true) has no wait behind it: the thread
// that asked carried on. Its answer is delivered the way a message the user
// types to that thread is — through the chat service's own send paths, so it
// queues for a working thread (whose next call_llm drains it), rings the
// thread's doorbell as the user's input (a thread parked on a blocking
// ask_user yields to it), resumes a paused or failed run, and starts a run
// when the chat's has ended:
//
//   - the main thread: SendMessage, the composer's send.
//   - a sub-agent: SendAgentMessage to it, which also resumes one that
//     stopped on an error. One that has finished (or whose parent has) is
//     refused; the answer then goes to the main thread instead, saying who
//     asked, so it is never dropped.
//
// The one difference from a typed message: the doorbell is not the user's
// (threadwake.WithoutQuestionYield). The answer is to THIS question, so a
// thread parked on another, blocking, question keeps waiting on it rather
// than closing it unanswered. And a thread parked on a blocking question gets
// the answer in its mailbox and nothing else — no resume, no restart, no
// answer-as-resume of that question — so it reads the answer at the call_llm
// after the user answers the question it is waiting on. Its mailbox survives
// the thread failing meanwhile (store.MarkQueuedAgentMessagesUndeliveredForThread),
// because the pending question is what resumes it.
//
// The question is resolved only after a delivery is accepted. A refusal or an
// error is returned with the question still pending: the user can answer
// again, and the same answer is never queued twice, because the message is
// named by its content (asyncAnswerMessageID). See research/ASYNC_ASK_USER.md.

// asyncAnswerNamespace names the client message ids of async answers.
var asyncAnswerNamespace = uuid.MustParse("6a1f3e0c-8d4b-4c57-9a52-3f1d2b7e9c41")

// asyncAnswerMessageID is the client message id an answer is sent under: one
// per (question, thread, text), so a retry of the same answer to the same
// thread names the message the first attempt queued.
func asyncAnswerMessageID(questionID, threadID, body string) string {
	return uuid.NewSHA1(asyncAnswerNamespace, []byte(questionID+"\x00"+threadID+"\x00"+body)).String()
}

// resolveAsyncQuestion delivers the user's answer to an async question and
// then resolves it.
func (s *Service) resolveAsyncQuestion(
	ctx context.Context,
	chat *db.Chat,
	question *db.Question,
	responseData string,
) (*connect.Response[reliantv1.ResolveQuestionResponse], error) {
	answer := formatAsyncAnswer(question.Metadata, responseData)

	// An answer with nothing in it (a bare "Continue") tells the agent
	// nothing, and a message would wake — or restart — a run for nothing.
	if answer != "" {
		if err := s.deliverAsyncAnswer(ctx, chat, question, answer); err != nil {
			return nil, err
		}
	}

	var stored *string
	if responseData != "" {
		stored = &responseData
	}
	if err := s.deps.Repo.ResolveQuestion(ctx, question.ID, stored); err != nil {
		// Delivered but still pending: an answer sent again finds the
		// message already queued and resolves it then.
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve question"))
	}

	toolCallID := ""
	if question.ToolCallID != nil {
		toolCallID = *question.ToolCallID
	}
	if err := s.deps.Repo.EmitQuestionUpdate(ctx, question.ChatID, db.QuestionUpdate{
		QuestionID: question.ID,
		ChatID:     question.ChatID,
		WorkflowID: question.WorkflowID,
		ThreadID:   question.ThreadID,
		StepID:     question.StepID,
		Status:     "resolved",
		Async:      true,
		ToolCallID: toolCallID,
	}); err != nil {
		logging.WarnContext(ctx, "[Question] Failed to emit question update", "error", err)
	}
	// Answered, it leaves the user's inbox; no chat activity change says so.
	if err := s.deps.Repo.EmitUserRefetch(ctx, chat.UserID, db.RefetchInbox, db.RefetchOpts{ChatID: &chat.ID}); err != nil {
		logging.WarnContext(ctx, "[Question] Failed to announce an answered async question to the inbox", "error", err)
	}

	return connect.NewResponse(&reliantv1.ResolveQuestionResponse{Success: true}), nil
}

// deliverAsyncAnswer sends answer to the thread that asked, or — when that
// thread is a sub-agent that can no longer take a message — to the chat's
// main thread. A nil error means a send path accepted it.
func (s *Service) deliverAsyncAnswer(ctx context.Context, chat *db.Chat, question *db.Question, answer string) error {
	ctx = threadwake.WithoutQuestionYield(ctx)
	mainThread := chat.MainThreadID()
	asker := question.ThreadID
	if asker == "" || asker == chat.ID {
		asker = mainThread
	}

	parked, err := s.parkedOnBlockingQuestion(ctx, chat.ID, asker)
	if err != nil {
		return err
	}
	if parked {
		return s.queueBehindBlockingQuestion(ctx, chat, question.ID, asker, asyncAnswerBody(question.ID, answer))
	}

	if asker != mainThread {
		body := asyncAnswerBody(question.ID, answer)
		id := asyncAnswerMessageID(question.ID, asker, body)
		resp, err := s.deps.Messages.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
			ChatId:          chat.ID,
			ThreadId:        asker,
			Message:         body,
			ClientMessageId: &id,
		}))
		if err != nil {
			return err
		}
		if resp.Msg.GetSuccess() {
			return nil
		}
		logging.InfoContext(ctx, "The agent that asked an async question cannot take its answer; delivering it to the main thread",
			"questionID", question.ID, "chatID", chat.ID, "threadID", asker, "refusal", resp.Msg.GetMessage())
		body = asyncAnswerForwardedBody(question.ID, asker, answer)
		return s.sendToMainThread(ctx, chat, question.ID, body)
	}

	return s.sendToMainThread(ctx, chat, question.ID, asyncAnswerBody(question.ID, answer))
}

// parkedOnBlockingQuestion reports whether thread has a pending blocking
// question: the thread is waiting on the user there, whatever state its run
// is in. PendingQuestionsByThread holds blocking questions only, so the async
// one being answered is never it.
func (s *Service) parkedOnBlockingQuestion(ctx context.Context, chatID, thread string) (bool, error) {
	byThread, err := s.deps.Repo.PendingQuestionsByThread(ctx, chatID)
	if err != nil {
		return false, svcerr.WrapOr(err, svcerr.Internal("failed to check the asking thread's questions"))
	}
	return byThread[thread] != nil, nil
}

// queueBehindBlockingQuestion puts the answer in the thread's mailbox and
// does nothing else: the thread is parked on a blocking question, and its
// next call_llm — after that question is answered — reads it. No doorbell
// rings, no run is resumed or started: any of those would act on the
// question the thread is waiting on. Queued once under its content-named id.
func (s *Service) queueBehindBlockingQuestion(ctx context.Context, chat *db.Chat, questionID, thread, body string) error {
	from := chat.MainThreadID()
	if from == "" {
		from = thread
	}
	if _, err := s.deps.Repo.EnqueueAgentMessageOnce(ctx, &db.AgentMessage{
		ID:           asyncAnswerMessageID(questionID, thread, body),
		FromThreadID: from,
		ChatID:       chat.ID,
		ToThreadID:   thread,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         body,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now(),
	}); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to queue the answer"))
	}
	logging.InfoContext(ctx, "Queued an async answer behind the blocking question its thread is parked on",
		"questionID", questionID, "chatID", chat.ID, "threadID", thread)
	return nil
}

// sendToMainThread sends body to the chat's main thread as the user's
// message, the composer's way.
func (s *Service) sendToMainThread(ctx context.Context, chat *db.Chat, questionID, body string) error {
	id := asyncAnswerMessageID(questionID, chat.MainThreadID(), body)
	_, err := s.deps.Messages.SendMessage(ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chat.ID,
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: body,
		}},
		ClientMessageId: &id,
	}))
	return err
}

// asyncAnswerBody is the message that carries an answer to the thread that
// asked.
func asyncAnswerBody(questionID, answer string) string {
	return fmt.Sprintf("Answer to the question you asked without waiting (ask_user, question_id: %s):\n\n%s", questionID, answer)
}

// asyncAnswerForwardedBody is the message that carries an answer to the main
// thread when the sub-agent that asked has finished.
func asyncAnswerForwardedBody(questionID, askerThreadID, answer string) string {
	return fmt.Sprintf("A sub-agent (agent_id: %s) asked the user this without waiting (ask_user, question_id: %s) and has finished since, so the answer comes to you:\n\n%s\n\n"+
		"If it still matters, act on it yourself, or resume that agent with spawn(agent_id=%q).", askerThreadID, questionID, answer, askerThreadID)
}

// formatAsyncAnswer renders the user's answer to the model as question and
// answer pairs. responseData is the ResolveQuestion payload — the ask_user
// form's {"answers":[{question, selected, freetext}]}, or plain text. A bare
// "Continue" selection is no answer. "" when nothing was answered.
func formatAsyncAnswer(metadata *string, responseData string) string {
	responseData = strings.TrimSpace(responseData)
	if responseData == "" {
		return ""
	}
	var payload struct {
		Answers []askuser.Answer `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &payload); err != nil || payload.Answers == nil {
		// Not the form's shape: the user's own words.
		return "A: " + responseData
	}

	// The asked questions, for an answer that does not repeat its question.
	var asked []askuser.Question
	if metadata != nil {
		if md, ok := askuser.ParseMetadata(*metadata); ok {
			asked = md.Questions
		}
	}

	var parts []string
	for i, a := range payload.Answers {
		var said []string
		for _, sel := range a.Selected {
			if sel != "" && sel != "Continue" {
				said = append(said, sel)
			}
		}
		if f := strings.TrimSpace(a.Freetext); f != "" {
			said = append(said, f)
		}
		if len(said) == 0 {
			continue
		}
		q := a.Question
		if q == "" && i < len(asked) {
			q = asked[i].Question
		}
		if q != "" {
			parts = append(parts, "Q: "+q+"\nA: "+strings.Join(said, " — "))
		} else {
			parts = append(parts, "A: "+strings.Join(said, " — "))
		}
	}
	return strings.Join(parts, "\n\n")
}
