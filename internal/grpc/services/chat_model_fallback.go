// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
)

// fallBackFromUnservableModels applies the send-time model policy to the
// inputs a send is handing a run: every model input whose model none of the
// user's connected providers can serve right now moves to what they offer in
// its place (launch.SubstituteUnservableModels), and the chat says so in a
// warning notice on thread — what changed, why, and how to go back.
//
// A send is never refused for this. Where nothing connected can stand in, the
// input is left as it is and the run fails it with the explained error.
//
// isNews, when set, reports whether a substitution changes what the run is
// already using: the composer keeps sending the model the user last picked,
// so once a run has been moved, later sends would otherwise repeat the notice.
// It receives the input's top-level key.
func (s *ChatService) fallBackFromUnservableModels(ctx context.Context, userID, chatID, projectID, workflowName, workflowID, thread string, inputs map[string]interface{}, isNews func(topLevelInput string) bool) {
	var news []launch.ModelSubstitution
	for _, sub := range s.moveUnservableModels(ctx, userID, chatID, projectID, workflowName, workflowID, inputs) {
		if isNews == nil || isNews(topLevelInput(sub.Input)) {
			news = append(news, sub)
		}
	}
	s.postModelFallbackNotices(ctx, chatID, workflowID, thread, news)
}

// moveUnservableModels is the policy half of fallBackFromUnservableModels: it
// moves every unservable model input in inputs, in place, and returns what it
// moved. It tells no one; the caller posts the notices once the run that will
// use the inputs is known to be getting them.
func (s *ChatService) moveUnservableModels(ctx context.Context, userID, chatID, projectID, workflowName, workflowID string, inputs map[string]interface{}) []launch.ModelSubstitution {
	subs := s.launcher().SubstituteUnservableModels(ctx, userID, workflowName, projectID, inputs)
	for _, sub := range subs {
		logging.Info("Send moved a model input off a provider that cannot serve it",
			"chatID", chatID, "workflowID", workflowID, "input", sub.Input, "from", sub.From, "to", sub.To)
	}
	return subs
}

// postModelFallbackNotices is the telling half: one warning notice on thread
// per move.
func (s *ChatService) postModelFallbackNotices(ctx context.Context, chatID, workflowID, thread string, subs []launch.ModelSubstitution) {
	warning := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING)
	for _, sub := range subs {
		if _, err := s.database.SaveMessageToThread(ctx, chatID, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sub.Notice, &workflowID, nil, &warning); err != nil {
			// The switch itself already happened; only the notice is lost.
			logging.Warn("Failed to save the model fallback notice", "error", err, "chatID", chatID)
		}
	}
}

// resumeInputUpdate is the input update a send hands a run it is resuming in
// place (a paused run) or resetting and replaying (a failed one), with the
// notices to post once that run has it.
//
// It is the send's validated params, each moved off a provider that cannot
// serve it — plus every model input the run already holds and the send does
// not carry, moved the same way. A send's params name only what the composer
// sent: from another device, or after a reload, that can be nothing at all,
// and the run would resume on the pin it parked on — gpt-5.6-sol@codex for a
// user with no Codex, failing exactly as before. Where the send carries the
// model, the run's own value is left to it.
//
// runInputs are the run's current inputs (get_workflow_inputs), nil when
// Temporal could not say: then only the send's params are moved, and every
// move is announced. With them, a move of a sent param is announced only when
// it changes what the run is using (see fallBackFromUnservableModels' isNews).
func (s *ChatService) resumeInputUpdate(ctx context.Context, userID, chatID, projectID, workflowName, workflowID string, sent, runInputs map[string]interface{}) (map[string]interface{}, []launch.ModelSubstitution) {
	update := make(map[string]interface{}, len(sent))
	for key, value := range sent {
		update[key] = value
	}
	var notices []launch.ModelSubstitution
	for _, sub := range s.moveUnservableModels(ctx, userID, chatID, projectID, workflowName, workflowID, update) {
		key := topLevelInput(sub.Input)
		if runInputs == nil || inputsDiffer(runInputs, map[string]interface{}{key: update[key]}) {
			notices = append(notices, sub)
		}
	}

	if runInputs != nil {
		held := make(map[string]interface{}, len(runInputs))
		for key, value := range runInputs {
			if _, sentIt := update[key]; !sentIt {
				held[key] = value
			}
		}
		for _, sub := range s.moveUnservableModels(ctx, userID, chatID, projectID, workflowName, workflowID, held) {
			key := topLevelInput(sub.Input)
			update[key] = held[key]
			notices = append(notices, sub)
		}
	}

	if len(update) == 0 {
		return nil, notices
	}
	return update, notices
}

// runInputsTimeout bounds the get_workflow_inputs query a send makes on the
// run it is about to resume. A parked run answers from its worker's cache; a
// closed one is replayed first. Past this the send goes on without them.
const runInputsTimeout = 10 * time.Second

// runInputsForSend is the run's current inputs, for resumeInputUpdate, or nil
// when Temporal cannot say in time — then only what the send carries is
// checked against the user's providers.
func (s *ChatService) runInputsForSend(ctx context.Context, workflowID, runID string) map[string]interface{} {
	queryCtx, cancel := context.WithTimeout(ctx, runInputsTimeout)
	defer cancel()
	inputs, err := s.queryRunInputs(queryCtx, workflowID, runID)
	if err != nil {
		logging.Info("Could not read the run's inputs; checking only the send's own params against the user's providers",
			"workflowID", workflowID, "error", err)
		return nil
	}
	return inputs
}

// topLevelInput is the input key a substitution's path starts with: "model"
// for "model", "<group>" for "<group>.model".
func topLevelInput(path string) string {
	return strings.SplitN(path, ".", 2)[0]
}
