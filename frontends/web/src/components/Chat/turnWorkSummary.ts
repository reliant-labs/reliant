/**
 * The words on a simplified-thread fold row: "Working… · Reading
 * ChatInput.tsx" while a turn runs, "Worked for 1m 12s · read 6 files · ran 3
 * commands" once it is done.
 *
 * Pure. Spawn status needs the child workflow tree (a hook), so the component
 * resolves it and hands it in as `spawnStatus`; everything else reads the
 * executions themselves, with the same status semantics as
 * ToolExecutionGroup's summary.
 */

import { ToolCallStatus } from "../../gen/reliant/v1/chat_pb";
import { getToolCategory } from "../Settings/ToolCallSettings";
import { formatToolDuration, toolActionVerb, toolBaseName } from "../../lib/toolAction";
import { formatToolParams, isSpawnTool, type ToolInput } from "../../lib/toolFormatters";

/** What a fold reads of one tool call (a structural subset of ToolCallData). */
export interface TurnWorkCall {
  id: string;
  name: string;
  input?: Record<string, unknown> | string;
  durableStatus?: ToolCallStatus;
  /** When the call finished, from the tool_calls row; ends the turn's elapsed time. */
  durableCompletedAt?: string;
  childWorkflowId?: string;
}

export interface TurnWorkExecution {
  call: TurnWorkCall;
  result?: { is_error?: boolean };
  approval?: unknown;
  /** Live status from the tool-call state store (same vocabulary as ToolExecution). */
  status?: string;
}

export type TurnWorkCountKey =
  | "read"
  | "searched"
  | "web"
  | "ran"
  | "edited"
  | "planned"
  | "spawned"
  | "other";

export type TurnWorkCounts = Record<TurnWorkCountKey, number>;

export interface TurnWorkSummary {
  total: number;
  running: number;
  failed: number;
  /** Spawned agents whose child workflow is still going. */
  spawnsRunning: number;
  counts: TurnWorkCounts;
  /** Live label of the latest running call, e.g. "Reading ChatInput.tsx". */
  current?: string;
}

export interface SummarizeTurnWorkOptions {
  /**
   * A dispatched spawn's effective status, from its child workflow. Return
   * undefined for "unknown yet" (treated as running, as ToolExecutionGroup does).
   */
  spawnStatus?: (call: TurnWorkCall) => string | undefined;
  /** Max characters of a call's argument preview in `current`. Default 48. */
  maxLabelLength?: number;
}

const DEFAULT_LABEL_MAX = 48;
const WEB_TOOLS = new Set(["websearch", "fetch"]);

function truncate(text: string, max: number): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  return oneLine.length > max ? `${oneLine.slice(0, max - 1)}…` : oneLine;
}

function durableToStatus(status: ToolCallStatus | undefined): string | undefined {
  switch (status) {
    case ToolCallStatus.PENDING:
      return "pending";
    case ToolCallStatus.EXECUTING:
      return "executing";
    case ToolCallStatus.COMPLETED:
      return "completed";
    case ToolCallStatus.FAILED:
      return "failed";
    case ToolCallStatus.CANCELLED:
      return "cancelled";
    case ToolCallStatus.BACKGROUNDED:
      return "backgrounded";
    default:
      return undefined;
  }
}

function countKeyFor(toolName: string): TurnWorkCountKey {
  if (WEB_TOOLS.has(toolBaseName(toolName).toLowerCase())) return "web";
  switch (getToolCategory(toolName)) {
    case "fileView":
      return "read";
    case "searchRead":
      return "searched";
    case "execution":
      return "ran";
    case "fileWrite":
      return "edited";
    case "planning":
      return "planned";
    case "agent":
      return "spawned";
    default:
      return "other";
  }
}

function liveLabel(call: TurnWorkCall, max: number): string {
  const formatted =
    call.input === undefined ? undefined : formatToolParams(call.name, call.input as ToolInput);
  const preview = formatted?.summary?.trim();
  const verb = toolActionVerb(call.name, "live");
  // A shell call's description is already an action ("Run cart tests").
  if (verb && preview && typeof formatted?.structured?.description === "string") {
    return truncate(preview, max);
  }
  if (verb) return preview ? `${verb} ${truncate(preview, max)}` : verb;
  const name = toolBaseName(call.name);
  return preview ? `${name}(${truncate(preview, max)})` : name;
}

export function summarizeTurnWork(
  executions: ReadonlyArray<TurnWorkExecution>,
  opts: SummarizeTurnWorkOptions = {},
): TurnWorkSummary {
  const max = opts.maxLabelLength ?? DEFAULT_LABEL_MAX;
  const counts: TurnWorkCounts = {
    read: 0,
    searched: 0,
    web: 0,
    ran: 0,
    edited: 0,
    planned: 0,
    spawned: 0,
    other: 0,
  };
  let running = 0;
  let failed = 0;
  let spawnsRunning = 0;
  let latestRunning: TurnWorkCall | undefined;

  for (const { call, result, status } of executions) {
    counts[countKeyFor(call.name)] += 1;

    const spawn = isSpawnTool(call.name);
    const effective =
      spawn && call.childWorkflowId
        ? (opts.spawnStatus?.(call) ?? "executing")
        : (status ?? durableToStatus(call.durableStatus));

    if (effective === "failed" || (!spawn && result?.is_error)) {
      failed += 1;
    } else if (effective === "completed" || (result && !(spawn && call.childWorkflowId))) {
      // done
    } else if (effective === "cancelled" || effective === "denied") {
      // stopped, neither running nor failed
    } else {
      running += 1;
      latestRunning = call;
      if (spawn) spawnsRunning += 1;
    }
  }

  return {
    total: executions.length,
    running,
    failed,
    spawnsRunning,
    counts,
    current: latestRunning ? liveLabel(latestRunning, max) : undefined,
  };
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

const COUNT_PHRASES: ReadonlyArray<[TurnWorkCountKey, (n: number) => string]> = [
  ["read", (n) => `read ${plural(n, "file", "files")}`],
  ["searched", (n) => plural(n, "search", "searches")],
  ["web", (n) => plural(n, "web lookup", "web lookups")],
  ["ran", (n) => `ran ${plural(n, "command", "commands")}`],
  ["edited", (n) => `edited ${plural(n, "file", "files")}`],
  ["planned", (n) => plural(n, "plan update", "plan updates")],
  ["spawned", (n) => `spawned ${plural(n, "agent", "agents")}`],
  ["other", (n) => `used ${plural(n, "tool", "tools")}`],
];

function formatElapsed(ms: number): string {
  if (ms < 60_000) return `${Math.max(1, Math.round(ms / 1000))}s`;
  return formatToolDuration(ms);
}

export function formatTurnWorkHeadline(
  summary: TurnWorkSummary,
  opts: { live: boolean; elapsedMs?: number | null },
): string {
  if (opts.live) {
    const parts = ["Working…"];
    if (summary.current) parts.push(summary.current);
    if (summary.spawnsRunning > 0) {
      parts.push(`${plural(summary.spawnsRunning, "agent", "agents")} running`);
    }
    return parts.join(" · ");
  }

  const hasElapsed = typeof opts.elapsedMs === "number" && Number.isFinite(opts.elapsedMs);
  const parts = [hasElapsed ? `Worked for ${formatElapsed(opts.elapsedMs as number)}` : "Worked"];
  for (const [key, phrase] of COUNT_PHRASES) {
    const n = summary.counts[key];
    if (n > 0) parts.push(phrase(n));
  }
  return parts.join(" · ");
}
