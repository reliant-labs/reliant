// Copyright (c) 2025 Reliant Labs

/**
 * Thin client over `reliant.v1.RunService` for the Runs surface.
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
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";

import { grpcClient } from "./grpc-client";
import {
  ListRunsRequestSchema,
  RunDisplayState,
  type ListRunsRequest,
  type Run as ProtoRun,
} from "../gen/reliant/v1/run_pb";
import {
  ContentBlockType,
  MessageRole,
  type ChatActivity,
  type WorkflowState,
  type WorkflowStopReason,
} from "../gen/reliant/v1/chat_pb";
import { TriggerEventKind, type TriggerEvent as ProtoTriggerEvent } from "../gen/reliant/v1/trigger_pb";
import type { RunRangeKey, RunStateFilterKey, RunsSearch } from "../routeSchemas";
import type { Chat, Message } from "../types/chat";
import { chatGrpc } from "./chat-grpc";

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
export type RunListFilters = Omit<RunsSearch, "allProjects" | "group" | "parent"> & {
  /** Only runs an agent started from this chat (`?parent=`). */
  parentChatId?: string;
  /** Set to scope to one project; unset lists every project. */
  projectId?: string;
};

/**
 * The ListRunsRequest for a filter set. `now` anchors the time window so a
 * re-render does not shift it; the caller passes the moment the filters were
 * applied.
 */
export function buildListRunsRequest(
  filters: RunListFilters,
  options: { now: number; pageToken?: string; limit?: number },
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
    pageToken: options.pageToken || undefined,
    limit: options.limit ?? 0,
    includeArchived: false,
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
}

/**
 * The event that launched a chat (TriggerService.GetLaunchEvent), with its
 * free-form payload read into the fields the run detail shows. Every
 * payload field is optional: the payload is recorded verbatim at launch, so
 * an older row may lack any of them.
 */
export interface LaunchEvent {
  /** "chat.start", "schedule" or "agent.start_run"; empty for a kind this client does not know. */
  kind: string;
  /** Unset for ad hoc kinds, and once the automation is deleted. */
  triggerId?: string;
  /** RFC3339; for a schedule, the time the fire was FOR. */
  occurredAt: string;
  start?: LaunchStart;
  /** RFC3339 UTC slot of a scheduled fire. */
  scheduledFor?: string;
  /** The automation's name when it fired. */
  triggerName?: string;
  /** A "Run now" fire of a schedule. */
  manual: boolean;
  /** The chat whose agent started this run. */
  parentChatId?: string;
}

const LAUNCH_EVENT_KINDS: Partial<Record<TriggerEventKind, string>> = {
  [TriggerEventKind.CHAT_START]: "chat.start",
  [TriggerEventKind.SCHEDULE]: "schedule",
  [TriggerEventKind.AGENT_START_RUN]: "agent.start_run",
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
  return {
    kind: LAUNCH_EVENT_KINDS[event.kind] ?? "",
    triggerId: event.triggerId || undefined,
    occurredAt: event.occurredAt,
    start: start
      ? {
          workflow: typeof start.workflow === "string" ? start.workflow : "",
          presets: stringRecord(start.presets),
          params: isRecord(start.params) ? start.params : {},
        }
      : undefined,
    scheduledFor: optionalString(payload.scheduled_for),
    triggerName: optionalString(payload.trigger_name),
    manual: payload.manual === true,
    parentChatId: optionalString(payload.parent_chat_id),
  };
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
export function firstPromptOf(messages: Message[], chatId: string): string | undefined {
  let first: Message | undefined;
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
// Client
// ============================================

export const runGrpc = {
  async list(request: ListRunsRequest): Promise<RunPage> {
    const response = await grpcClient.run().listRuns(request);
    return { runs: response.runs.map(runFromProto), nextPageToken: response.nextPageToken };
  },

  async pause(chatId: string): Promise<void> {
    await grpcClient.run().pauseRun({ runId: chatId });
  },

  async resume(chatId: string): Promise<void> {
    await grpcClient.run().resumeRun({ runId: chatId });
  },

  async cancel(chatId: string): Promise<void> {
    await grpcClient.run().cancelRun({ runId: chatId });
  },

  /**
   * Adopt a run into the chat list (§6.3): the user is taking it over, so it
   * lists in the sidebar from now on. Returns the updated chat.
   */
  async adopt(chatId: string): Promise<Chat> {
    const response = await grpcClient.chat().adoptChat({ chatId });
    if (!response.chat) throw new Error("No chat in response");
    const { $typeName: _, ...chat } = response.chat;
    return chat;
  },

  /**
   * The event that launched a chat. Undefined for a chat with none: one that
   * predates launch events. NotFound (not the caller's, or gone) throws.
   */
  async launchEvent(chatId: string): Promise<LaunchEvent | undefined> {
    const response = await grpcClient.trigger().getLaunchEvent({ chatId });
    return response.event ? launchEventFromProto(response.event) : undefined;
  },

  /** The prompt a run was started with; see firstPromptOf. */
  async firstPrompt(chatId: string): Promise<string | undefined> {
    const page = await chatGrpc.listMessages(chatId, { recent: SEED_WINDOW, before_seq: SEED_WINDOW });
    return firstPromptOf(page.messages, chatId);
  },

  /**
   * Undo an adoption: the run leaves the chat list and lives in Runs again.
   * Not an archive; origin and history are untouched. Returns the updated chat.
   */
  async unadopt(chatId: string): Promise<Chat> {
    const response = await grpcClient.chat().unadoptChat({ chatId });
    if (!response.chat) throw new Error("No chat in response");
    const { $typeName: _, ...chat } = response.chat;
    return chat;
  },
};
