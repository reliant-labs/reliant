// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox's data layer (WORKFLOW_UI.md §8): one list of everything waiting
 * on the user, and the counts the nav badge shows. Both are scoped the same
 * way — the current project, or every project — so the badge never counts
 * what the list is not showing.
 *
 * Both are one query per scope: the badge selects the counts from the list.
 * It does not poll. Approvals, questions and waiting-for-machine changes
 * arrive as `chat_activity_changed`, and a run stopping arrives as
 * `chat_state_change`; the global updates store asks `inboxInvalidatingUpdate`
 * and marks the query stale (refetching at most once per
 * STREAM_REFETCH_INTERVAL_MS). The automation failure kinds emit NO user
 * update of their own, so it also refetches whenever the window regains
 * focus, even while fresh.
 */

import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { create as createStore } from "zustand";
import { persist } from "zustand/middleware";

import type { ListInboxResponse } from "../gen/reliant/v1/inbox_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import { queryClient as appQueryClient } from "../lib/query-client";
import { useProjectStore } from "../store/projectStore";
import {
  inboxServiceKeys,
  useCleanupStorage as useCleanupStorageRpc,
  useDismissInboxItem,
  useListInbox,
  useRestoreInboxItem,
} from "./inbox-service-hooks_gen";

export type { InboxItem, ListInboxResponse } from "../gen/reliant/v1/inbox_pb";
export { InboxItemKind } from "../gen/reliant/v1/inbox_pb";

// ── Scope ───────────────────────────────────────────────────────────────────

/** "project": only the current project's items. "all": every project's. */
export type InboxScope = "project" | "all";

interface InboxScopeState {
  scope: InboxScope;
  setScope: (scope: InboxScope) => void;
}

/** The user's last choice, remembered across sessions. Defaults to the project. */
export const useInboxScopeStore = createStore<InboxScopeState>()(
  persist(
    (set) => ({
      scope: "project",
      setScope: (scope) => set({ scope }),
    }),
    { name: "reliant-inbox-scope" },
  ),
);

/**
 * The project the Inbox is scoped to, or undefined for every project. With no
 * current project there is nothing to scope to, so "project" falls back to all.
 */
export function useInboxProjectId(): string | undefined {
  const scope = useInboxScopeStore((state) => state.scope);
  const projectId = useProjectStore((state) => state.currentProject?.id);
  return scope === "project" ? projectId : undefined;
}

// ── Keys ────────────────────────────────────────────────────────────────────
//
// The Inbox is InboxService on forge's generated keys: one ListInbox entry per
// scope (`inboxServiceKeys.listInbox({ projectId })`), every one of them under
// `inboxServiceKeys.listInbox.all`. Every write below is a generated mutation,
// which invalidates `inboxServiceKeys.all` on success; the stream invalidates
// the same root (store/streamInvalidatedScopes, `inbox`).

/**
 * Whether a user update can change what the Inbox holds. Exported so the
 * global updates store asks this module rather than keeping its own list.
 * An async question (ask_user without blocking: true) moves no chat activity, so its
 * coming and going is announced as a refetch of type "inbox".
 */
export function inboxInvalidatingUpdate(type: UserUpdateType, data?: unknown): boolean {
  if (type === UserUpdateType.REFETCH) {
    const payload = typeof data === "string" ? safeParse(data) : data;
    return (payload as { type?: unknown } | null)?.type === "inbox";
  }
  return type === UserUpdateType.CHAT_ACTIVITY_CHANGED || type === UserUpdateType.CHAT_STATE_CHANGE;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

export interface InboxCounts {
  /** Approvals + questions + waiting-for-machine: the items blocking a live run. */
  blockingCount: number;
  /** Any automation failure is waiting (the badge's dot, not a count). */
  hasInformational: boolean;
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/** The one Inbox read: items (default limit, 100) and the counts. */
function inboxRequest(projectId?: string) {
  return { projectId };
}

/** The Inbox's items in the server's priority order (default limit, 100). */
export function useInbox(projectId?: string) {
  return useListInbox(inboxRequest(projectId), { refetchOnWindowFocus: "always" });
}

function countsOf(response: ListInboxResponse): InboxCounts {
  return { blockingCount: response.blockingCount, hasInformational: response.hasInformational };
}

/**
 * The nav badge's counts, read from the SAME query as the list. The server
 * counts every item before it truncates the list, so the list response's
 * counts are the badge's; a separate `limit: 0` read doubled every Inbox
 * refetch for a few KB of items saved.
 */
export function useInboxCounts(projectId?: string) {
  return useListInbox(inboxRequest(projectId), { refetchOnWindowFocus: "always", select: countsOf });
}

// ── Cache helpers ───────────────────────────────────────────────────────────

/** Drop items from every cached list (each scope holds its own copy). */
function dropFromLists(client: QueryClient, ids: Set<string>): void {
  client.setQueriesData<ListInboxResponse>({ queryKey: inboxServiceKeys.listInbox.all }, (previous) =>
    previous ? { ...previous, items: previous.items.filter((item) => !ids.has(item.itemId)) } : previous,
  );
}

/** Mark every Inbox read stale; observed ones refetch. */
export function invalidateInbox(client: QueryClient = appQueryClient): Promise<void> {
  return client.invalidateQueries({ queryKey: inboxServiceKeys.all });
}

/**
 * Take items out of the cached lists at once — after the user acted on them,
 * or learned they were already handled — and mark every inbox query stale so
 * the counts and anything that replaced them come from the server.
 */
export function removeInboxItems(itemIds: string[], client = appQueryClient): void {
  dropFromLists(client, new Set(itemIds));
  void invalidateInbox(client);
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

/**
 * Dismiss items — one row, or a whole section: `mutate({ itemIds })`. They
 * leave every cached list immediately; on error the lists are put back and
 * read again. Any kind can be dismissed: hiding an approval or a question does
 * not resolve it.
 */
export function useDismissInboxItems() {
  const queryClient = useQueryClient();
  return useDismissInboxItem({
    onMutate: async ({ itemIds = [] }) => {
      await queryClient.cancelQueries({ queryKey: inboxServiceKeys.listInbox.all });
      const previous = queryClient.getQueriesData<ListInboxResponse>({ queryKey: inboxServiceKeys.listInbox.all });
      dropFromLists(queryClient, new Set(itemIds));
      return { previous };
    },
    onError: (_error, _request, context) => {
      for (const [key, data] of context?.previous ?? []) queryClient.setQueryData(key, data);
      void invalidateInbox(queryClient);
    },
  });
}

/** Undo a dismissal (`mutate({ itemIds })`): the items come back on the next read. */
export const useRestoreInboxItems = useRestoreInboxItem;

/**
 * Clean up a machine's held worktrees: the machine saves each one's work to a
 * local ref, verifies it, and removes the directory. The caller passes exactly
 * the ids the user confirmed. Whatever the machine reports, the storage item is
 * read again afterwards (the generated write on success, here on failure), so
 * the list shows what is actually left.
 */
export function useCleanupStorage() {
  const queryClient = useQueryClient();
  return useCleanupStorageRpc({
    onError: () => {
      void invalidateInbox(queryClient);
    },
  });
}
