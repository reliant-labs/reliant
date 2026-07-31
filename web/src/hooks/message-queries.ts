import {
  useQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api, type Message } from "../api/client";
import { queryClient } from "../lib/query-client";
import { chatKeys } from "./chat-queries";

// ── Query key factory ───────────────────────────────────────────────────────

export const messageKeys = {
  all: ["messages"] as const,
  list: (chatId: string) => [...messageKeys.all, "list", chatId] as const,
};

// ── Message-list cache (the single source of truth for a chat's messages) ────
//
// messageKeys.list(chatId) stores the ENVELOPE returned by
// api.chatsV2.listMessages — { messages, total, hasMore, oldestOrdinal } — NOT
// a bare array (mirrors the chat-list cache pattern in chat-queries.ts).
// Readers select `.messages`; the chat stream patches `.messages` live via the
// helpers below (setQueryData, never invalidate/refetch) so the render path
// stays live without a round-trip. Metadata fields are preserved across
// message-only patches and defaulted for stream-seeded envelopes (the render
// path only consumes `.messages`; the infinite/paged path is a separate key
// with no live consumers today).

export type MessageListResult = {
  messages: Message[];
  total: number;
  hasMore: boolean;
  oldestOrdinal: number;
};

const EMPTY_MESSAGES: Message[] = [];

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

function makeMessageEnvelope(
  messages: Message[],
  prev?: MessageListResult
): MessageListResult {
  return {
    messages,
    total: prev?.total ?? messages.length,
    hasMore: prev?.hasMore ?? false,
    oldestOrdinal: prev?.oldestOrdinal ?? 0,
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
 * Replace a chat's messages in the cache, preserving envelope metadata.
 * Creates the entry if absent (used to seed the init marker + snapshot).
 */
export function setMessagesInCache(chatId: string, messages: Message[]): void {
  queryClient.setQueryData<MessageListResult>(
    messageKeys.list(chatId),
    (prev) => makeMessageEnvelope(messages, prev)
  );
}

/**
 * Functionally update a chat's messages in the cache. The updater receives the
 * current messages (or []) and returns the next array; metadata is preserved.
 */
export function patchMessagesCache(
  chatId: string,
  updater: (messages: Message[]) => Message[]
): void {
  queryClient.setQueryData<MessageListResult>(
    messageKeys.list(chatId),
    (prev) => makeMessageEnvelope(updater(prev?.messages ?? EMPTY_MESSAGES), prev)
  );
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
      api.chatsV2.listMessages(chatId!, { recent: options?.recent }),
    enabled: !!chatId,
    ...messageListQueryOptions,
  });
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