import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Chat } from "../api/client";
import type { Chat as ProtoChat, ChatSelection, ListArchivedChatsResponse } from "../gen/reliant/v1/chat_pb";
import type { WorktreeSummary } from "../gen/reliant/v1/worktree_pb";
import { queryClient } from "../lib/query-client";
import { chatServiceKeys, useListArchivedChats } from "./chat-service-hooks_gen";
import type { StartChatRequest, UpdateChatRequest } from "../types/api";

// ── Query key factory ───────────────────────────────────────────────────────

export const chatKeys = {
  all: ["chats"] as const,
  lists: () => [...chatKeys.all, "list"] as const,
  list: (projectId?: string) => [...chatKeys.lists(), projectId] as const,
  details: () => [...chatKeys.all, "detail"] as const,
  detail: (chatId?: string) => [...chatKeys.details(), chatId] as const,
  // The chat's server-side selection: a ChatSelection, or null when it has none.
  selection: (chatId?: string) => [...chatKeys.detail(chatId), "selection"] as const,
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
  // The project's main workspace, so the list can show it with no chat in it.
  mainWorktree?: WorktreeSummary;
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
  // Only a chat the server answered for carries its selection (the project
  // list omits it), so its absence here says nothing about whether there is one.
  if (chat.selection) {
    setChatSelection(chat.id, chat.selection);
  }
}

/** Install the chat's server-side selection (null: it has none) in its query. */
export function setChatSelection(chatId: string, selection: ChatSelection | null): void {
  queryClient.setQueryData(chatKeys.selection(chatId), selection);
}

/** The chat's cached selection: null when it has none, undefined when not loaded. */
export function getChatSelection(chatId: string): ChatSelection | null | undefined {
  return queryClient.getQueryData<ChatSelection | null>(chatKeys.selection(chatId));
}

/** Re-read the chat's selection from the server, e.g. after a refused stale edit. */
export function refetchChatSelection(chatId: string): Promise<unknown> {
  return queryClient.refetchQueries({ queryKey: chatKeys.selection(chatId), exact: true });
}

/**
 * Seed or upsert a full chat into the project list cache.
 *
 * Create-chat flows already have the authoritative Chat returned by the server,
 * so they can make subscribed list readers update synchronously instead of
 * waiting for the global CHAT_CREATED invalidation/refetch path. Creates a
 * minimal envelope when the list has not loaded yet.
 */
export function upsertChatInListCache(
  projectId: string | undefined,
  chat: Chat
): void {
  const resolvedProjectId = projectId ?? chat.projectId;
  if (!resolvedProjectId) return;

  queryClient.setQueryData(
    chatKeys.list(resolvedProjectId),
    (prev: ChatListEnvelope | undefined): ChatListEnvelope => {
      if (!prev) {
        return {
          chats: [chat],
          total: 1,
          lastUserUpdateSequence: 0,
        };
      }

      const existingIndex = prev.chats.findIndex((c) => c.id === chat.id);
      if (existingIndex >= 0) {
        return {
          ...prev,
          chats: prev.chats.map((c, index) =>
            index === existingIndex ? { ...c, ...chat } : c
          ),
        };
      }

      return {
        ...prev,
        chats: [chat, ...prev.chats],
        ...(prev.total !== undefined ? { total: prev.total + 1 } : {}),
      };
    }
  );
}

/**
 * Read a single Chat from the detail cache (imperative, non-reactive).
 * Returns undefined when the chat has not been loaded/seeded.
 */
export function getChatFromCache(chatId: string): Chat | undefined {
  return queryClient.getQueryData<Chat>(chatKeys.detail(chatId));
}

/**
 * Resolve a chat by id: the detail cache, or GetChat on a miss (seeding the
 * cache with the result). Resolves undefined when the chat does not exist.
 *
 * Use this — not getChatFromCache alone — anywhere a chat is reopened from a
 * stored id (workspace restore, project switch, a notification). The detail
 * cache is seeded from the project chat list, and that list deliberately omits
 * automation-launched chats (ListChats sidebar_only), so a cache miss
 * no longer means the chat is gone.
 */
