import {
  useQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api, type Message } from "../api/client";
import { queryClient } from "../lib/query-client";
import { logger } from "../lib/logger";
import { chatKeys } from "./chat-queries";

// ── Query key factory ───────────────────────────────────────────────────────

export const messageKeys = {
  all: ["messages"] as const,
  list: (chatId: string) => [...messageKeys.all, "list", chatId] as const,
  thread: (chatId: string, threadId: string) =>
    [...messageKeys.all, "thread", chatId, threadId] as const,
};

// ── Message-list cache (the single source of truth for a chat's messages) ────
//
// messageKeys.list(chatId) stores the ENVELOPE returned by
// api.chatsV2.listMessages — { messages, total, hasMore, oldestSeq } — NOT
// a bare array (mirrors the chat-list cache pattern in chat-queries.ts).
// Readers select `.messages`; the chat stream patches `.messages` live via the
// helpers below (setQueryData, never invalidate/refetch) so the render path
// stays live without a round-trip.
//
// The metadata fields are load-bearing, not decorative: the initial snapshot is
// BOUNDED to the newest N messages, so `hasMore`/`oldestSeq` are what makes
// the rest of a long chat reachable. They are written from real server values
// (setMessagesMetaInCache from the snapshot's pagination info,
// prependMessagesCache from a paged fetch) and merely PRESERVED across the
// message-only patches the stream issues.

export type MessageListResult = {
  messages: Message[];
  total: number;
  hasMore: boolean;
  oldestSeq: number;
};

/**
 * Partial envelope metadata a writer can assert. Every field is optional:
 * omitting one means "keep/derive", which is what lets the high-frequency
 * stream patches (which know nothing about pagination) leave it alone.
 */
export type MessageListMeta = {
  total?: number;
  hasMore?: boolean;
  oldestSeq?: number;
};

const EMPTY_MESSAGES: Message[] = [];

/**
 * Default bound for a cold-start message fetch. Matches the server's snapshot
 * bound so a cold cache and a stream snapshot seed the same slice — without it
 * the queryFn issues an UNBOUNDED listMessages and re-creates the whole-history
 * payload problem the bounded snapshot exists to solve.
 */
export const DEFAULT_RECENT_MESSAGES = 200;

/**
 * Minimum seq across a message list, as a plain number (Message.seq is a
 * bigint). Returns 0 for an empty list — the same "unknown" sentinel the server
 * uses for oldest_seq.
 *
 * seq is a chat-global total order, so this is exactly "the oldest message" in
 * display order, and exactly what the paging RPC wants: the server filters with
 * `seq < before_seq`, so the minimum cached seq is the correct high-water mark
 * for "everything below this is not loaded yet".
 */
export function oldestSeqOf(messages: Message[]): number {
  let oldest = 0;
  for (const message of messages) {
    const seq = Number(message.seq ?? 0);
    if (oldest === 0 || seq < oldest) oldest = seq;
  }
  return oldest;
}

// Message reads must NOT be clobbered by background refetches. The messages
// cache is kept live from the chat stream (snapshot on subscribe / reconnect,
// incremental thereafter), so the queryFn is only an initial seed for a cold
// chat — the stream is the real update channel. These options mirror the old
// Zustand semantics exactly:
//   - staleTime Infinity: never auto-refetch. A window-focus or tab-switch
//     remount must not fire a raw listMessages fetch that would overwrite the
//     normalized/sorted array the stream and loadMessages write.
//   - gcTime Infinity: never garbage-collect. Zustand kept every chat's
//     messages until reset(); an unviewed chat must stay instantly available
//     on tab-switch-back (freed by clearAllMessagesCache on reset/logout).
//   - refetchOnWindowFocus false: belt-and-suspenders with staleTime.
export const messageListQueryOptions = {
  staleTime: Infinity,
  gcTime: Infinity,
  refetchOnWindowFocus: false as const,
} as const;

// Metadata precedence: an explicitly asserted field wins, then the previous
// envelope's value, then a value derived from `messages`.
//
// The derived fallbacks are deliberately conservative about hasMore. A
// stream-seeded envelope has no `prev`, and the snapshot that seeds it is
// BOUNDED (newest N messages) — so defaulting hasMore to false would have the
// cache actively assert "this is the complete history" for a truncated chat,
// which is how older messages become unreachable. Callers that know the truth
// (the snapshot's pagination info, a paged fetch's response) pass `meta`.
function makeMessageEnvelope(
  messages: Message[],
  prev?: MessageListResult,
  meta?: MessageListMeta
): MessageListResult {
  return {
    messages,
    total: meta?.total ?? prev?.total ?? messages.length,
    hasMore: meta?.hasMore ?? prev?.hasMore ?? false,
    oldestSeq:
      meta?.oldestSeq ?? prev?.oldestSeq ?? oldestSeqOf(messages),
  };
}

/**
 * Read a chat's messages from the cache (imperative, non-reactive).
 * Returns a stable empty array when the chat has no cache entry.
 */
