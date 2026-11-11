// Copyright (c) 2025 Reliant Labs

import { hasErrorReason } from "./errorReasons";

/**
 * A message sent from a run's main view goes to the agent of the run that is
 * running now (internal/handlers/chat/run_recipient.go). The server refuses it
 * when that has no single answer, and never guesses:
 *
 *   - several agents run in parallel (`run_recipient_ambiguous`): open the
 *     thread of the agent the message is for and send it from there;
 *   - no agent is running right now (`run_no_listening_agent`), e.g. a
 *     workflow between its agent steps: nothing was queued.
 *
 * Returns the line the composer shows for such a refusal, or null for any
 * other error, which the caller shows as it is.
 */
export function runRecipientRefusal(error: unknown): string | null {
  if (hasErrorReason(error, "run_recipient_ambiguous")) {
    return "Several agents are running in this run. Open the thread of the agent you mean and send the message from there.";
  }
  if (hasErrorReason(error, "run_no_listening_agent")) {
    return "No agent is running in this run right now, so the message wasn't sent.";
  }
  return null;
}
