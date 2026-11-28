// Copyright (c) 2026 Reliant Labs

/**
 * The GenAI lens's model: a trace's agent, model and tool steps, read from
 * the OpenTelemetry GenAI span attributes (forge `pkg/observe/genai`; spans
 * from OpenInference, OpenLLMetry and the older gen_ai names are normalised
 * onto the same keys at ingest, forge `pkg/observe/genai/conventions`).
 *
 * The lens shows only what the spans carry: names, models, tokens, timings,
 * cost and errors. It never reads message content, even where a span holds
 * some: content capture is its own opt-in slice with its own storage and
 * grant (forge docs/proposals/llm-agent-observability.md §4), so
 * {@link STEP_KEYS} is an allowlist of metadata keys.
 *
 * Steps nest as the spans do (workflow → node → agent → model call / tool
 * call). A span between two AI spans that is not one (an HTTP client call
 * under a model call) is skipped, its AI children lifted to the nearest AI
 * ancestor, so the lens reads as a transcript, not a waterfall.
 */

import type { Span } from "@/services/controlPlane/observability/types";

import { buildTraceTree, type SpanNode } from "../waterfall/traceTree";

/** The GenAI attribute keys the lens reads. Metadata only, never content. */
export const STEP_KEYS = {
  operation: "gen_ai.operation.name",
  provider: "gen_ai.provider.name",
  system: "gen_ai.system",
  requestModel: "gen_ai.request.model",
  responseModel: "gen_ai.response.model",
  inputTokens: "gen_ai.usage.input_tokens",
  outputTokens: "gen_ai.usage.output_tokens",
  cacheReadTokens: "gen_ai.usage.cache_read.input_tokens",
  cacheReadTokensLegacy: "gen_ai.usage.cache_read_input_tokens",
  cacheWriteTokens: "gen_ai.usage.cache_creation.input_tokens",
  reasoningTokens: "gen_ai.usage.reasoning.output_tokens",
  finishReasons: "gen_ai.response.finish_reasons",
  agentName: "gen_ai.agent.name",
  conversationId: "gen_ai.conversation.id",
  toolName: "gen_ai.tool.name",
  retries: "gen_ai.retries",
  errorType: "error.type",
  costUsd: "forge.ai.cost.usd",
  costUsdReliant: "reliant.cost_usd",
  workflowName: "forge.ai.workflow.name",
  runId: "forge.ai.run.id",
  nodeId: "forge.ai.node.id",
  convention: "forge.ai.convention",
  userId: "user.id",
} as const;

/** The span event a streamed call records at its first token. */
export const FIRST_TOKEN_EVENT = "gen_ai.first_token";

export type StepKind =
  "workflow" | "node" | "agent" | "model" | "tool" | "other";

/** The model-call operations (gen_ai.operation.name). */
const MODEL_OPS = new Set([
  "chat",
  "text_completion",
  "generate_content",
  "embeddings",
]);

export interface Tokens {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  reasoning: number;
}

/**
 * A step's cost in US dollars. `reported` is what the provider or gateway
 * stated on the span; `estimated` is a price-table estimate a later reader
 * supplies (the lens never prices a call itself); a rolled-up figure over
 * both is `mixed`.
 */
export interface Cost {
  usd: number;
  source: "reported" | "estimated" | "mixed";
}

export interface Step {
  id: string;
  span: Span;
  kind: StepKind;
  /** What the step is: the agent, model or tool name, else the span name. */
  label: string;
  /** The model that served the call, else the one asked for. */
  model?: string;
  provider?: string;
  durationMs: number;
  /** Start to the first token of a streamed model call. */
  ttftMs?: number;
  /** This step's own tokens (model calls). */
  tokens?: Tokens;
  /** This step's own reported cost. */
  cost?: Cost;
  failed: boolean;
  /** error.type, when the span names one. */
  errorType?: string;
  retries?: number;
  finishReasons?: string;
  children: Step[];
  /** This step and everything under it. */
  totals: StepTotals;
}

export interface StepTotals {
  modelCalls: number;
  toolCalls: number;
  failed: number;
  tokens: Tokens;
  /** Undefined when no step under it reports a cost: no figure, never $0. */
  cost?: Cost;
}

export interface GenAITrace {
  roots: Step[];
  totals: StepTotals;
  /** gen_ai.conversation.id: the session the trace belongs to. */
  conversationId?: string;
  userId?: string;
  /** forge.ai.convention values seen: where the canonical keys came from. */
  conventions: string[];
}

/** Whether any span of the trace is a GenAI span: the lens's switch. */
export function hasGenAI(spans: Span[]): boolean {
  return spans.some((span) => !!span.attributes[STEP_KEYS.operation]);
}

function num(value: string | undefined): number {
  const n = value === undefined || value === "" ? NaN : Number(value);
  return Number.isFinite(n) && n >= 0 ? n : 0;
}

function kindOf(span: Span): StepKind | null {
  const op = span.attributes[STEP_KEYS.operation];
  if (op) {
    if (MODEL_OPS.has(op)) return "model";
    if (op === "execute_tool") return "tool";
    if (op === "invoke_agent" || op === "create_agent") return "agent";
    if (op === "invoke_workflow") return "workflow";
    return "other";
  }
  // A workflow node carries forge.ai.* and no operation.
  if (
    span.attributes[STEP_KEYS.nodeId] ||
    span.name.startsWith("workflow.node")
  )
    return "node";
  return null;
}

function zero(): Tokens {
  return { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, reasoning: 0 };
}

