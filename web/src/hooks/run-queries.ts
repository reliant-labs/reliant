import { useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  buildListRunsRequest,
  runGrpc,
  type RunListFilters,
  type RunSummary,
} from "../api/run-grpc";
import { ListRunsRequestSchema, RunDisplayState } from "../gen/reliant/v1/run_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import { chatKeys, seedChatDetail } from "./chat-queries";

export type { RunSummary, RunListFilters } from "../api/run-grpc";

// ── Key factory ─────────────────────────────────────────────────────────────

export const runKeys = {
  all: ["runs"] as const,
  lists: () => [...runKeys.all, "list"] as const,
  list: (filters: RunListFilters) => [...runKeys.lists(), filters] as const,
  /** Under lists(), so the update stream's invalidation refreshes it too. */
  lastPerWorkflow: (projectId?: string) => [...runKeys.lists(), "lastPerWorkflow", projectId ?? null] as const,
  /** Under lists(): the update stream's invalidation reaches it too. */
  children: (parentChatId: string) => [...runKeys.lists(), "children", parentChatId] as const,
  launchEvent: (chatId: string) => [...runKeys.all, "launch-event", chatId] as const,
  firstPrompt: (chatId: string) => [...runKeys.all, "first-prompt", chatId] as const,
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

/**
 * The event that launched a chat: who started it, the scheduled slot, and
 * what it was started with. `data` is null for a chat with no launch event
 * (one from before launch events existed), which is not an error.
 *
 * A launch event is written once and never changes, so it is never refetched.
 */
export function useLaunchEvent(chatId: string | undefined) {
  return useQuery({
    queryKey: runKeys.launchEvent(chatId ?? ""),
    queryFn: async () => (await runGrpc.launchEvent(chatId!)) ?? null,
    enabled: !!chatId,
    staleTime: Infinity,
  });
}

/**
 * The prompt a run was started with, read from its first messages (the launch
 * event records no message text). Immutable, so never refetched. Only fetched
 * when a caller asks; `enabled` lets run detail defer it until needed.
 */
export function useFirstPrompt(chatId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: runKeys.firstPrompt(chatId ?? ""),
    queryFn: async () => (await runGrpc.firstPrompt(chatId!)) ?? null,
    enabled: !!chatId && enabled,
    staleTime: Infinity,
  });
}

/** How many child runs the parent chat's header lists. */
export const CHILD_RUNS_LIMIT = 5;

/**
 * The runs an agent started from this chat (decision 4: they are not listed
 * in the sidebar, so the parent's header is where they are discovered).
 *
 * One small page, for ONE chat: the open chat's header, never a sidebar row.
 * Keyed under `runKeys.lists()`, so the update stream's invalidation on
 * chat_created and chat_activity_changed refreshes it; it does not poll.
 * Every project and every time: a child is a child wherever it runs.
 */
export function useChildRuns(parentChatId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: runKeys.children(parentChatId ?? ""),
    queryFn: () =>
      runGrpc.list(
        create(ListRunsRequestSchema, {
          parentChatId,
          limit: CHILD_RUNS_LIMIT + 1,
          includeArchived: false,
        }),
      ),
    enabled: !!parentChatId && enabled,
    select: (page) => ({
      runs: page.runs.slice(0, CHILD_RUNS_LIMIT),
      /** More than the strip shows. */
      hasMore: page.runs.length > CHILD_RUNS_LIMIT || page.nextPageToken !== "",
    }),
  });
}

/**
 * The newest run of every workflow, for the Library's "Last run" column and
 * workflow detail. One request for the whole list (LastRunPerWorkflow), never
 * one per row.
 */
export function useLastRunPerWorkflow(projectId: string | undefined) {
  return useQuery({
    queryKey: runKeys.lastPerWorkflow(projectId),
    queryFn: () => runGrpc.lastRunPerWorkflow(projectId),
    enabled: !!projectId,
  });
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
      void queryClient.invalidateQueries({ queryKey: runKeys.lists() });
    },
  });
}

/**
 * Un-adopt a run ("Move back to Runs", §6.3): it leaves the sidebar list and
 * stays in Runs. The list refetch is what removes the row; the detail cache is
 * patched so anything showing the chat stops calling it adopted.
 */
export function useUnadoptRun() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => runGrpc.unadopt(chatId),
    onSuccess: (chat) => {
      seedChatDetail(chat);
      void queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      void queryClient.invalidateQueries({ queryKey: runKeys.lists() });
    },
  });
}

// ── Live automation runs (the sidebar footer pill) ──────────────────────────

/**
 * What the pill counts: a run that is executing, or blocked on the user or on
 * a machine. Queued and paused are deliberately out: neither is "running",
 * and a paused run waits for a person who already paused it.
 */
const LIVE_AUTOMATION_STATES: readonly RunDisplayState[] = [
  RunDisplayState.RUNNING,
  RunDisplayState.NEEDS_INPUT,
  RunDisplayState.WAITING_FOR_MACHINE,
];

/** The pill reads one page; a count past this reads "N+". */
export const LIVE_AUTOMATION_LIMIT = 100;

export interface LiveAutomationSummary {
  running: number;
  needsYou: number;
  /** True when the server had more than one page: the counts are a floor. */
  truncated: boolean;
}

/**
 * Count live automation runs that are NOT in the chat list.
 *
 * `excludeChatIds` is the sidebar's own list: an adopted run that is live is
 * already a row with an activity dot, so counting it again in the pill would
 * report it twice. Pure so the counting rule is testable without a server.
 */
export function summarizeLiveAutomations(
  runs: readonly RunSummary[],
  excludeChatIds: ReadonlySet<string>,
  truncated = false,
): LiveAutomationSummary {
  let running = 0;
  let needsYou = 0;
  for (const run of runs) {
    if (!run.launchKind || run.launchKind === "chat.start") continue;
    if (excludeChatIds.has(run.chatId)) continue;
    if (!LIVE_AUTOMATION_STATES.includes(run.displayState)) continue;
    running += 1;
    if (run.displayState === RunDisplayState.NEEDS_INPUT) needsYou += 1;
  }
  return { running, needsYou, truncated };
}

/**
 * Live runs across every project, for the sidebar footer pill.
 *
 * Keyed under `runKeys.lists()`, so it does not poll: the global updates store
 * already invalidates that prefix on chat_state_change, chat_activity_changed
 * and chat_created, which are exactly the events that start, block or end a
 * run. No time window: a run that started three days ago and is still going is
 * still live.
 */
export function useLiveRuns() {
  return useQuery({
    queryKey: [...runKeys.lists(), "live-pill"] as const,
    queryFn: () =>
      runGrpc.list(
        create(ListRunsRequestSchema, {
          displayStates: [...LIVE_AUTOMATION_STATES],
          limit: LIVE_AUTOMATION_LIMIT,
          includeArchived: false,
        }),
      ),
  });
}
