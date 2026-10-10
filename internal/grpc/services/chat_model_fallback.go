// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
	for _, sub := range s.launcher().SubstituteUnservableModels(ctx, userID, workflowName, projectID, inputs) {
		logging.Info("Send moved a model input off a provider that cannot serve it",
			"chatID", chatID, "workflowID", workflowID, "input", sub.Input, "from", sub.From, "to", sub.To)
		if isNews != nil && !isNews(strings.SplitN(sub.Input, ".", 2)[0]) {
			continue
		}
		warning := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING)
		if _, err := s.database.SaveMessageToThread(ctx, chatID, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sub.Notice, &workflowID, nil, &warning); err != nil {
			// The switch itself already happened; only the notice is lost.
			logging.Warn("Failed to save the model fallback notice", "error", err, "chatID", chatID)
		}
	}
}
