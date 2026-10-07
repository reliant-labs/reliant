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