export function getMessagesFromCache(chatId: string): Message[] {
  return (
    queryClient.getQueryData<MessageListResult>(messageKeys.list(chatId))
      ?.messages ?? EMPTY_MESSAGES
  );
}

/**
 * Whether a message-list cache entry exists for this chat. Presence is the
 * per-chat "initialized / loaded once" marker (mirrors the old
 * chatStore.messages[chatId] !== undefined init marker).
 */
export function hasMessagesCache(chatId: string): boolean {
  return (
    queryClient.getQueryData(messageKeys.list(chatId)) !== undefined
  );
}

/**
 * Read a chat's pagination metadata from the cache (imperative, non-reactive).
 * Returns undefined when the chat has no cache entry — "we have never loaded
 * this chat", which is distinct from "loaded and there is nothing older".
 */
export function getMessagesMetaFromCache(
  chatId: string
): MessageListMeta | undefined {
  const envelope = queryClient.getQueryData<MessageListResult>(
    messageKeys.list(chatId)
  );
  if (!envelope) return undefined;
  return {
    total: envelope.total,
    hasMore: envelope.hasMore,
    oldestSeq: envelope.oldestSeq,
  };
}

/**
 * Replace a chat's messages in the cache. Envelope metadata is preserved
 * unless `meta` asserts otherwise — pass it whenever the caller has real
 * server-supplied pagination info (e.g. a snapshot), so the cache stops
 * guessing. Creates the entry if absent (used to seed the init marker +
 * snapshot).
 */
export function setMessagesInCache(
  chatId: string,
  messages: Message[],
  meta?: MessageListMeta
): void {
  queryClient.setQueryData<MessageListResult>(
    messageKeys.list(chatId),
    (prev) => makeMessageEnvelope(messages, prev, meta)
  );
}

/**
 * Update only a chat's pagination metadata, leaving `messages` untouched.
 *
 * This is the write path for the snapshot's pagination info, which arrives on
 * its own stream callback (onChatPaginationInfo) separately from the snapshot
 * messages. Creating the entry here would seed a message-less envelope and
 * falsely trip the hasMessagesCache "initialized" marker, so this is a no-op
 * when the chat has no entry yet — the metadata comes back on the next
 * snapshot, and until then there is nothing rendered to page back from.
 */
export function setMessagesMetaInCache(
  chatId: string,
  meta: MessageListMeta
): void {
  queryClient.setQueryData<MessageListResult>(
    messageKeys.list(chatId),
    (prev) => {
      if (!prev) return prev;
      // oldestSeq means "the minimum ordinal we HOLD", so it may only move
      // backward. A reconnect snapshot describes just its bounded newest-N
      // window, but the cache may still hold older pages the user scrolled back
      // to load (mergeMessages preserves them). Taking the snapshot's value
      // verbatim would push the paging cursor forward past that history and
      // make the next page re-fetch messages we already have.
      const clamped: MessageListMeta = { ...meta };
      if (meta.oldestSeq !== undefined) {
        const held = oldestSeqOf(prev.messages);
        clamped.oldestSeq =
          held > 0 ? Math.min(meta.oldestSeq, held) : meta.oldestSeq;
      }
      return makeMessageEnvelope(prev.messages, prev, clamped);
    }
  );
}

/**
 * Prepend a page of OLDER messages to the front of a chat's cached list.
 *
 * Unlike setMessagesInCache this CONCATENATES: the newer messages already in
 * the cache (including live stream writes that landed while the page was in
 * flight) are kept, and only genuinely new ids are added. Ordering is left to
 * sortMessagesForDisplay at the render layer, consistent with mergeMessages.
 *
 * oldestSeq is recomputed from the resulting list rather than trusted from
 * the caller, so the paging high-water mark can only ever move backward.
 */
export function prependMessagesCache(
  chatId: string,
  olderMessages: Message[],
  meta?: MessageListMeta
): void {
  queryClient.setQueryData<MessageListResult>(
    messageKeys.list(chatId),
    (prev) => {
      const existing = prev?.messages ?? EMPTY_MESSAGES;
      const existingIds = new Set(existing.map((m) => m.id));
      const additions = olderMessages.filter((m) => !existingIds.has(m.id));
      const next = [...additions, ...existing];
      return makeMessageEnvelope(next, prev, {
        ...meta,
        oldestSeq: oldestSeqOf(next),
      });
    }
  );
}

/**
 * Functionally update a chat's messages in the cache. The updater receives the
 * current messages (or []) and returns the next array; metadata is preserved.
 */
export function patchMessagesCache(
  chatId: string,
  updater: (messages: Message[]) => Message[],
  meta?: MessageListMeta
): void {
  queryClient.setQueryData<MessageListResult>(
    messageKeys.list(chatId),
    (prev) =>
      makeMessageEnvelope(updater(prev?.messages ?? EMPTY_MESSAGES), prev, meta)
  );
}

