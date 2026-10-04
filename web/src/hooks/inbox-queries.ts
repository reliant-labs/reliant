// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox's data layer (WORKFLOW_UI.md §8): one list of everything waiting
 * on the user, and the counts the nav badge shows.
 *
 * Neither polls. Approvals, questions and waiting-for-machine changes arrive
 * as `chat_activity_changed`, and a run stopping arrives as
 * `chat_state_change`; the global updates store asks `inboxInvalidatingUpdate`
 * and marks both queries stale. The automation failure kinds emit NO user
 * update of their own, so both queries also refetch whenever the window
 * regains focus, even while fresh.
 */

import { create } from "@bufbuild/protobuf";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { grpcClient } from "../api/grpc-client";
import {
  DismissInboxItemRequestSchema,
  ListInboxRequestSchema,
  type ListInboxResponse,
} from "../gen/reliant/v1/inbox_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import { queryClient as appQueryClient } from "../lib/query-client";

export type { InboxItem, ListInboxResponse } from "../gen/reliant/v1/inbox_pb";
export { InboxItemKind } from "../gen/reliant/v1/inbox_pb";

// ── Key factory ─────────────────────────────────────────────────────────────

export const inboxKeys = {
  all: ["inbox"] as const,
  list: () => [...inboxKeys.all, "list"] as const,
  counts: () => [...inboxKeys.all, "counts"] as const,
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
export function useInbox() {
  return useQuery({
    queryKey: inboxKeys.list(),
    queryFn: () => grpcClient.inbox().listInbox(create(ListInboxRequestSchema, {})),
    refetchOnWindowFocus: "always",
  });
}

/** The nav badge's counts: `limit: 0` asks the server for no items at all. */
export function useInboxCounts() {
  return useQuery({
    queryKey: inboxKeys.counts(),
    queryFn: async (): Promise<InboxCounts> => {
      const response = await grpcClient.inbox().listInbox(create(ListInboxRequestSchema, { limit: 0 }));
      return { blockingCount: response.blockingCount, hasInformational: response.hasInformational };
    },
    refetchOnWindowFocus: "always",
  });
}

// ── Cache helpers ───────────────────────────────────────────────────────────

/**
 * Take items out of the cached list at once — after the user acted on them, or
 * learned they were already handled — and mark both queries stale so the
 * counts and anything that replaced them come from the server.
 */
export function removeInboxItems(itemIds: string[], client = appQueryClient): void {
  const ids = new Set(itemIds);
  client.setQueryData<ListInboxResponse>(inboxKeys.list(), (previous) =>
    previous ? { ...previous, items: previous.items.filter((item) => !ids.has(item.itemId)) } : previous,
  );
  void client.invalidateQueries({ queryKey: inboxKeys.all });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

/**
 * Dismiss a failure item. The row leaves immediately; on error it comes back.
 * Approvals and questions are not dismissable: they clear when resolved.
 */
export function useDismissInboxItem() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (itemId: string) => {
      await grpcClient.inbox().dismissInboxItem(create(DismissInboxItemRequestSchema, { itemId }));
    },
    onMutate: async (itemId) => {
      await queryClient.cancelQueries({ queryKey: inboxKeys.list() });
      const previous = queryClient.getQueryData<ListInboxResponse>(inboxKeys.list());
      if (previous) {
        queryClient.setQueryData<ListInboxResponse>(inboxKeys.list(), {
          ...previous,
          items: previous.items.filter((item) => item.itemId !== itemId),
        });
      }
      return { previous };
    },
    onError: (_error, _itemId, context) => {
      if (context?.previous) queryClient.setQueryData(inboxKeys.list(), context.previous);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
    },
  });
}
