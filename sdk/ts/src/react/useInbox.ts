"use client";
// useInbox: everything waiting on the caller, across projects or within one,
// and the counts a nav badge shows. Dismissing hides an item without resolving
// it: an approval or a question stays pending in its chat.

import { useMutation, useQuery, useQueryClient, type QueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo } from "react";
import {
  InboxService,
  type InboxItem,
  type ListInboxResponse,
} from "#gen/reliant/v1/inbox_pb.js";
import { UserUpdateType } from "#gen/reliant/v1/streaming_pb.js";
import type { ReliantClient } from "../client/client.js";
import type { UserUpdate } from "../client/events.js";
import type { UserStream } from "../client/user-stream.js";
import { useReliantClient } from "./context.js";
import { inboxKeys } from "./keys.js";

export type { InboxItem, ListInboxResponse };

export interface InboxCounts {
  /** Approvals + questions + waiting-for-machine: the items blocking a live run. */
  blockingCount: number;
  /** An automation failure is waiting (the badge's dot, not a count). */
  hasInformational: boolean;
}

export interface UseInboxOptions {
  /** Scope to one project; omit for every project. */
  projectId?: string;
  /**
   * Keep the inbox current from the user-wide update stream (one shared stream
   * per client). Default true. A host that already invalidates the inbox from
   * its own user stream passes false.
   */
  live?: boolean;
  enabled?: boolean;
}

export interface UseInboxResult {
  items: InboxItem[];
  counts: InboxCounts;
  /** More items exist than the list holds. */
  truncated: boolean;
  query: UseQueryResult<ListInboxResponse>;
  /** Hide items from the caller's inbox; they leave every cached list at once and come back if the write fails. */
  dismiss(itemIds: string[]): Promise<void>;
  /** Undo a dismissal. */
  restore(itemIds: string[]): Promise<void>;
  /** Take items out of the cached lists now (acted on, or found already handled), and read the inbox again. */
  remove(itemIds: string[]): void;
}

/**
 * Whether a user update can change what the inbox holds. Approvals, questions
 * and waiting-for-machine changes arrive as a chat activity change, a run
 * stopping as a chat state change, and an async question (which moves no chat
 * activity) as a refetch of type "inbox".
 */
export function inboxInvalidatingUpdate(update: Pick<UserUpdate, "update_type" | "data">): boolean {
  if (update.update_type === UserUpdateType.REFETCH) {
    return (update.data as { type?: unknown } | null)?.type === "inbox";
  }
  return update.update_type === UserUpdateType.CHAT_ACTIVITY_CHANGED || update.update_type === UserUpdateType.CHAT_STATE_CHANGE;
}

const readInbox = (projectId?: string) => ({ projectId });

function dropFromLists(queryClient: QueryClient, ids: ReadonlySet<string>): void {
  queryClient.setQueriesData<ListInboxResponse>({ queryKey: inboxKeys.list.all }, (previous) =>
    previous ? { ...previous, items: previous.items.filter((item) => !ids.has(item.itemId)) } : previous,
  );
}

// ── The shared user stream ──────────────────────────────────────────────────

interface Shared {
  stream: UserStream;
  refs: number;
}
const streams = new WeakMap<ReliantClient, Map<string, Shared>>();

function acquire(client: ReliantClient, queryClient: QueryClient, projectId: string | undefined): () => void {
  const key = projectId ?? "";
  let byProject = streams.get(client);
  if (!byProject) streams.set(client, (byProject = new Map()));
  let shared = byProject.get(key);
  if (!shared) {
    const stream = client.streams.user({
      callbacks: {
        onUpdate: (updates) => {
          if (updates.some(inboxInvalidatingUpdate)) void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
        },
        onError: () => {},
        onStatusChange: () => {},
        onSync: () => {},
      },
    });
    stream.start(0, projectId);
    shared = { stream, refs: 0 };
    byProject.set(key, shared);
  }
  shared.refs++;
  const held = shared;
  return () => {
    held.refs--;
    if (held.refs > 0) return;
    // StrictMode unmounts and remounts in one tick: close on a microtask, only if nobody came back.
    queueMicrotask(() => {
      if (held.refs > 0) return;
      held.stream.stop();
      if (byProject!.get(key) === held) byProject!.delete(key);
    });
  };
}

export function useInbox(options: UseInboxOptions = {}): UseInboxResult {
  const client = useReliantClient();
  const queryClient = useQueryClient();
  const { projectId, live = true, enabled = true } = options;

  // No polling: the stream marks the read stale. The automation failure kinds
  // emit no user update of their own, so the window regaining focus reads again.
  const query = useQuery<ListInboxResponse>({
    queryKey: inboxKeys.list(readInbox(projectId)),
    queryFn: ({ signal }) => client.service(InboxService).listInbox(readInbox(projectId), { signal }),
    enabled,
    refetchOnWindowFocus: "always",
  });

  useEffect(() => {
    if (!live || !enabled) return;
    return acquire(client, queryClient, projectId);
  }, [client, queryClient, projectId, live, enabled]);

  const dismissMutation = useMutation({
    mutationFn: (itemIds: string[]) => client.service(InboxService).dismissInboxItem({ itemIds }),
    onMutate: async (itemIds) => {
      await queryClient.cancelQueries({ queryKey: inboxKeys.list.all });
      const previous = queryClient.getQueriesData<ListInboxResponse>({ queryKey: inboxKeys.list.all });
      dropFromLists(queryClient, new Set(itemIds));
      return { previous };
    },
    onError: (_error, _ids, context) => {
      for (const [key, data] of context?.previous ?? []) queryClient.setQueryData(key, data);
      void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
    },
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: inboxKeys.all }),
  });
  const restoreMutation = useMutation({
    mutationFn: (itemIds: string[]) => client.service(InboxService).restoreInboxItem({ itemIds }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: inboxKeys.all }),
  });

  const dismiss = useCallback(async (itemIds: string[]) => void (await dismissMutation.mutateAsync(itemIds)), [dismissMutation]);
  const restore = useCallback(async (itemIds: string[]) => void (await restoreMutation.mutateAsync(itemIds)), [restoreMutation]);
  const remove = useCallback(
    (itemIds: string[]) => {
      dropFromLists(queryClient, new Set(itemIds));
      void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
    },
    [queryClient],
  );

  const data = query.data;
  const items = useMemo(() => data?.items ?? [], [data]);
  const counts = useMemo<InboxCounts>(
    () => ({ blockingCount: data?.blockingCount ?? 0, hasInformational: data?.hasInformational ?? false }),
    [data],
  );
  return { items, counts, truncated: data?.truncated ?? false, query, dismiss, restore, remove };
}
