import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { Chat } from "../api/client";
import type { Chat as ProtoChat, ChatSelection, ListArchivedChatsResponse } from "../gen/reliant/v1/chat_pb";
import type { WorktreeSummary } from "../gen/reliant/v1/worktree_pb";
import { queryClient } from "../lib/query-client";
import { chatServiceKeys, useListArchivedChats } from "./chat-service-hooks_gen";
import type { StartChatRequest, UpdateChatRequest } from "../types/api";
import * as chatActions from "./chat-actions";

// ── Query key factory ───────────────────────────────────────────────────────

/**
 * The list scope of the all-projects chat list (ListChats with no project_id).
 * A real key segment rather than `undefined`, so a project-scoped key never
 * prefix-matches it and an invalidation of one list never reaches the other.
 */
export const ALL_PROJECTS_SCOPE = "*all-projects*";

export const chatKeys = {
  all: ["chats"] as const,
  lists: () => [...chatKeys.all, "list"] as const,
  /** One project's list, or every project's when `projectId` is null/undefined. */
  list: (projectId?: string | null) => [...chatKeys.lists(), projectId ?? ALL_PROJECTS_SCOPE] as const,
  details: () => [...chatKeys.all, "detail"] as const,
  detail: (chatId?: string) => [...chatKeys.details(), chatId] as const,
  // The chat's server-side selection: a ChatSelection, or null when it has none.
  selection: (chatId?: string) => [...chatKeys.detail(chatId), "selection"] as const,
};

// ── Cache patch helpers ─────────────────────────────────────────────────────
// GOTCHA: the list cache stores the RAW envelope from chatActions.listChats
// ({ chats, lastUserUpdateSequence }) — the `select: (data) => data.chats`
// transform in useChatList runs on READ, not at cache time. Updaters here must
// patch `.chats` inside the envelope, not a bare array.

export type ChatListEnvelope = {
  chats: Chat[];
  total?: number;
  lastUserUpdateSequence: number;
  // The main workspace of each project in the list's scope, so the list can
  // show a project's main workspace with no chat in it.
  mainWorktrees: WorktreeSummary[];
};

/** The scope a list cache key was written for: a project id, or null for all. */
function scopeOfListKey(queryKey: readonly unknown[]): string | null {
  const scope = queryKey[2];
  return typeof scope === "string" && scope !== ALL_PROJECTS_SCOPE ? scope : null;
}

/** Whether a list of `scope` (null: every project) holds chats of `projectId`. */
function scopeHolds(scope: string | null, projectId: string | undefined): boolean {
  return scope === null || (!!projectId && scope === projectId);
}

/**
 * Surgically patch a chat's fields in every list cache holding it and in its
 * detail cache. Never creates cache entries — if a cache is empty or the chat
 * isn't present, that cache is left untouched. `projectId` is accepted for the
 * callers that know it; a chat is patched wherever it is listed either way.
 */
