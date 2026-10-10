/**
 * Messages the composer has sent whose SendMessage has not answered yet.
 *
 * A send to a chat whose run is executing does not go into the transcript: the
 * server queues it in the thread's mailbox, and the thread's next turn drains
 * it into history. Until SendMessage answers, the client cannot know which of
 * those two the server did, so the message is shown the way most sends end —
 * as an optimistic transcript entry — and it must not ALSO show up in the
 * pending-queue strip if the strip happens to read the mailbox first (the
 * enqueue is announced on the chat stream, and that announcement can beat the
 * RPC's own response).
 *
 * The id is the one the client chose and sent as client_message_id, which the
 * server uses for the queued row; so "is this queued row one of my in-flight
 * sends" is an exact id match, never a guess from the text. The strip hides an
 * in-flight id; when the response says the message was queued, the optimistic
 * entry is dropped and the row handed to the strip in one React commit
 * (chatStore.sendMessage, "agentMailbox:queued").
 */
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

/** A row as the pending-queue strip shows it (QueuedAgentMessageView). */
export interface QueuedRow {
  id: string;
  body: string;
  created_at: string;
  sender_kind: number;
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