export async function resolveChat(chatId: string): Promise<Chat | undefined> {
  const cached = getChatFromCache(chatId);
  if (cached) return cached;
  try {
    const chat = await api.chatsV2.get(chatId);
    seedChatDetail(chat);
    return chat;
  } catch {
    return undefined;
  }
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
 * The project's main workspace as the chat list last reported it (imperative,
 * non-reactive). Undefined until the list has loaded.
 */
export function getCachedMainWorktree(
  projectId: string | undefined
): WorktreeSummary | undefined {
  if (!projectId) return undefined;
  return queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(projectId))
    ?.mainWorktree;
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

/**
 * The chat list together with the project's main workspace: everything the
 * sidebar and the mobile list need to group chats by workspace, from the one
 * ListChats response. The full worktree list is not involved.
 */
export function useChatListWithWorkspaces(projectId?: string) {
  return useQuery({
    queryKey: chatKeys.list(projectId),
    queryFn: () => api.chatsV2.list(projectId),
    enabled: !!projectId,
  });
}

export function useChat(chatId?: string) {
  return useQuery({
    queryKey: chatKeys.detail(chatId),
    queryFn: () => api.chatsV2.get(chatId!),
    enabled: !!chatId,
  });
}

/**
 * The composer's server-side choice for a chat: model, mode, tools and every
 * other param, kept so any device, restart or branch starts from it. `null` is
 * a definite "this chat has none"; `undefined` data means not loaded yet.
 *
 * The fetch is only the cold-start seed (the project chat list omits the
 * selection, so a chat's first open reads it once). Freshness comes from this
 * device's own writes and from the update stream: every server-side change is
 * announced as a durable CHAT_CONFIG_CHANGED update, which the stream patches
 * live and replays on a cursor resume. What the stream cannot vouch for is
 * marked stale instead: a disruption invalidates what was read before it
 * (globalUpdatesStore.handleStatusChange), and closing the stream — a project
 * switch, which re-scopes it — marks every selection stale
 * (markChatSelectionsUnmaintained). Revalidating on every mount re-read
 * GetChat on each chat switch for an answer the stream already had.
 */
export function useChatSelection(chatId?: string) {
  return useQuery({
    queryKey: chatKeys.selection(chatId),
    queryFn: async () => (await api.chatsV2.get(chatId!)).selection ?? null,
    enabled: !!chatId,
    staleTime: Infinity,
  });
}

/**
 * The stream stopped maintaining every chat's selection (it was closed, e.g. to
 * re-scope it to another project, whose updates it filters out until then).
 * Marks them stale without refetching: each is re-read when a composer next
 * mounts it.
 */
export function markChatSelectionsUnmaintained(): void {
  void queryClient.invalidateQueries({
    queryKey: chatKeys.details(),
    predicate: (query) => query.queryKey[3] === "selection",
    refetchType: "none",
  });
}

/**
 * An archived row as the sidebar lists it: the chat, with its workspace's name
 * and deletion time flattened in.
 */
function toArchivedChats(response: ListArchivedChatsResponse): Chat[] {
  return response.chats.map(({ chat, worktreeName, worktreeDeletedAt }) => {
    const { $typeName: _, ...rest } = chat ?? ({} as Partial<ProtoChat>);
    return { ...rest, worktreeName, worktreeDeletedAt } as Chat;
  });
}

/**
 * The archived list is a pull read on the generated key (entity scope
 * `chatServiceKeys.archivedChat`): the stream only says it changed, and every
 * archive, restore and delete — hand or streamed — invalidates that scope.
 * Unlike the live list it seeds no detail entry, so a refetch of it (a
 * generated ChatService mutation invalidates `chatServiceKeys.all`) never
 * writes over stream-maintained chat state.
 */
export function useArchivedChats() {
  return useListArchivedChats({}, { select: toArchivedChats });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useStartChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: StartChatRequest) => api.chatsV2.start(request),
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
      // Deletes can come from the archived list too.
      queryClient.invalidateQueries({ queryKey: chatServiceKeys.archivedChat });
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
      queryClient.invalidateQueries({ queryKey: chatServiceKeys.archivedChat });
    },
  });
}

export function useUnarchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.unarchive(chatId),
    onSuccess: () => {
      // The chat list carries each chat's workspace, so the restored chat shows
      // with the right group as soon as the list refetches.
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatServiceKeys.archivedChat });
    },
  });
}