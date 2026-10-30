// Copyright (c) 2025 Reliant Labs

/**
 * The Runs surface — `reliant.v1.RunService` — on the generated hooks and
 * keys.
 *
 * The cache holds what the generated hooks fetch (the proto responses) under
 * `runServiceKeys`; components read the plain `RunSummary` mapped from them
 * with `select`, so they never touch `$typeName` or bigint timestamps. Every
 * RunService write invalidates `runServiceKeys.all`, and so does the update
 * stream (globalUpdatesStore, via `runsInvalidatingUpdate`): one prefix
 * reaches every list, the children strip, the live pill and the Library's
 * last-run column.
 *
 * Two things are decided here and nowhere else:
 *
 *   - How the filters a user picks (search params, in the user's words) become
 *     ListRunsRequest fields. `buildListRunsRequest` is pure so that mapping is
 *     testable without a server.
 *   - Which id a run is addressed by. The UI addresses a run by its CHAT id:
 *     it is what /runs/$runId takes, what start_run and get_run report as
 *     `run_id`, what a trigger event carries, and what ChatContainer needs.
 *     The root workflow id (`Run.id`) changes when a chat continues into a new
 *     root run, so it is kept only as `runId` for reference.
 *
 * A run's chat-side reads and writes (adoption, its seed prompt) and its
 * launch event belong to ChatService and TriggerService; they are on those
 * services' generated hooks, and also invalidate the run reads they change.
 */

import { useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";
import { useQueryClient, type InfiniteData } from "@tanstack/react-query";

import {
  ListRunsRequestSchema,
  RunDisplayState,
  UnlaunchedTriggerEventOutcome,
  type LastRunPerWorkflowResponse,
  type ListRunsRequest,
  type ListRunsResponse,
  type Run as ProtoRun,
  type UnlaunchedTriggerEvent as ProtoUnlaunchedTriggerEvent,
} from "../gen/reliant/v1/run_pb";
import {
  ContentBlockType,
  MessageRole,
  type AdoptChatResponse,
  type ChatActivity,
  type ListMessagesResponse,
  type UnadoptChatResponse,
  type WorkflowState,
  type WorkflowStopReason,
} from "../gen/reliant/v1/chat_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import {
  TriggerEventKind,
  type GetLaunchEventResponse,
  type TriggerEvent as ProtoTriggerEvent,
} from "../gen/reliant/v1/trigger_pb";
import type { RunRangeKey, RunStateFilterKey, RunsSearch } from "../routeSchemas";
import { chatKeys, seedChatDetail } from "./chat-queries";
import { useAdoptChat, useListMessages, useUnadoptChat } from "./chat-service-hooks_gen";
import {
  runServiceKeys,
  useCancelRun,
  useLastRunPerWorkflow as useLastRunPerWorkflowRpc,
  useListRuns,
  useListRunsInfinite,
  usePauseRun,
  useResumeRun,
} from "./run-service-hooks_gen";
import { useGetLaunchEvent } from "./trigger-service-hooks_gen";

// ============================================
// Frontend types
// ============================================

/** One row of the cross-cutting run list. */
export interface RunSummary {
  /** The root workflow id. Not the address of the run; see the module comment. */
  runId: string;
  /** The backing chat id: the run's address in the UI. */
  chatId: string;
  title: string;
  workflowName: string;
  projectId: string;
  /** Empty for a run that predates launch events; read as chat.start. */
  launchKind: string;
  triggerId: string;
  /** The automation's current name; empty when it has been deleted. */
  triggerName: string;
  daemonId: string;
  state: WorkflowState;
  stopReason: WorkflowStopReason;
  activity: ChatActivity;
  displayState: RunDisplayState;
  outcome: string;
  /** Epoch ms. */
  createdAt: number;
  /** Epoch ms; unset while the run has not finished. */
  completedAt?: number;
  /** The chat whose agent started this run; set for agent.start_run only. */
  parentChatId?: string;
  /** That chat's title; unset when it is gone or not the caller's. */
  parentChatTitle?: string;
  /**
   * The run's chat is archived: a person put it away, or it is an automation
   * run that finished successfully and archived itself.
   */
  archived: boolean;
}

export interface RunPage {
  runs: RunSummary[];
  /** Empty on the last page. */
  nextPageToken: string;
}

