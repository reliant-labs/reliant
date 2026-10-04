import { useMemo, useState } from "react";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";

import {
  buildListRunsRequest,
  runGrpc,
  type RunListFilters,
  type RunSummary,
} from "../api/run-grpc";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import { chatKeys, seedChatDetail } from "./chat-queries";

export type { RunSummary, RunListFilters } from "../api/run-grpc";

// ── Key factory ─────────────────────────────────────────────────────────────

export const runKeys = {
  all: ["runs"] as const,
  lists: () => [...runKeys.all, "list"] as const,
  list: (filters: RunListFilters) => [...runKeys.lists(), filters] as const,
};

/**
 * Whether a user update can change what the Runs list shows. Exported so the
 * global updates store asks this module rather than keeping its own list.
 *
 * The sidebar learns about a chat through per-chat cache patches, but the
 * Runs list holds chats the sidebar never lists (automation and agent-started
 * runs), so for these events the list is simply marked stale and refetched.
 */
export function runsInvalidatingUpdate(type: UserUpdateType): boolean {
  return (
    type === UserUpdateType.CHAT_STATE_CHANGE ||
    type === UserUpdateType.CHAT_ACTIVITY_CHANGED ||
    type === UserUpdateType.CHAT_CREATED
  );
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/** Matches the server's default; "Load more" fetches another page this size. */
const RUNS_PAGE_SIZE = 50;

/**
 * The cross-cutting run list, paged by the server's keyset token. Pages
 * append in order, so a run that starts mid-browse cannot shift what has
 * already loaded.
 */
export function useRunList(filters: RunListFilters) {
  // The time window is anchored to when this filter set was first applied,
  // not recomputed per fetch: a "last 24 hours" whose start moved on every
  // refetch would make the first page and the next disagree about the window
  // the cursor is paging through.
  const filterKey = JSON.stringify(filters);
  const [anchor, setAnchor] = useState(() => ({ key: filterKey, now: Date.now() }));
  if (anchor.key !== filterKey) setAnchor({ key: filterKey, now: Date.now() });
  const now = anchor.key === filterKey ? anchor.now : Date.now();

  const query = useInfiniteQuery({
    queryKey: runKeys.list(filters),
    queryFn: ({ pageParam }) =>
      runGrpc.list(
        buildListRunsRequest(filters, { now, pageToken: pageParam, limit: RUNS_PAGE_SIZE }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
  });

  const runs = useMemo<RunSummary[]>(
    () => query.data?.pages.flatMap((page) => page.runs) ?? [],
    [query.data],
  );

  return {
    runs,
    isLoading: query.isLoading,
    isError: query.isError,
    error: query.error,
    refetch: query.refetch,
    fetchNextPage: query.fetchNextPage,
    hasNextPage: query.hasNextPage,
    isFetchingNextPage: query.isFetchingNextPage,
    /** A page after the first failed; the loaded rows stay. */
    isFetchNextPageError: query.isFetchNextPageError,
  };
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

/** Pause, resume or stop a run, then refresh everything that shows it. */
export function useRunControl() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ chatId, action }: { chatId: string; action: "pause" | "resume" | "stop" }) => {
      switch (action) {
        case "pause":
          return runGrpc.pause(chatId);
        case "resume":
          return runGrpc.resume(chatId);
        case "stop":
          return runGrpc.cancel(chatId);
      }
    },
    onSettled: (_data, _error, { chatId }) => {
      void queryClient.invalidateQueries({ queryKey: chatKeys.detail(chatId) });
      void queryClient.invalidateQueries({ queryKey: runKeys.lists() });
    },
  });
}

/** Adopt a run: it becomes a chat in the sidebar (§6.3). */
export function useAdoptRun() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => runGrpc.adopt(chatId),
    onSuccess: (chat) => {
      seedChatDetail(chat);
      void queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    },
  });
}