/**
 * Fan streamed messages out to any thread-scoped caches that are currently
 * live (see useThreadMessages).
 *
 * The stream is the live update channel, and it writes the chat-wide list. A
 * thread-scoped reader would therefore go stale the moment it mattered most —
 * an ONGOING spawn, whose messages are arriving right now. This routes each
 * streamed message to its own thread's cache too.
 *
 * Two rules keep this from re-creating the bug it exists to fix:
 *
 *   - Only ALREADY-EXISTING thread caches are patched. Seeding a thread cache
 *     from a chat-wide payload would fill it with whatever subset of that
 *     thread the chat-wide window happened to carry, and a partial thread that
 *     looks complete is exactly the failure being removed here. A thread cache
 *     is created only by its own thread-scoped fetch.
 *   - Upsert by id, never replace. The thread-scoped fetch owns the full
 *     history; the stream only ever adds to it.
 */
export function fanOutMessagesToThreadCaches(
  chatId: string,
  messages: Message[]
): void {
  if (messages.length === 0) return;

  const byThread = new Map<string, Message[]>();
  for (const message of messages) {
    const threadId = message.thread;
    if (!threadId) continue;
    const bucket = byThread.get(threadId);
    if (bucket) bucket.push(message);
    else byThread.set(threadId, [message]);
  }

  for (const [threadId, threadMessages] of byThread) {
    const key = messageKeys.thread(chatId, threadId);
    const hasCache = queryClient.getQueryData(key) !== undefined;
    logger.warn(
      `[fanOut] thread=${threadId.slice(0, 8)} count=${threadMessages.length} openCache=${hasCache}`,
    );
    if (!hasCache) continue;
    queryClient.setQueryData<Message[]>(key, (prev) => {
      const existing = prev ?? EMPTY_MESSAGES;
      const byId = new Map(existing.map((m) => [m.id, m]));
      for (const message of threadMessages) byId.set(message.id, message);
      return Array.from(byId.values());
    });
  }
}

/** Drop a chat's message-list cache entry (used on global reset). */
export function clearMessagesCache(chatId: string): void {
  queryClient.removeQueries({ queryKey: messageKeys.list(chatId) });
}

/** Drop ALL message-list cache entries (used on global reset / logout). */
export function clearAllMessagesCache(): void {
  queryClient.removeQueries({ queryKey: messageKeys.all });
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/**
 * Reactive read of a chat's message envelope. The SINGLE useQuery definition
 * for messageKeys.list — chatStoreHooks.useChatMessages wraps this with a
 * select-to-array. The queryFn is only a cold-start seed; the chat stream is
 * the live update channel (see messageListQueryOptions above).
 */
export function useMessages(
  chatId?: string,
  options?: { recent?: number }
) {
  return useQuery({
    queryKey: messageKeys.list(chatId!),
    queryFn: () =>
      api.chatsV2.listMessages(chatId!, {
        // Always bounded. useChatMessages calls this with no options, so an
        // unbounded default would make any cold key (first open of a chat the
        // stream snapshot hasn't seeded) fetch the ENTIRE history — the exact
        // payload the bounded snapshot was introduced to avoid. Older messages
        // are reachable via chatStore.loadOlderMessages.
        recent: options?.recent ?? DEFAULT_RECENT_MESSAGES,
      }),
    enabled: !!chatId,
    ...messageListQueryOptions,
  });
}

/**
 * Reactive read of ONE thread's messages.
 *
 * This is a real fetch of that thread, not a filter over the chat-wide list.
 * The distinction is the point: filtering only worked when the thread's
 * messages happened to be inside a window sized for the MAIN transcript, and
 * when they weren't, a spawn preview showed "Starting…" over a thread with
 * hundreds of messages — a wrong answer that looked like a legitimate empty
 * state. A dedicated query cannot express that: it is pending, or it has the
 * thread.
 *
 * Live updates arrive via fanOutMessagesToThreadCaches from the chat stream,
 * so an ongoing thread stays current without polling.
 */
export function useThreadMessages(chatId?: string, threadId?: string) {
  const query = useQuery({
    queryKey: messageKeys.thread(chatId!, threadId!),
    queryFn: async () => {
      const result = await api.chatsV2.listMessages(chatId!, {
        threadId: threadId!,
      });
      return result.messages;
    },
    enabled: !!chatId && !!threadId,
    ...messageListQueryOptions,
  });

  if (chatId && threadId) {
    const msgs = query.data ?? [];
    const last = msgs[msgs.length - 1];
    logger.warn(
      `[useThreadMessages] thread=${threadId.slice(0, 8)} ` +
        `status=${query.status} fetching=${query.isFetching} ` +
        `count=${msgs.length} ` +
        `lastSeq=${last ? String(last.seq ?? "") : "none"} ` +
        `updatedAt=${query.dataUpdatedAt}`,
    );
  }

  return query;
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useBranchChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      chatId,
      messageId,
      title,
      worktreeId,
    }: {
      chatId: string;
      messageId: string;
      title?: string;
      worktreeId?: string;
    }) => api.chatsV2.branch(chatId, { messageId, title, worktreeId }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    },
  });
}

export function useMarkUnread() {
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.markUnread(chatId),
  });
}