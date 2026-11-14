// Copyright (c) 2025 Reliant Labs

import { ConnectError } from "@connectrpc/connect";

import { RunRecipientAmbiguousSchema } from "../gen/reliant/v1/run_recipient_pb";
import { hasErrorReason } from "./errorReasons";

/** One agent running in a run, as the server's refusal lists it. */
export interface RunRecipientChoice {
  /** The thread the agent reads: the target thread to send to it. */
  threadId: string;
  /** The workflow node it runs at, e.g. "implement.agent_loop"; "" if unknown. */
  node: string;
  /** The thread's title; "" when it has none. */
  title: string;
}

/**
 * A message sent from a run's main view goes to the agent of the run that is
 * running now (internal/handlers/chat/run_recipient.go). The server refuses it
 * when that has no single answer, and never guesses:
 *
 *   - several agents run in parallel (`run_recipient_ambiguous`): the refusal
 *     lists them (runRecipientChoices), and the message's "Not sent" line
 *     offers each as a place to send it (FailedSendStatus). A server from
 *     before that list asks the user to open the agent's thread instead;
 *   - no agent is running right now (`run_no_listening_agent`), e.g. a
 *     workflow between its agent steps: nothing was queued.
 *
 * Returns the line the composer shows for such a refusal, or null for any
 * other error, which the caller shows as it is.
 */
export function runRecipientRefusal(error: unknown): string | null {
  if (hasErrorReason(error, "run_recipient_ambiguous")) {
    if (runRecipientChoices(error).length > 0) {
      return "Several agents are running in this run. Choose the one the message is for, under the message.";
    }
    return "Several agents are running in this run. Open the thread of the agent you mean and send the message from there.";
  }
  if (hasErrorReason(error, "run_no_listening_agent")) {
    return "No agent is running in this run right now, so the message wasn't sent.";
  }
  return null;
}

/**
 * The agents a `run_recipient_ambiguous` refusal lists (its
 * RunRecipientAmbiguous detail), or [] for any other error and for a server
 * that sends no list.
 */
export function runRecipientChoices(error: unknown): RunRecipientChoice[] {
  if (!(error instanceof ConnectError) || !hasErrorReason(error, "run_recipient_ambiguous")) {
    return [];
  }
  const choices: RunRecipientChoice[] = [];
  for (const detail of error.findDetails(RunRecipientAmbiguousSchema)) {
    for (const agent of detail.agents) {
      if (agent.threadId) {
        choices.push({ threadId: agent.threadId, node: agent.node, title: agent.title });
      }
    }
  }
  return choices;
}

/**
 * What a choice is called on screen: the thread's title, else its node's
 * name (the first segment of "implement.agent_loop"), else its thread id.
 */
export function runRecipientLabel(choice: RunRecipientChoice): string {
  if (choice.title.trim()) return choice.title.trim();
  const node = choice.node.split(".")[0];
  if (node) return node;
  return `thread ${choice.threadId.slice(0, 8)}`;
}
