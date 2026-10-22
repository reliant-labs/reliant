// Copyright (c) 2025 Reliant Labs

/**
 * A test run, as the builder's canvas shows it: each step's state, the path
 * the run took, and each loop's iterations.
 *
 * Everything here is derived from what the Runs viewer already reads — the
 * node_execution stream (status, plus the event that decided it) and the
 * execution tree (loop iterations) — so the canvas and the run's own page
 * cannot disagree. The stream's coarse status says running/completed/failed;
 * its deciding event adds the two states a builder needs and the coarse
 * status hides:
 *
 *   - skipped: the node's condition was false. Its activity completes, so the
 *     lifecycle says "completed", but the event's status says SKIPPED.
 *   - waiting: an approval or ask-user node's last act was raising its
 *     request (ApprovalCreate / QuestionCreate completed) and the run is now
 *     waiting on a person — nothing has happened since, or the run says it
 *     needs you.
 */

import type { NodeExecutionUpdate } from "../../../types/streaming";
import { NodeExecutionStatus as ProtoNodeExecutionStatus } from "../../../gen/reliant/v1/streaming_pb";
import type { NodeExecutionStatus } from "../../../lib/workflow-flow";
import type { LoopExecutionInfo } from "../hooks/useExecutionStatus";

export type RunNodeStatus = "running" | "completed" | "failed" | "skipped" | "waiting";

export interface RunNodeState {
  status: RunNodeStatus;
  /** What the step reported when it failed. */
  error?: string;
  /** A loop's iterations so far. */
  loop?: LoopExecutionInfo;
}

export interface BuilderRunView {
  nodes: Record<string, RunNodeState>;
  /** Edge ids the run went along. */
  takenEdges: ReadonlySet<string>;
  /** The run is over: steps it never reached are shown as not run. */
  ended: boolean;
  /** Steps that failed, in canvas order. */
  failed: string[];
}

export interface BuilderRunInput {
  nodes: ReadonlyArray<{ id: string; type?: string }>;
  edges: ReadonlyArray<{ id: string; source: string; target: string }>;
  /** Status per node id (useExtendedExecutionStatus's statusMap). */
  statusMap: Record<string, NodeExecutionStatus>;
  loopInfo: Record<string, LoopExecutionInfo>;
  /** The stream event that decided each node's status, by node id. */
  decidingEvents: Record<string, NodeExecutionUpdate>;
  /** The run's newest node event sequence. */
  latestSequence: number | undefined;
  live: boolean;
  /** The run says it needs a person (an approval or a question). */
  awaitingInput: boolean;
  /** Node types that start the run (the trigger rail, the start node). */
  isEntryType: (type: string | undefined) => boolean;
}

/** The activities an approval or ask-user node runs to raise its request. */
const RAISES_A_REQUEST = new Set(["ApprovalCreate", "QuestionCreate"]);

export function isSkippedEvent(update: NodeExecutionUpdate | undefined): boolean {
  if (!update) return false;
  const status: unknown = update.status;
  if (typeof status === "number") return status === ProtoNodeExecutionStatus.SKIPPED;
  return typeof status === "string" && status.toLowerCase().includes("skipped");
}

function nodeState(input: BuilderRunInput, nodeId: string): RunNodeState | undefined {
  const status = input.statusMap[nodeId];
  if (!status || status === "pending") return undefined;
  const event = input.decidingEvents[nodeId];
  const loop = input.loopInfo[nodeId];
  const withLoop = (state: RunNodeState): RunNodeState => (loop ? { ...state, loop } : state);

  if (status === "failed") return withLoop({ status, error: event?.error_message || undefined });
  if (status === "completed" && isSkippedEvent(event)) return withLoop({ status: "skipped" });
  if (
    status === "completed" &&
    input.live &&
    event &&
    RAISES_A_REQUEST.has(event.node_type) &&
    (input.awaitingInput || event.sequence_number === input.latestSequence)
  ) {
    return withLoop({ status: "waiting" });
  }
  return withLoop({ status });
}

export function deriveBuilderRun(input: BuilderRunInput): BuilderRunView {
  const nodes: Record<string, RunNodeState> = {};
  const reached = new Set<string>();
  const failed: string[] = [];
  for (const node of input.nodes) {
    if (input.isEntryType(node.type)) {
      reached.add(node.id);
      continue;
    }
    const state = nodeState(input, node.id);
    if (!state) continue;
    nodes[node.id] = state;
    reached.add(node.id);
    if (state.status === "failed") failed.push(node.id);
  }

  // A node that is not a step (a switch drawing conditional edges) has no
  // state of its own: the run passed through it when it came from a reached
  // node and went on to one.
  const stepIds = new Set(Object.keys(nodes));
  for (const node of input.nodes) {
    if (reached.has(node.id) || stepIds.has(node.id)) continue;
    const from = input.edges.some((edge) => edge.target === node.id && reached.has(edge.source));
    const to = input.edges.some((edge) => edge.source === node.id && reached.has(edge.target));
    if (from && to) reached.add(node.id);
  }

  const takenEdges = new Set<string>();
  for (const edge of input.edges) {
    if (reached.has(edge.source) && reached.has(edge.target)) takenEdges.add(edge.id);
  }
  return { nodes, takenEdges, ended: !input.live, failed };
}

/** How a step's run state reads in its accessible name and badge. */
export const RUN_STATUS_LABEL: Record<RunNodeStatus, string> = {
  running: "running",
  completed: "done",
  failed: "failed",
  skipped: "skipped",
  waiting: "waiting for you",
};

/**
 * The config-panel field a run error is about, when it names one. A step whose
 * {{ }} expression could not be evaluated fails with the field's path:
 * "CEL evaluation failed for step post: action.with[channel]: evaluating …".
 * The first segment is the step's kind; the rest is keyed like a validation
 * finding's field ("with.channel" → "channel"), so the same field takes focus.
 */
export function runErrorFieldKey(message: string | undefined): string | undefined {
  if (!message) return undefined;
  const match = /CEL evaluation failed for step [^:]+: ([^\s:]+): evaluating/.exec(message);
  if (!match) return undefined;
  const segments = match[1].replace(/\[([^\]]+)\]/g, ".$1").split(".").filter(Boolean);
  const field = segments.slice(1);
  if (field[0] === "with" || field[0] === "args") field.shift();
  return field.length === 1 && !/^\d+$/.test(field[0]) ? field[0] : undefined;
}
