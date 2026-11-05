/**
 * Messages the composer has sent whose SendMessage has not answered yet.
 *
 * A send to a chat whose run is executing does not go into the transcript: the
 * server queues it in the thread's mailbox, and the thread's next turn drains
 * it into history. Until SendMessage answers, the client cannot know for sure
 * which of those two the server did, so chatStore.sendMessage shows the message
 * where its run's activity says it will end up: in the pending-queue strip
 * (beginQueuedSend, below) for a run that is executing, otherwise as an
 * optimistic transcript entry — which must not ALSO show up in the strip if
 * the strip happens to read the mailbox first (the enqueue is announced on the
 * chat stream, and that announcement can beat the RPC's own response).
 *
 * The id is the one the client chose and sent as client_message_id, which the
 * server uses for the queued row; so "is this queued row one of my in-flight
 * sends" is an exact id match, never a guess from the text. The strip hides an
 * in-flight transcript send's id; when the response says the message went the
 * other way, it is moved in one React commit (chatStore.sendMessage,
 * "agentMailbox:queued" / "agentMailbox:drained").
 */
import { createAttemptIds } from "@reliantlabs/sdk/client";
import type { AgentMessageKind } from "../gen/reliant/v1/chat_pb";

const inFlight = new Set<string>();

export function beginPendingSend(clientMessageId: string): void {
  inFlight.add(clientMessageId);
}

export function endPendingSend(clientMessageId: string): void {
  inFlight.delete(clientMessageId);
}

export function isPendingSend(id: string): boolean {
  return inFlight.has(id);
}

// ── One id per logical send ─────────────────────────────────────────────────
//
// The id is how the server tells a repeat of a send from a new message, so an
// id belongs to the logical send and every attempt at it reuses it. The rule
// and its transcript ids are @reliantlabs/sdk's; this module keeps the app's
// one in-memory set of kept ids (cleared on sign-out).
const attempts = createAttemptIds();

/** The id for this logical send: the one an earlier failed attempt used, else a new one. */
export const attemptIdFor = (key: string): string => attempts.idFor(key);

/** The send is settled (it succeeded, or the id is spent): the next one is new. */
export const endAttempt = (key: string): void => attempts.end(key);

/** Forget every kept id. For tests and sign-out. */
export const clearAttempts = (): void => attempts.clear();

/** A row as the pending-queue strip shows it (QueuedAgentMessageView). */
export interface QueuedRow {
  id: string;
  body: string;
  created_at: string;
  sender_kind: AgentMessageKind;
  attachments: string[];
}

interface QueuedSend {
  chatId: string;
  thread: string;
  row: QueuedRow;
}

const queuedInFlight = new Map<string, QueuedSend>();

export function beginQueuedSend(send: QueuedSend): void {
  queuedInFlight.set(send.row.id, send);
}

export function endQueuedSend(rowId: string): void {
  queuedInFlight.delete(rowId);
}

/**
 * A mailbox read for (chat, thread), with every in-flight queued send that the
 * read does not yet include appended in send order. The row IDs are the
 * client_message_id values sent to the server, so reconciliation is exact.
 */
export function withQueuedSendsInFlight<T extends QueuedRow>(
  rows: T[],
  chatId: string,
  thread: string,
): T[] {
  const local = [...queuedInFlight.values()].filter(
    (send) => send.chatId === chatId && send.thread === thread,
  );
  if (local.length === 0) return rows;

  const known = new Set(rows.map((row) => row.id));
  const missing = local
    .map((send) => send.row)
    .filter((row) => !known.has(row.id));
  return missing.length === 0 ? rows : [...rows, ...(missing as T[])];
}

export {
  newClientMessageId,
  OPTIMISTIC_USER_PREFIX,
  FAILED_SEND_PREFIX,
  optimisticSendId,
  failedSendId,
  isLocalOnlyMessageId,
  clientIdOfFailedSend,
} from "@reliantlabs/sdk/client";