export function patchChatCaches(
  _projectId: string | undefined,
  chatId: string,
  patch: Partial<Chat>
): void {
  queryClient.setQueriesData<ChatListEnvelope>(
    { queryKey: chatKeys.lists() },
    (prev) => {
      if (!prev || !prev.chats.some((c) => c.id === chatId)) return prev;
      return {
        ...prev,
        chats: prev.chats.map((c) =>
          c.id === chatId ? { ...c, ...patch } : c
        ),
      };
    }
  );
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

function upsertInto(prev: ChatListEnvelope, chat: Chat): ChatListEnvelope {
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

/**
 * Seed or upsert a full chat into the list caches that hold its project.
 *
 * Create-chat flows already have the authoritative Chat returned by the server,
 * so they can make subscribed list readers update synchronously instead of
 * waiting for the global CHAT_CREATED invalidation/refetch path. The chat's
 * project list gets a minimal envelope when it has not loaded yet; the
 * all-projects list is only upserted into once it has (a one-chat envelope
 * would stand in for every other project's chats until the refetch).
 */
export function upsertChatInListCache(
  projectId: string | undefined,
  chat: Chat
): void {
  const resolvedProjectId = projectId ?? chat.projectId;
  if (!resolvedProjectId) return;

  queryClient.setQueryData(
    chatKeys.list(null),
    (prev: ChatListEnvelope | undefined) => (prev ? upsertInto(prev, chat) : prev),
  );
  queryClient.setQueryData(
    chatKeys.list(resolvedProjectId),
    (prev: ChatListEnvelope | undefined): ChatListEnvelope => {
      if (!prev) {
        return {
          chats: [chat],
          total: 1,
          lastUserUpdateSequence: 0,
          mainWorktrees: [],
        };
      }
      return upsertInto(prev, chat);
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
    const chat = await chatActions.getChat(chatId);
    seedChatDetail(chat);
    return chat;
  } catch {
    return undefined;
  }
}

/**
 * Read the cached chat list of a scope (imperative, non-reactive): one
 * project's chats — from its own list, or the all-projects list when only that
 * one has loaded — or every project's when `projectId` is null. Returns []
 * when no list holding them has been loaded/seeded.
 */
export function getCachedChatList(projectId: string | null | undefined): Chat[] {
  if (projectId === undefined) return [];
  const own = queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(projectId));
  if (own) return own.chats;
  if (projectId === null) return [];
  return (
    queryClient
      .getQueryData<ChatListEnvelope>(chatKeys.list(null))
      ?.chats.filter((c) => c.projectId === projectId) ?? []
  );
}

/**
 * The project's main workspace as a chat list holding it last reported it
 * (imperative, non-reactive). Undefined until such a list has loaded.
 */
export function getCachedMainWorktree(
  projectId: string | undefined
): WorktreeSummary | undefined {
  if (!projectId) return undefined;
  for (const [key, envelope] of queryClient.getQueriesData<ChatListEnvelope>({ queryKey: chatKeys.lists() })) {
    if (!envelope || !scopeHolds(scopeOfListKey(key), projectId)) continue;
    const main = envelope.mainWorktrees?.find((w) => w.projectId === projectId);
    if (main) return main;
  }
  return undefined;
}

/**
 * The main workspaces a scope's chat list carries (imperative, non-reactive):
 * [] until it has loaded.
 */
export function getCachedListMainWorktrees(scope: string | null): WorktreeSummary[] {
  return queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(scope))?.mainWorktrees ?? [];
}

/**
 * Remove a chat from every list cache envelope and drop its detail cache.
 */
export function removeChatFromListCache(
  _projectId: string | undefined,
  chatId: string
): void {
  queryClient.setQueriesData<ChatListEnvelope>(
    { queryKey: chatKeys.lists() },
    (prev) => {
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
  queryClient.removeQueries({ queryKey: chatKeys.detail(chatId) });
}

// ── Query hooks ─────────────────────────────────────────────────────────────

export function useChatList(projectId?: string) {
  return useQuery({
    queryKey: chatKeys.list(projectId),
    queryFn: () => chatActions.listChats(projectId),
    enabled: !!projectId,
    select: (data) => data.chats,
  });
}

/**
 * The chat list of a scope — one project, or every project when `scope` is
 * null — together with each project's main workspace: everything the sidebar
 * and the mobile list need to group chats by project and workspace, from the
 * one ListChats response. The full worktree list is not involved.
 */
export function useChatListWithWorkspaces(scope: string | null, options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: chatKeys.list(scope),
    queryFn: () => chatActions.listChats(scope ?? undefined),
    enabled: options.enabled ?? true,
  });
}

/**
 * The chat's detail, kept current by the stream. The stream patches this cache
 * as the chat changes (title, state, activity, worktree, config), and every
 * path that reads a chat from the server seeds it (seedChatDetail, which
 * selectChat runs on open) -- so a mount re-reading GetChat is a round trip
 * for what the cache already holds. That duplicate was the extra GetChat on
 * every open and every switch back to a chat.
 *
 * Fresh for CHAT_DETAIL_STALE_MS rather than forever: a missed event heals on
 * the next mount past that, and a stream disruption invalidates every chat
 * query read before it (globalUpdatesStore.handleStatusChange).
 */
export const CHAT_DETAIL_STALE_MS = 5 * 60_000;

export function chatDetailQueryOptions(chatId: string | undefined) {
  return {
    queryKey: chatKeys.detail(chatId),
    queryFn: () => chatActions.getChat(chatId!),
    enabled: !!chatId,
    staleTime: CHAT_DETAIL_STALE_MS,
  };
}

export function useChat(chatId?: string) {
  return useQuery(chatDetailQueryOptions(chatId));
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
    queryFn: () => fetchChatSelection(chatId!),
    enabled: !!chatId,
    staleTime: Infinity,
  });
}

/**
 * The read of a chat's selection: the chat's own GetChat (the selection rides
 * on it), joined to the detail query's request when one is already in flight.
 * A chat opened with no detail cached would otherwise make that call twice,
 * once for the header and once for the composer.
 *
 * It never answers from a cached detail, however fresh: the project list seeds
 * details without a selection, so a cached detail says nothing about whether
 * the chat has one, and a selection read after the stream stopped maintaining
 * it must reach the server. staleTime 0 makes fetchQuery fetch (or join the
 * request in flight) instead of returning the cache.
 */
async function fetchChatSelection(chatId: string): Promise<ChatSelection | null> {
  const chat = await queryClient.fetchQuery({ ...chatDetailQueryOptions(chatId), staleTime: 0 });
  return chat.selection ?? null;
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
    mutationFn: (request: StartChatRequest) => chatActions.startChat(request),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    },
  });
}

export function useDeleteChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => chatActions.deleteChat(chatId),
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
      chatActions.updateChat(chatId, { title } as UpdateChatRequest),
    onSuccess: (_data, { chatId }) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.detail(chatId) });
    },
  });
}

export function useArchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => chatActions.archiveChat(chatId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatServiceKeys.archivedChat });
    },
  });
}

export function useUnarchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => chatActions.unarchiveChat(chatId),
    onSuccess: () => {
      // The chat list carries each chat's workspace, so the restored chat shows
      // with the right group as soon as the list refetches.
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatServiceKeys.archivedChat });
    },
  });
}