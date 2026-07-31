import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Chat } from "../api/client";
import { queryClient } from "../lib/query-client";
import type { CreateChatRequest, UpdateChatRequest } from "../types/api";

// ── Query key factory ───────────────────────────────────────────────────────

export const chatKeys = {
  all: ["chats"] as const,
  lists: () => [...chatKeys.all, "list"] as const,
  list: (projectId?: string) => [...chatKeys.lists(), projectId] as const,
  details: () => [...chatKeys.all, "detail"] as const,
  detail: (chatId?: string) => [...chatKeys.details(), chatId] as const,
  archived: () => [...chatKeys.all, "archived"] as const,
  search: (projectId: string, query: string) =>
    [...chatKeys.all, "search", projectId, query] as const,
};

// ── Cache patch helpers ─────────────────────────────────────────────────────
// GOTCHA: the list cache stores the RAW envelope from api.chatsV2.list
// ({ chats, lastUserUpdateSequence }) — the `select: (data) => data.chats`
// transform in useChatList runs on READ, not at cache time. Updaters here must
// patch `.chats` inside the envelope, not a bare array.

type ChatListEnvelope = {
  chats: Chat[];
  total?: number;
  lastUserUpdateSequence: number;
};

/**
 * Surgically patch a chat's fields in both the list and detail caches.
 * Never creates cache entries — if a cache is empty or the chat isn't
 * present, that cache is left untouched.
 */
export function patchChatCaches(
  projectId: string | undefined,
  chatId: string,
  patch: Partial<Chat>
): void {
  if (projectId) {
    queryClient.setQueryData(
      chatKeys.list(projectId),
      (prev: ChatListEnvelope | undefined) => {
        if (!prev || !prev.chats.some((c) => c.id === chatId)) return prev;
        return {
          ...prev,
          chats: prev.chats.map((c) =>
            c.id === chatId ? { ...c, ...patch } : c
          ),
        };
      }
    );
  }
  // Also patch the detail cache — ChatHeader reads the title from the detail
  // query, and patching beats invalidation (no refetch round-trip).
  queryClient.setQueryData(
    chatKeys.detail(chatId),
    (prev: Chat | undefined) => (prev ? { ...prev, ...patch } : prev)
  );
}

/**
 * Seed (create or replace) the detail cache for a chat.
 *
 * Unlike patchChatCaches — which is defensive and never creates an entry —
 * this installs the full Chat object as the detail-query data. Object-entering
 * paths (select / create / branch / status refresh) call this so the detail
 * cache is populated synchronously, before any component's useChat query would
 * otherwise fetch it. That keeps reads (e.g. ChatInterface's active-chat
 * existence check) from momentarily seeing `undefined` and flashing the
 * new-chat view.
 */
export function seedChatDetail(chat: Chat): void {
  queryClient.setQueryData(chatKeys.detail(chat.id), chat);
}

/**
 * Read a single Chat from the detail cache (imperative, non-reactive).
 * Returns undefined when the chat has not been loaded/seeded.
 */
export function getChatFromCache(chatId: string): Chat | undefined {
  return queryClient.getQueryData<Chat>(chatKeys.detail(chatId));
}

/**
 * Read the cached chat list for a project (imperative, non-reactive).
 * Returns [] when the list has not been loaded/seeded.
 */
export function getCachedChatList(projectId: string | undefined): Chat[] {
  if (!projectId) return [];
  return (
    queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(projectId))
      ?.chats ?? []
  );
}

/**
 * Remove a chat from the list cache envelope and drop its detail cache.
 */
export function removeChatFromListCache(
  projectId: string | undefined,
  chatId: string
): void {
  if (projectId) {
    queryClient.setQueryData(
      chatKeys.list(projectId),
      (prev: ChatListEnvelope | undefined) => {
        if (!prev) return prev;
        const chats = prev.chats.filter((c) => c.id !== chatId);
        if (chats.length === prev.chats.length) return prev;
        return {
          ...prev,
          chats,
          ...(prev.total !== undefined
            ? { total: Math.max(0, prev.total - 1) }
            : {}),
        };
      }
    );
  }
  queryClient.removeQueries({ queryKey: chatKeys.detail(chatId) });
}

// ── Query hooks ─────────────────────────────────────────────────────────────

export function useChatList(projectId?: string) {
  return useQuery({
    queryKey: chatKeys.list(projectId),
    queryFn: () => api.chatsV2.list(projectId),
    enabled: !!projectId,
    select: (data) => data.chats,
  });
}

export function useChat(chatId?: string) {
  return useQuery({
    queryKey: chatKeys.detail(chatId),
    queryFn: () => api.chatsV2.get(chatId!),
    enabled: !!chatId,
  });
}

export function useArchivedChats() {
  return useQuery({
    queryKey: chatKeys.archived(),
    queryFn: () => api.chatsV2.listArchived(),
  });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useCreateChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateChatRequest) => api.chatsV2.create(request),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    },
  });
}

export function useDeleteChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.delete(chatId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.all });
    },
  });
}

export function useRenameChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ chatId, title }: { chatId: string; title: string }) =>
      api.chatsV2.update(chatId, { title } as UpdateChatRequest),
    onSuccess: (_data, { chatId }) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.detail(chatId) });
    },
  });
}

export function useArchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.archive(chatId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.archived() });
    },
  });
}

export function useUnarchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.unarchive(chatId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.archived() });
    },
  });
}