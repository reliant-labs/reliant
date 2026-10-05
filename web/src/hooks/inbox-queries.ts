// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox's data layer (WORKFLOW_UI.md §8): one list of everything waiting
 * on the user, and the counts the nav badge shows. Both are scoped the same
 * way — the current project, or every project — so the badge never counts
 * what the list is not showing.
 *
 * Neither polls. Approvals, questions and waiting-for-machine changes arrive
 * as `chat_activity_changed`, and a run stopping arrives as
 * `chat_state_change`; the global updates store asks `inboxInvalidatingUpdate`
 * and marks both queries stale. The automation failure kinds emit NO user
 * update of their own, so both queries also refetch whenever the window
 * regains focus, even while fresh.
 */

import { create } from "@bufbuild/protobuf";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { create as createStore } from "zustand";
import { persist } from "zustand/middleware";

import { grpcClient } from "../api/grpc-client";
import {
  DismissInboxItemRequestSchema,
  ListInboxRequestSchema,
  RestoreInboxItemRequestSchema,
  type ListInboxResponse,
} from "../gen/reliant/v1/inbox_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import { queryClient as appQueryClient } from "../lib/query-client";
import { useProjectStore } from "../store/projectStore";

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

// ── Key factory ─────────────────────────────────────────────────────────────

export const inboxKeys = {
  all: ["inbox"] as const,
  lists: () => [...inboxKeys.all, "list"] as const,
  list: (projectId?: string) => [...inboxKeys.lists(), projectId ?? "*"] as const,
  counts: (projectId?: string) => [...inboxKeys.all, "counts", projectId ?? "*"] as const,
};

/**
 * Whether a user update can change what the Inbox holds. Exported so the
 * global updates store asks this module rather than keeping its own list.
 */
export function inboxInvalidatingUpdate(type: UserUpdateType): boolean {
  return type === UserUpdateType.CHAT_ACTIVITY_CHANGED || type === UserUpdateType.CHAT_STATE_CHANGE;
}

export interface InboxCounts {
  /** Approvals + questions + waiting-for-machine: the items blocking a live run. */
  blockingCount: number;
  /** Any automation failure is waiting (the badge's dot, not a count). */
  hasInformational: boolean;
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/** The Inbox's items in the server's priority order (default limit, 100). */
export function useInbox(projectId?: string) {
  return useQuery({
    queryKey: inboxKeys.list(projectId),
    queryFn: () => grpcClient.inbox().listInbox(create(ListInboxRequestSchema, { projectId })),
    refetchOnWindowFocus: "always",
  });
}

/** The nav badge's counts: `limit: 0` asks the server for no items at all. */
export function useInboxCounts(projectId?: string) {
  return useQuery({
    queryKey: inboxKeys.counts(projectId),
    queryFn: async (): Promise<InboxCounts> => {
      const response = await grpcClient.inbox().listInbox(create(ListInboxRequestSchema, { limit: 0, projectId }));
      return { blockingCount: response.blockingCount, hasInformational: response.hasInformational };
    },
    refetchOnWindowFocus: "always",
  });
}

// ── Cache helpers ───────────────────────────────────────────────────────────

/** Drop items from every cached list (each scope holds its own copy). */
function dropFromLists(client: QueryClient, ids: Set<string>): void {
  client.setQueriesData<ListInboxResponse>({ queryKey: inboxKeys.lists() }, (previous) =>
    previous ? { ...previous, items: previous.items.filter((item) => !ids.has(item.itemId)) } : previous,
  );
}

/**
 * Take items out of the cached lists at once — after the user acted on them,
 * or learned they were already handled — and mark every inbox query stale so
 * the counts and anything that replaced them come from the server.
 */
export function removeInboxItems(itemIds: string[], client = appQueryClient): void {
  dropFromLists(client, new Set(itemIds));
  void client.invalidateQueries({ queryKey: inboxKeys.all });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

/**
 * Dismiss items — one row, or a whole section. They leave every cached list
 * immediately; on error the lists are put back. Any kind can be dismissed:
 * hiding an approval or a question does not resolve it.
 */
export function useDismissInboxItems() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (itemIds: string[]) => {
      await grpcClient.inbox().dismissInboxItem(create(DismissInboxItemRequestSchema, { itemIds }));
    },
    onMutate: async (itemIds) => {
      await queryClient.cancelQueries({ queryKey: inboxKeys.lists() });
      const previous = queryClient.getQueriesData<ListInboxResponse>({ queryKey: inboxKeys.lists() });
      dropFromLists(queryClient, new Set(itemIds));
      return { previous };
    },
    onError: (_error, _itemIds, context) => {
      for (const [key, data] of context?.previous ?? []) queryClient.setQueryData(key, data);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
    },
  });
}

/** Undo a dismissal: the items come back on the next read. */
export function useRestoreInboxItems() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (itemIds: string[]) => {
      await grpcClient.inbox().restoreInboxItem(create(RestoreInboxItemRequestSchema, { itemIds }));
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
    },
  });
}
