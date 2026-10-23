// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// signalProviderPolicy hands the chat's current provider policy to its live
// root run as an input update, so a change made while the run is working or
// parked on a limit applies to the next limit it meets. Best-effort: the policy
// is stored, and every run built from the chat from now on starts from it.
//
// The run's own model and provider preference decide which Settings default
// applies, so the policy is resolved against the run's current inputs.
func (s *Service) signalProviderPolicy(ctx context.Context, userID string, chat *db.Chat) {
	workflowID := chat.MainThreadID()
	if workflowID == "" || !s.rootRunIsLive(ctx, workflowID) {
		return
	}
	update := s.providerPolicyUpdate(ctx, userID, chat, s.runInputsForSend(ctx, workflowID), nil)
	if len(update) == 0 {
		return
	}
	if err := s.signalParamUpdate(ctx, workflowID, update); err != nil {
		logging.WarnContext(ctx, "Could not signal the chat's provider policy to its live run", "chatID", chat.ID, "error", err)
	}
}

// wakeUsageLimitWait re-decides a run that is parked on a usage limit after the
// chat's policy changed. A parked run only wakes on Retry now, a message, a
// pause or a cancel; the policy update is none of them, so a person who turns
// cross-provider fallback on while waiting would stay parked until the reset.
// It sends the same signal as Retry now, after the policy update, so the step
// is retried against the new policy: if the limit still holds, the step is
// refused again and the run continues on the fallback, or parks on the reset
// as before at the price of one refused request.
//
// Only a policy that allows a fallback can change the answer, so any other
// leaves the wait alone. Best-effort, like signalProviderPolicy.
func (s *Service) wakeUsageLimitWait(ctx context.Context, chat *db.Chat, allowsFallback bool) {
	if !allowsFallback || !isWaitingOnUsageLimit(chat) {
		return
	}
	if err := s.deps.Temporal.SignalWorkflow(ctx, *chat.WorkflowID, "", v2.UsageLimitRetrySignalName, v2.UsageLimitRetrySignal{}); err != nil {
		logging.WarnContext(ctx, "Could not wake a run waiting on a usage limit after a policy change", "chatID", chat.ID, "error", err)
		return
	}
	logging.InfoContext(ctx, "Woke a run waiting on a usage limit to apply a policy change", "chatID", chat.ID, "workflowID", *chat.WorkflowID)
}

// providerPolicyUpdate is the input update that brings a run holding runInputs
// to the chat's current provider policy: the policy under
// v2.InputKeyProviderPolicy, or nothing when the run already has it. update is
// the model/params update going out beside it (it can change which provider is
// primary); nil when there is none. runInputs nil means the run's inputs are
// unknown, so the policy is always sent.
func (s *Service) providerPolicyUpdate(ctx context.Context, userID string, chat *db.Chat, runInputs, update map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(runInputs)+len(update))
	for key, value := range runInputs {
		merged[key] = value
	}
	for key, value := range update {
		merged[key] = value
	}
	policy := s.launcher().ResolveProviderPolicy(ctx, userID, chat, merged)
	if runInputs == nil && policy.IsZero() {
		return nil
	}
	if runInputs != nil && !inputsDiffer(map[string]interface{}{v2.InputKeyProviderPolicy: v2.ProviderPolicy(runInputs).Value()},
		map[string]interface{}{v2.InputKeyProviderPolicy: policy.Value()}) {
		return nil
	}
	return map[string]interface{}{v2.InputKeyProviderPolicy: policy.Value()}
}

// withProviderPolicy adds the chat's current provider policy to a resume's input
// update when the run does not already hold it, so a run woken by a send follows
// a policy the chat changed while it was stopped. It returns update itself when
// there is nothing to add.
func (s *Service) withProviderPolicy(ctx context.Context, userID string, chat *db.Chat, runInputs, update map[string]interface{}) map[string]interface{} {
	extra := s.providerPolicyUpdate(ctx, userID, chat, runInputs, update)
	if len(extra) == 0 {
		return update
	}
	out := make(map[string]interface{}, len(update)+len(extra))
	for key, value := range update {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

// refreshProviderPolicy re-resolves the provider policy of the recorded inputs
// of a stopped run that a new run is built from.
func (s *Service) refreshProviderPolicy(ctx context.Context, chat *db.Chat, inputs map[string]interface{}) {
	s.launcher().RefreshProviderPolicy(ctx, chat.UserID, chat, inputs)
}
