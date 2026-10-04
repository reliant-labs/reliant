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
import type { ChatActivity, WorkflowState, WorkflowStopReason } from "../gen/reliant/v1/chat_pb";
import type { RunRangeKey, RunStateFilterKey, RunsSearch } from "../routeSchemas";
import type { Chat } from "../types/chat";

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
export type RunListFilters = Omit<RunsSearch, "allProjects" | "group"> & {
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
    pageToken: options.pageToken || undefined,
    limit: options.limit ?? 0,
    includeArchived: false,
  });
}

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
};
