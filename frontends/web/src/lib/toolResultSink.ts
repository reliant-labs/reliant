/**
 * Where a message read that does not go through the chat store hands its
 * tool results.
 *
 * A tool call's result arrives as its own TOOL message, and the transcript
 * joins it to the call through the chat store's toolResultsByCallId index.
 * Most reads fold their TOOL messages into that index already (loadMessages,
 * scroll-back, the stream's snapshot and batches). The React Query reads in
 * hooks/message-queries.ts — the cold-start seed of a chat and the
 * single-thread read behind a spawn preview or the thread view — do not touch
 * the store, and must not import it (the store imports them). They report
 * their messages here instead; the store registers the receiver when it is
 * created.
 */
import type { Message } from "../types/chat";

type ToolResultSink = (chatId: string, messages: Message[]) => void;

let sink: ToolResultSink | null = null;

/** Called once by the chat store, with the action that merges results in. */
export function registerToolResultSink(fn: ToolResultSink): void {
  sink = fn;
}

/** Hand a read's messages over so their tool results join the index. */
export function recordToolResults(chatId: string, messages: Message[]): void {
  sink?.(chatId, messages);
}