export function runFromProto(run: ProtoRun): RunSummary {
  const completedAt = Number(run.completedAtMs);
  return {
    runId: run.id,
    chatId: run.sessionId || run.id,
    title: run.title,
    workflowName: run.workflowName,
    projectId: run.projectId,
    launchKind: run.launchKind,
    triggerId: run.triggerId,
    triggerName: run.triggerName,
    daemonId: run.daemonId,
    state: run.state,
    stopReason: run.stopReason,
    activity: run.activity,
    displayState: run.displayState,
    outcome: run.outcome,
    createdAt: Number(run.createdAtMs),
    completedAt: completedAt > 0 ? completedAt : undefined,
    parentChatId: run.parentChatId || undefined,
    parentChatTitle: run.parentChatTitle || undefined,
    archived: run.archived,
  };
}

/**
 * A trigger event that started no run — Skipped, or Couldn't start — listed
 * in the run log beside the runs, so "why didn't last night's run happen" is
 * answered where runs are.
 */
export interface UnlaunchedEventSummary {
  id: string;
  triggerId: string;
  triggerName: string;
  /** The workflow the trigger runs, as stored on it. */
  workflowName: string;
  /** The declared trigger it activates; empty for a hand-made one. */
  workflowTrigger: string;
  projectId: string;
  launchKind: string;
  /** Epoch ms: the time the firing was for. */
  occurredAt: number;
  outcome: "skipped" | "failed";
  outcomeDetail: string;
}

export function unlaunchedEventFromProto(event: ProtoUnlaunchedTriggerEvent): UnlaunchedEventSummary {
  return {
    id: event.id,
    triggerId: event.triggerId,
    triggerName: event.triggerName,
    workflowName: event.workflowName,
    workflowTrigger: event.workflowTrigger,
    projectId: event.projectId,
    launchKind: event.launchKind,
    occurredAt: Number(event.occurredAtMs),
    outcome: event.outcome === UnlaunchedTriggerEventOutcome.SKIPPED ? "skipped" : "failed",
    outcomeDetail: event.outcomeDetail,
  };
}

// ============================================
// Filters → request
// ============================================

/** A state chip, and the display states it stands for. */
export interface RunStateFilter {
  key: RunStateFilterKey;
  label: string;
  displayStates: RunDisplayState[];
}

/**
 * The state chips, in the order they render. "Needs you" includes waiting for
 * a machine: both are a live run that will not move until something outside
 * it changes, which is what the Needs-you section means (§5.3).
 */
export const RUN_STATE_FILTERS: readonly RunStateFilter[] = [
  {
    key: "needs_you",
    label: "Needs you",
    displayStates: [RunDisplayState.NEEDS_INPUT, RunDisplayState.WAITING_FOR_MACHINE],
  },
  {
    key: "live",
    label: "Live",
    displayStates: [
      RunDisplayState.QUEUED,
      RunDisplayState.RUNNING,
      RunDisplayState.PAUSED,
      RunDisplayState.WAITING_FOR_MACHINE,
    ],
  },
  { key: "failed", label: "Failed", displayStates: [RunDisplayState.FAILED] },
  { key: "completed", label: "Completed", displayStates: [RunDisplayState.COMPLETED] },
  { key: "cancelled", label: "Cancelled", displayStates: [RunDisplayState.CANCELLED] },
];

const RANGE_MS: Record<Exclude<RunRangeKey, "all">, number> = {
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
  "30d": 30 * 24 * 60 * 60 * 1000,
};

export const DEFAULT_RUN_RANGE: RunRangeKey = "24h";

/** The search-param filters plus the resolved project (decision 10). */
export type RunListFilters = Omit<RunsSearch, "allProjects" | "group" | "parent" | "project"> & {
  /** Only runs an agent started from this chat (`?parent=`). */
  parentChatId?: string;
  /** Set to scope to one project; unset lists every project. */
  projectId?: string;
  /**
   * Also list the trigger events that started no run, in the same pages. Only
   * with no state filter and no parent: an event has neither (the server
   * returns none then anyway).
   */
  includeUnlaunched?: boolean;
};

/**
 * The ListRunsRequest for a filter set. `now` anchors the time window so a
 * re-render does not shift it; the caller passes the moment the filters were
 * applied. The page token is not part of it: the infinite query owns that.
 */
