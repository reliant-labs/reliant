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

/** A fresh id for a message the client is about to send. */
export function newClientMessageId(): string {
  return crypto.randomUUID();
}

// ── One id per logical send ─────────────────────────────────────────────────
//
// The id is how the server tells a repeat of a send from a new message: a send
// whose response was lost may well have been saved, and sending it again under
// a new id saves it twice. So an id belongs to the logical send — the same
// message to the same place — and every attempt at that send reuses it.
//
// A send that fails keeps its id for the next attempt at the same message, and
// a send that succeeds retires it. The key says what "the same message" is, so
// a message the user edited, or sent somewhere else, is a new one: the server
// refuses one id for two messages. Kept in memory, like the failed sends.

const attemptIds = new Map<string, string>();
const MAX_KEPT_ATTEMPTS = 32;

/** The id for this logical send: the one an earlier failed attempt used, else a new one. */
export function attemptIdFor(key: string): string {
  const kept = attemptIds.get(key);
  if (kept) return kept;
  const id = newClientMessageId();
  attemptIds.set(key, id);
  if (attemptIds.size > MAX_KEPT_ATTEMPTS) {
    const oldest = attemptIds.keys().next().value;
    if (oldest !== undefined) attemptIds.delete(oldest);
  }
  return id;
}

/** The send is settled (it succeeded, or the id is spent): the next one is new. */
export function endAttempt(key: string): void {
  attemptIds.delete(key);
}

/** Forget every kept id. For tests and sign-out. */
export function clearAttempts(): void {
  attemptIds.clear();
}

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

// ── Transcript ids of a send ────────────────────────────────────────────────
//
// A send in flight is an optimistic transcript entry, retired by any arriving
// user message (chatStreamReducers.mergeMessages). A send that failed for good
// is renamed to the failed prefix and marked "Not sent", with Retry and Remove.
// It keeps the "optimistic-" prefix on purpose — it has no server row, so
// everything that skips local-only messages (branching, the first-message
// check, the cold-start seed) skips it too — but NOT "optimistic-user-": no
// arriving message, and no snapshot, describes it, so nothing may retire it
// except the user.

export const OPTIMISTIC_USER_PREFIX = "optimistic-user-";
export const FAILED_SEND_PREFIX = "optimistic-failed-";

export function optimisticSendId(clientMessageId: string): string {
  return OPTIMISTIC_USER_PREFIX + clientMessageId;
}

export function failedSendId(clientMessageId: string): string {
  return FAILED_SEND_PREFIX + clientMessageId;
}

/**
 * A transcript entry that exists only in this client — a send in flight or one
 * that failed for good. It has no server row, so no snapshot or replay is
 * coming to describe it.
 */
export function isLocalOnlyMessageId(messageId: string): boolean {
  return messageId.startsWith("optimistic-");
}

/** The client message id of a failed send's transcript entry, or null. */
export function clientIdOfFailedSend(messageId: string): string | null {
  return messageId.startsWith(FAILED_SEND_PREFIX)
    ? messageId.slice(FAILED_SEND_PREFIX.length)
    : null;
}
