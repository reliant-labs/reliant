/**
 * Canonical client-side ordering for chat messages.
 *
 * This is the ONE ordering used everywhere a message list is sorted
 * (chatStore, ChatContainer, InterleavedTimeline, thread views). Do not add
 * bespoke comparators elsewhere.
 *
 * Server facts this encodes:
 * - `seq` is the server's canonical conversation order, a chat-global total
 *   order across every thread (assigned at persistence time; see
 *   internal/db GetNextSeqByChat and the ORDER BY seq queries in
 *   internal/grpc/services/chat_crud.go and streaming.go). Because it is
 *   total, messages from different threads are directly comparable by seq —
 *   no time-based interleaving or clamping is needed.
 * - Client-built placeholders (optimistic user message: seq 999998,
 *   streaming placeholder: seq 999999) sort after every real message.
 */

export interface OrderableMessage {
  id: string;
  thread?: string;
  seq?: bigint | number;
}

/**
 * A message whose seq never arrived. Sorting it as 0 puts it at the very TOP of
 * the transcript — ahead of the entire conversation — which is the worst
 * possible answer: a just-sent message is the newest thing there is, and a user
 * message landing above its own history breaks the running user-message header
 * the interleaved timeline pins from. Ordering it last is both closer to the
 * truth and self-correcting, since the real seq replaces it on the next read.
 *
 * Below the 999998/999999 placeholder sentinels so a genuine optimistic echo
 * still sorts after a message that merely lost its seq.
 */
const MISSING_SEQ_ORDER = 999_000;

function seqOf(msg: OrderableMessage): number {
  const seq = Number(msg.seq ?? 0);
  // seq 0 is legitimate for the first message in a chat, but that message also
  // sorts first, so treating a missing seq as "newest" costs nothing there and
  // avoids hoisting a live message to the top of a long conversation.
  if (!Number.isFinite(seq) || seq <= 0) {
    return msg.seq === undefined || msg.seq === null ? MISSING_SEQ_ORDER : 0;
  }
  return seq;
}

/**
 * Canonical order for two messages: seq, then id (fully deterministic).
 * Safe across threads too — seq is a chat-global total order. Named
 * "WithinThread" for call sites that use it to sort a single thread's
 * messages, but the comparison itself doesn't care.
 */
export function compareMessagesWithinThread(
  a: OrderableMessage,
  b: OrderableMessage,
): number {
  const bySeq = seqOf(a) - seqOf(b);
  if (bySeq !== 0) return bySeq;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * Returns a new array in canonical display order (oldest first): chat-global
 * seq order. `chatId` is unused now that ordering doesn't depend on thread
 * grouping, but kept in the signature to match every call site.
 */
export function sortMessagesForDisplay<T extends OrderableMessage>(
  messages: readonly T[],
  _chatId: string,
): T[] {
  if (messages.length <= 1) return [...messages];

  const result = [...messages];
  result.sort(compareMessagesWithinThread);
  return result;
}