export function buildListRunsRequest(
  filters: RunListFilters,
  options: { now: number; limit?: number },
): ListRunsRequest {
  const displayStates = new Set<RunDisplayState>();
  for (const key of filters.state ?? []) {
    for (const state of RUN_STATE_FILTERS.find((f) => f.key === key)?.displayStates ?? []) {
      displayStates.add(state);
    }
  }
  const range = filters.range ?? DEFAULT_RUN_RANGE;
  const query = filters.q?.trim();
  return create(ListRunsRequestSchema, {
    projectId: filters.projectId || undefined,
    workflow: filters.workflow ?? [],
    triggerId: filters.trigger || undefined,
    launchKind: filters.kind ?? [],
    displayStates: [...displayStates],
    startedAfter: range === "all" ? undefined : timestampFromMs(options.now - RANGE_MS[range]),
    query: query || undefined,
    parentChatId: filters.parentChatId || undefined,
    limit: options.limit ?? 0,
    // Runs is history: an automation archives each run it finishes
    // successfully, and those must still be found here. Rows say so.
    includeArchived: true,
    includeUnlaunchedEvents:
      filters.includeUnlaunched && displayStates.size === 0 && !filters.parentChatId ? true : undefined,
  });
}

// ============================================
// Launch event
// ============================================

/** What a run was started with, as the launcher recorded it. */
export interface LaunchStart {
  /** The resolved workflow name. */
  workflow: string;
  /** Preset per input group; "" is the workflow-level group. */
  presets: Record<string, string>;
  /** Explicit input values, nested by group. */
  params: Record<string, unknown>;
  /** The seed prompt, exactly as the run started; absent on runs recorded before it was kept. */
  prompt?: string;
}

/**
 * The event that launched a chat (TriggerService.GetLaunchEvent), with its
 * free-form payload read into the fields the run detail shows. Every
 * payload field is optional: the payload is recorded verbatim at launch, so
 * an older row may lack any of them.
 */
export interface LaunchEvent {
  /** The launch kind ("chat.start", "schedule", "webhook", …); empty for a kind this client does not know. */
  kind: string;
  /** Unset for ad hoc kinds, and once the automation is deleted. */
  triggerId?: string;
  /** RFC3339; for a schedule, the time the fire was FOR. */
  occurredAt: string;
  start?: LaunchStart;
  /** RFC3339 UTC slot of a scheduled fire. */
  scheduledFor?: string;
  /** RFC3339 UTC time the fire actually ran; absent on events recorded before it was kept. */
  firedAt?: string;
  /** The automation's name when it fired. */
  triggerName?: string;
  /** A "Run now" fire of a schedule. */
  manual: boolean;
  /** A manual.run that replayed a past trigger event. */
  replayedEvent?: boolean;
  /** The chat whose agent started this run. */
  parentChatId?: string;
  /** A provider event's integration ("github"). */
  integration?: string;
  /** A provider event's type ("issues.opened"). */
  providerEvent?: string;
  /** A workflow event's source run: its workflow. */
  sourceWorkflow?: string;
  /** A workflow event's source run: its outcome ("finished", "failed", "blocked"). */
  sourceOutcome?: string;
}

/** Every kind, so a kind added to the proto has to be named here to compile. */
const LAUNCH_EVENT_KINDS: Record<TriggerEventKind, string> = {
  [TriggerEventKind.UNSPECIFIED]: "",
  [TriggerEventKind.CHAT_START]: "chat.start",
  [TriggerEventKind.SCHEDULE]: "schedule",
  [TriggerEventKind.AGENT_START_RUN]: "agent.start_run",
  [TriggerEventKind.BUILDER_TEST]: "builder.test",
  [TriggerEventKind.MANUAL_RUN]: "manual.run",
  [TriggerEventKind.CLI_RUN]: "cli.run",
  [TriggerEventKind.WEBHOOK]: "webhook",
  [TriggerEventKind.INTEGRATION]: "integration",
  [TriggerEventKind.WORKFLOW_EVENT]: "workflow_event",
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function stringRecord(value: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (!isRecord(value)) return out;
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry === "string") out[key] = entry;
  }
  return out;
}