function addTokens(a: Tokens, b: Tokens): Tokens {
  return {
    input: a.input + b.input,
    output: a.output + b.output,
    cacheRead: a.cacheRead + b.cacheRead,
    cacheWrite: a.cacheWrite + b.cacheWrite,
    reasoning: a.reasoning + b.reasoning,
  };
}

function addCost(a: Cost | undefined, b: Cost | undefined): Cost | undefined {
  if (!a) return b;
  if (!b) return a;
  return {
    usd: a.usd + b.usd,
    source: a.source === b.source ? a.source : "mixed",
  };
}

function stepOf(
  span: Span,
  kind: StepKind,
  id: string,
  children: Step[],
): Step {
  const a = span.attributes;
  const model =
    a[STEP_KEYS.responseModel] || a[STEP_KEYS.requestModel] || undefined;
  const label =
    (kind === "agent" && a[STEP_KEYS.agentName]) ||
    (kind === "tool" && a[STEP_KEYS.toolName]) ||
    (kind === "model" && model) ||
    (kind === "workflow" && a[STEP_KEYS.workflowName]) ||
    (kind === "node" && a[STEP_KEYS.nodeId]) ||
    span.name;
  const errorType = a[STEP_KEYS.errorType] || undefined;
  // A cancellation is not a failure, whatever the status says.
  const failed = span.status === "error" && errorType !== "canceled";
  const step: Step = {
    id,
    span,
    kind,
    label,
    model: kind === "model" ? model : undefined,
    provider:
      kind === "model"
        ? a[STEP_KEYS.provider] || a[STEP_KEYS.system] || undefined
        : undefined,
    durationMs: span.durationMs,
    failed,
    errorType,
    children,
    totals: { modelCalls: 0, toolCalls: 0, failed: 0, tokens: zero() },
  };
  if (kind === "model") {
    step.tokens = {
      input: num(a[STEP_KEYS.inputTokens]),
      output: num(a[STEP_KEYS.outputTokens]),
      cacheRead: num(
        a[STEP_KEYS.cacheReadTokens] ?? a[STEP_KEYS.cacheReadTokensLegacy],
      ),
      cacheWrite: num(a[STEP_KEYS.cacheWriteTokens]),
      reasoning: num(a[STEP_KEYS.reasoningTokens]),
    };
    const first = span.events.find((e) => e.name === FIRST_TOKEN_EVENT);
    if (first) step.ttftMs = Math.max(0, first.timeMs - span.startMs);
    const retries = num(a[STEP_KEYS.retries]);
    if (retries > 0) step.retries = retries;
    if (a[STEP_KEYS.finishReasons])
      step.finishReasons = a[STEP_KEYS.finishReasons];
  }
  const reported = a[STEP_KEYS.costUsd] ?? a[STEP_KEYS.costUsdReliant];
  if (
    reported !== undefined &&
    reported !== "" &&
    Number.isFinite(Number(reported))
  ) {
    step.cost = { usd: Number(reported), source: "reported" };
  }
  step.totals = totalsOf(step);
  return step;
}

function totalsOf(step: Step): StepTotals {
  let t: StepTotals = {
    modelCalls: step.kind === "model" ? 1 : 0,
    toolCalls: step.kind === "tool" ? 1 : 0,
    failed: step.failed ? 1 : 0,
    tokens: step.tokens ?? zero(),
    cost: step.cost,
  };
  for (const c of step.children) t = sumTotals(t, c.totals);
  return t;
}

function sumTotals(a: StepTotals, b: StepTotals): StepTotals {
  return {
    modelCalls: a.modelCalls + b.modelCalls,
    toolCalls: a.toolCalls + b.toolCalls,
    failed: a.failed + b.failed,
    tokens: addTokens(a.tokens, b.tokens),
    cost: addCost(a.cost, b.cost),
  };
}

/** The steps under node, lifting AI steps out of the non-AI spans between them. */
function stepsUnder(node: SpanNode): Step[] {
  const out: Step[] = [];
  for (const child of node.children) {
    const kind = kindOf(child.span);
    if (kind === null) {
      out.push(...stepsUnder(child));
    } else {
      out.push(stepOf(child.span, kind, child.id, stepsUnder(child)));
    }
  }
  return out;
}

/** A trace's GenAI steps, and their totals. */
export function buildGenAITrace(spans: Span[]): GenAITrace {
  const tree = buildTraceTree(spans);
  const roots: Step[] = [];
  for (const root of tree.roots) {
    const kind = kindOf(root.span);
    if (kind === null) roots.push(...stepsUnder(root));
    else roots.push(stepOf(root.span, kind, root.id, stepsUnder(root)));
  }
  let totals: StepTotals = {
    modelCalls: 0,
    toolCalls: 0,
    failed: 0,
    tokens: zero(),
  };
  for (const r of roots) totals = sumTotals(totals, r.totals);
  const first = (key: string) =>
    spans.find((s) => s.attributes[key])?.attributes[key];
  const conventions = [
    ...new Set(
      spans
        .map((s) => s.attributes[STEP_KEYS.convention])
        .filter((c): c is string => !!c),
    ),
  ].sort();
  return {
    roots,
    totals,
    conversationId: first(STEP_KEYS.conversationId),
    userId: first(STEP_KEYS.userId),
    conventions,
  };
}

/** A cost for a person: four decimals under a dollar, two above. */
export function formatUsd(usd: number): string {
  if (usd === 0) return "$0";
  if (Math.abs(usd) < 0.0001) return "<$0.0001";
  return `$${usd.toFixed(Math.abs(usd) < 1 ? 4 : 2)}`;
}
