/**
 * Canonical client-side ordering for chat messages.
 *
 * This is the ONE ordering used everywhere a message list is sorted
 * (chatStore, ChatContainer, InterleavedTimeline, thread views). Do not add
 * bespoke comparators elsewhere.
 *
 * Server facts this encodes:
 * - `ordinal` is the server's canonical conversation order WITHIN a thread
 *   (assigned per-thread at persistence time; see internal/db GetNextOrdinal
 *   and ListMessages sorting in internal/grpc/services/chat_crud.go).
 * - `createdAt` is NOT reliable within a thread: repair/attachment messages
 *   have shipped with wrong (local-vs-UTC) timestamps, and client-built
 *   placeholders (optimistic user message: ordinal 999998, streaming
 *   placeholder: ordinal 999999) stamp the client clock.
 * - There is no server-side total order ACROSS threads (ordinals are
 *   per-thread), so threads are interleaved by time — but with each thread's
 *   timestamps clamped to be non-decreasing in ordinal order, so a bad
 *   timestamp can never reorder a thread against itself.
 */

export interface OrderableMessage {
  id: string;
  thread?: string;
  ordinal?: bigint | number;
  createdAt?: string;
}

/** Main thread is encoded as "", "0", or the chatId itself. */
function threadKeyOf(chatId: string, thread: string | undefined): string {
  if (!thread || thread === "0" || thread === chatId) return chatId;
  return thread;
}

function ordinalOf(msg: OrderableMessage): number {
  // Ordinals are small integers (per-thread counters); Number() is safe.
  return Number(msg.ordinal ?? 0);
}

function timeOf(msg: OrderableMessage): number {
  const t = new Date(msg.createdAt || "").getTime();
  return Number.isNaN(t) ? 0 : t;
}

/**
 * Canonical order for two messages KNOWN to be on the same thread:
 * ordinal, then createdAt, then id (fully deterministic).
 */
export function compareMessagesWithinThread(
  a: OrderableMessage,
  b: OrderableMessage,
): number {
  const byOrdinal = ordinalOf(a) - ordinalOf(b);
  if (byOrdinal !== 0) return byOrdinal;
  const byTime = timeOf(a) - timeOf(b);
  if (byTime !== 0) return byTime;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * Returns a new array in canonical display order (oldest first):
 * per-thread ordinal order, threads interleaved by (clamped) time.
 */
export function sortMessagesForDisplay<T extends OrderableMessage>(
  messages: readonly T[],
  chatId: string,
): T[] {
  if (messages.length <= 1) return [...messages];

  // Group by thread, preserving nothing about arrival order.
  const groups = new Map<string, T[]>();
  for (const msg of messages) {
    const key = threadKeyOf(chatId, msg.thread);
    const group = groups.get(key);
    if (group) {
      group.push(msg);
    } else {
      groups.set(key, [msg]);
    }
  }

  // Within each thread: canonical ordinal order, then clamp timestamps to be
  // non-decreasing so cross-thread interleaving can never flip a thread
  // against its own ordinal order.
  type Keyed = { msg: T; time: number; threadKey: string; pos: number };
  const keyed: Keyed[] = [];
  for (const [threadKey, group] of groups) {
    group.sort(compareMessagesWithinThread);
    let clamp = 0;
    for (let pos = 0; pos < group.length; pos++) {
      clamp = Math.max(clamp, timeOf(group[pos]));
      keyed.push({ msg: group[pos], time: clamp, threadKey, pos });
    }
  }

  // Total order: clamped time, then thread key, then within-thread position.
  // (time, threadKey, pos) is a strict total order and, because clamped time
  // is non-decreasing in pos, same-thread messages always order by pos.
  keyed.sort((a, b) => {
    if (a.time !== b.time) return a.time - b.time;
    if (a.threadKey !== b.threadKey) {
      return a.threadKey < b.threadKey ? -1 : 1;
    }
    return a.pos - b.pos;
  });

  return keyed.map((k) => k.msg);
}