export function launchEventFromProto(event: ProtoTriggerEvent): LaunchEvent {
  const payload: Record<string, unknown> = isRecord(event.payload) ? event.payload : {};
  const start = isRecord(payload.start) ? payload.start : undefined;
  const kind = LAUNCH_EVENT_KINDS[event.kind] ?? "";
  // Read by kind: each source owns its payload's keys, and a webhook's are
  // whatever the sender posted.
  const integration = kind === "integration";
  const workflowEvent = kind === "workflow_event";
  return {
    kind,
    triggerId: event.triggerId || undefined,
    occurredAt: event.occurredAt,
    start: start
      ? {
          workflow: typeof start.workflow === "string" ? start.workflow : "",
          presets: stringRecord(start.presets),
          params: isRecord(start.params) ? start.params : {},
          prompt: optionalString(start.prompt),
        }
      : undefined,
    scheduledFor: optionalString(payload.scheduled_for),
    firedAt: optionalString(payload.fired_at),
    triggerName: optionalString(payload.trigger_name),
    manual: payload.manual === true,
    replayedEvent: isRecord(payload.replay) ? true : undefined,
    parentChatId: optionalString(payload.parent_chat_id),
    integration: integration ? optionalString(payload.integration) : undefined,
    providerEvent: integration ? optionalString(payload.event) : undefined,
    sourceWorkflow: workflowEvent ? optionalString(payload.workflow_name) : undefined,
    sourceOutcome: workflowEvent ? optionalString(payload.outcome) : undefined,
  };
}

/** The part of a message firstPromptOf reads: a proto Message, or the app's own. */
export interface SeedMessage {
  role: MessageRole;
  chatId: string;
  seq: bigint;
  contentBlocks: readonly { type: ContentBlockType; content?: string }[];
}

/**
 * The prompt a run was started with: the earliest user message the chat
 * itself holds.
 *
 * The launch event deliberately does not record message text (the messages
 * table holds it), so it is read back from there. A launched chat's seed
 * messages are its first rows, and `seq` is chat-global, so a small window
 * below a low cursor reaches them without loading the transcript. Only rows
 * this chat owns count: a branch would otherwise report its parent's prompt.
 */
export function firstPromptOf(messages: readonly SeedMessage[], chatId: string): string | undefined {
  let first: SeedMessage | undefined;
  for (const message of messages) {
    if (message.role !== MessageRole.USER || message.chatId !== chatId) continue;
    if (!first || message.seq < first.seq) first = message;
  }
  if (!first) return undefined;
  const text = first.contentBlocks
    .filter((block) => block.type === ContentBlockType.TEXT && block.content)
    .map((block) => block.content)
    .join("\n\n")
    .trim();
  return text || undefined;
}

/** How far into a chat to look for its seed messages. */
const SEED_WINDOW = 16;

// ============================================
// Errors
// ============================================

export function runErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

// ============================================
// Stream invalidation
// ============================================

/**
 * Whether a user update can change what the Runs list shows. Exported so the
 * global updates store asks this module rather than keeping its own list; it
 * invalidates `runServiceKeys.all` when this says yes.
 *
 * The sidebar learns about a chat through per-chat cache patches, but the
 * Runs list holds chats the sidebar never lists (automation and agent-started
 * runs), so for these events every run read is simply marked stale and
 * refetched.
 */
export function runsInvalidatingUpdate(type: UserUpdateType): boolean {
  return (
    type === UserUpdateType.CHAT_STATE_CHANGE ||
    type === UserUpdateType.CHAT_ACTIVITY_CHANGED ||
    type === UserUpdateType.CHAT_CREATED
  );
}

// ============================================
// Reads
// ============================================

export function runPageFromProto(response: ListRunsResponse): RunPage {
  return { runs: response.runs.map(runFromProto), nextPageToken: response.nextPageToken };
}

/** Matches the server's default; "Load more" fetches another page this size. */
const RUNS_PAGE_SIZE = 50;

/**
 * How long a filter set's time window outlives the list that anchored it:
 * React Query's default gcTime, so returning to a list that is still cached
 * asks for the same window, finds it under the same key, and shows it while
 * it refetches.
 */
const WINDOW_REUSE_MS = 5 * 60 * 1000;
const windowAnchors = new Map<string, number>();

