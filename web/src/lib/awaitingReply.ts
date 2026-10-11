// Copyright (c) 2025 Reliant Labs

/**
 * Whether the conversation's latest turn is the user's: a message that no
 * reply has answered yet.
 *
 * It decides what a run held for its machine says (ChatThinkingIndicator). A
 * run that has not read the user's message yet is holding that message, so the
 * honest line is "Queued — will send when your machine connects"; a run held
 * mid-turn (a tool call waiting for the machine) has already read it, and is
 * only "Waiting for your machine".
 *
 * System notes are not turns. The hidden "your params changed" note that a
 * send can write right after the user's message, and the notices the server
 * posts about a run, must not make an unanswered message look answered.
 * Messages on another thread (a sub-agent's) are not the conversation's turns
 * either, and nor is a send that failed ("Not sent", lib/pendingSends.ts): no
 * run will ever read it.
 */

import { MessageRole } from "@/gen/reliant/v1/chat_pb";
import { FAILED_SEND_PREFIX } from "./pendingSends";

export interface TurnLike {
  id?: string;
  role: MessageRole;
  thread?: string;
}

export function latestTurnIsUsers(
  messages: ReadonlyArray<TurnLike>,
  mainThreadId: string | undefined,
): boolean {
  return unansweredMessage(messages, mainThreadId) !== undefined;
}

/** The user's message that is the conversation's latest turn, if it is. */
export function unansweredMessage<T extends TurnLike>(
  messages: ReadonlyArray<T>,
  mainThreadId: string | undefined,
): T | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]!;
    if (mainThreadId && message.thread && message.thread !== mainThreadId) continue;
    if (message.role === MessageRole.SYSTEM) continue;
    if (message.id?.startsWith(FAILED_SEND_PREFIX)) continue;
    return message.role === MessageRole.USER ? message : undefined;
  }
  return undefined;
}