function windowAnchor(filterKey: string): number {
  const now = Date.now();
  for (const [key, at] of windowAnchors) {
    if (now - at >= WINDOW_REUSE_MS) windowAnchors.delete(key);
  }
  const anchored = windowAnchors.get(filterKey);
  if (anchored !== undefined) return anchored;
  windowAnchors.set(filterKey, now);
  return now;
}

const NO_RUNS: RunSummary[] = [];

interface RunLog {
  runs: RunSummary[];
  events: UnlaunchedEventSummary[];
}

function runLogOfPages(data: InfiniteData<ListRunsResponse, string>): RunLog {
  return {
    runs: data.pages.flatMap((page) => page.runs.map(runFromProto)),
    events: data.pages.flatMap((page) => page.unlaunchedEvents.map(unlaunchedEventFromProto)),
  };
}

const NO_EVENTS: UnlaunchedEventSummary[] = [];

/**
 * The cross-cutting run list, paged by the server's keyset token. Pages
 * append in order, so a run that starts mid-browse cannot shift what has
 * already loaded.
 */
export function useRunList(filters: RunListFilters) {
  // The time window is anchored to when this filter set was first applied,
  // not recomputed per fetch: a "last 24 hours" whose start moved on every
  // refetch would make the first page and the next disagree about the window
  // the cursor is paging through. It is part of the request, so of the key.
  const filterKey = JSON.stringify(filters);
  const [anchor, setAnchor] = useState(() => ({ key: filterKey, now: windowAnchor(filterKey) }));
  if (anchor.key !== filterKey) setAnchor({ key: filterKey, now: windowAnchor(filterKey) });
  const now = anchor.key === filterKey ? anchor.now : windowAnchor(filterKey);

  const query = useListRunsInfinite(buildListRunsRequest(filters, { now, limit: RUNS_PAGE_SIZE }), {
    select: runLogOfPages,
  });

  return {
    runs: query.data?.runs ?? NO_RUNS,
    /** Trigger events that started no run (with includeUnlaunched), newest first. */
    events: query.data?.events ?? NO_EVENTS,
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

function launchEventOf(response: GetLaunchEventResponse): LaunchEvent | null {
  return response.event ? launchEventFromProto(response.event) : null;
}

/**
 * The event that launched a chat: who started it, the scheduled slot, and
 * what it was started with. `data` is null for a chat with no launch event
 * (one from before launch events existed), which is not an error.
 *
 * A launch event is written once and never changes, so it is never refetched.
 */
export function useLaunchEvent(chatId: string | undefined) {
  return useGetLaunchEvent(
    { chatId: chatId ?? "" },
    { enabled: !!chatId, staleTime: Infinity, select: launchEventOf },
  );
}

/**
 * The prompt a run was started with, read from its first messages (the launch
 * event records no message text). Immutable, so never refetched. Only fetched
 * when a caller asks; `enabled` lets run detail defer it until needed.
 */
export function useFirstPrompt(chatId: string | undefined, enabled = true) {
  const id = chatId ?? "";
  const select = useMemo(
    () => (response: ListMessagesResponse) => firstPromptOf(response.messages, id) ?? null,
    [id],
  );
  return useListMessages(
    { chatId: id, recent: SEED_WINDOW, beforeSeq: BigInt(SEED_WINDOW) },
    { enabled: !!chatId && enabled, staleTime: Infinity, select },
  );
}

/** How many child runs the parent chat's header lists. */
export const CHILD_RUNS_LIMIT = 5;

function childRunsOf(response: ListRunsResponse) {
  return {
    runs: response.runs.slice(0, CHILD_RUNS_LIMIT).map(runFromProto),
    /** More than the strip shows. */
    hasMore: response.runs.length > CHILD_RUNS_LIMIT || response.nextPageToken !== "",
  };
}

/**
 * The runs an agent started from this chat (decision 4: they are not listed
 * in the sidebar, so the parent's header is where they are discovered).
 *
 * One small page, for ONE chat: the open chat's header, never a sidebar row.
 * Under `runServiceKeys.all`, so the update stream's invalidation on
 * chat_created and chat_activity_changed refreshes it; it does not poll.
 * Every project and every time: a child is a child wherever it runs.
 */
export function useChildRuns(parentChatId: string | undefined, enabled = true) {
  return useListRuns(
    { parentChatId, limit: CHILD_RUNS_LIMIT + 1, includeArchived: false },
    { enabled: !!parentChatId && enabled, select: childRunsOf },
  );
}

function lastRunsByWorkflow(response: LastRunPerWorkflowResponse): Map<string, RunSummary> {
  return new Map(response.runs.map((run) => [run.workflowName, runFromProto(run)]));
}

/**
 * The newest run of every workflow, in one project or (allProjects) in every
 * one, for the Workflows list's "Last run" column and
 * workflow detail, keyed by workflow name (the stored ref, e.g.
 * `builtin://agent`). One request for the whole list (LastRunPerWorkflow),
 * never one per row.
 */
export function useLastRunPerWorkflow(projectId: string | undefined, allProjects = false) {
  return useLastRunPerWorkflowRpc(
    { projectId: allProjects ? undefined : projectId },
    { enabled: allProjects || !!projectId, select: lastRunsByWorkflow },
  );
}

// ============================================
// Writes
// ============================================

export type RunControlAction = "pause" | "resume" | "stop";

/**
 * Pause, resume or stop a run, by its chat id, then refresh everything that
 * shows it. A success invalidates `runServiceKeys.all` (the generated
 * mutations do); a failure does too, because a refused control usually means
 * the run moved first (it finished, or someone else stopped it).
 */
export function useRunControl() {
  const queryClient = useQueryClient();
  const refresh = {
    onError: () => void queryClient.invalidateQueries({ queryKey: runServiceKeys.all }),
    onSettled: (_data: unknown, _error: unknown, request: { runId?: string }) =>
      void queryClient.invalidateQueries({ queryKey: chatKeys.detail(request.runId) }),
  };
  const pause = usePauseRun(refresh);
  const resume = useResumeRun(refresh);
  const stop = useCancelRun(refresh);
  return {
    mutate: (
      { chatId, action }: { chatId: string; action: RunControlAction },
      callbacks?: { onError?: (error: Error) => void },
    ) => {
      const control = action === "pause" ? pause : action === "resume" ? resume : stop;
      control.mutate({ runId: chatId }, callbacks);
    },
    isPending: pause.isPending || resume.isPending || stop.isPending,
  };
}

/**
 * Adoption moves a run between the chat list and Runs: the chat list
 * refetches (which is what adds or removes the row), the detail cache is
 * patched so anything showing the chat agrees, and every run read refetches.
 */
function useAdoptionRefresh() {
  const queryClient = useQueryClient();
  return (response: AdoptChatResponse | UnadoptChatResponse) => {
    if (response.chat) {
      const { $typeName: _, ...chat } = response.chat;
      seedChatDetail(chat);
    }
    void queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    void queryClient.invalidateQueries({ queryKey: runServiceKeys.all });
  };
}

/** Adopt a run: it becomes a chat in the sidebar (§6.3). `mutate({ chatId })`. */
export function useAdoptRun() {
  return useAdoptChat({ onSuccess: useAdoptionRefresh() });
}

/**
 * Un-adopt a run ("Move back to Runs", §6.3): it leaves the sidebar list and
 * stays in Runs. Not an archive; origin and history are untouched.
 * `mutate({ chatId })`.
 */
export function useUnadoptRun() {
  return useUnadoptChat({ onSuccess: useAdoptionRefresh() });
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
  for (const run of runs) {
    if (!run.launchKind || run.launchKind === "chat.start") continue;
    if (excludeChatIds.has(run.chatId)) continue;
    if (!LIVE_AUTOMATION_STATES.includes(run.displayState)) continue;
    running += 1;
  }
  return { running, truncated };
}

/**
 * Live runs across every project, for the sidebar footer pill.
 *
 * Under `runServiceKeys.all`, so it does not poll: the global updates store
 * already invalidates that prefix on chat_state_change, chat_activity_changed
 * and chat_created, which are exactly the events that start, block or end a
 * run. No time window: a run that started three days ago and is still going is
 * still live.
 */
export function useLiveRuns() {
  return useListRuns(
    { displayStates: [...LIVE_AUTOMATION_STATES], limit: LIVE_AUTOMATION_LIMIT, includeArchived: false },
    { select: runPageFromProto },
  );
}

