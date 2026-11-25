// Copyright (c) 2026 Reliant Labs

/**
 * The per-chat Trace/Cost tab's pure half (dev-docs/design/agent-analytics.md
 * §2): ChatTraceService answers in wire types, and this turns them into what
 * the tab draws. The waterfall is the observability shell's
 * (`Forge/Observability/waterfall/TraceWaterfall.tsx`), so a chat span becomes
 * that shell's `Span`; the cost summary keeps reported and estimated dollars
 * apart and counts what neither covers; a PARTIAL answer names each reason in
 * words.
 *
 * Viewer shape (`viewer` on the response, or a chat the caller only views):
 * the server already sends no cost, no `reliant.*` attribute and no ids, and
 * this file drops the same keys again, so a field the server one day lets
 * through still never reaches a viewer's screen.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  ChatTraceCompleteness,
  ChatTracePartialReason,
  type ChatTraceSpan,
  type ChatTraceTotals,
  type GetChatTraceResponse,
} from "@/gen/reliant/v1/chat_trace_pb";
import type { Span, SpanStatus } from "@/services/controlPlane/observability/types";

/** Span attribute keys the tab reads. */
export const ATTR = {
  model: "gen_ai.request.model",
  responseModel: "gen_ai.response.model",
  tool: "gen_ai.tool.name",
  inputTokens: "gen_ai.usage.input_tokens",
  outputTokens: "gen_ai.usage.output_tokens",
  cost: "forge.ai.cost.usd",
} as const;

/** True for an attribute a viewer must never see, whatever the server sent. */
export function isOwnerOnlyAttribute(key: string): boolean {
  return key === ATTR.cost || key.startsWith("reliant.") || key.endsWith(".cost.usd") || key.includes("cost_usd");
}

function ms(ts: Timestamp | undefined): number {
  return ts ? timestampMs(ts) : Number.NaN;
}

/** OTel status as stored (`Unset`, `Ok`, `Error`, or the STATUS_CODE_* spelling) → the waterfall's. */
export function spanStatus(code: string): SpanStatus {
  const lower = code.toLowerCase();
  if (lower.includes("error")) return "error";
  if (lower.endsWith("ok")) return "ok";
  return "unset";
}

/**
 * A chat span as the waterfall's `Span`. For a viewer, every owner-only
 * attribute is dropped here too, and events keep only their name and time.
 */
export function toWaterfallSpan(span: ChatTraceSpan, viewer: boolean): Span {
  const attributes: Record<string, string> = {};
  for (const [key, value] of Object.entries(span.attributes)) {
    if (viewer && isOwnerOnlyAttribute(key)) continue;
    attributes[key] = value;
  }
  return {
    spanId: span.spanId,
    parentSpanId: span.parentSpanId,
    name: span.name,
    service: span.serviceName,
    kind: span.kind.toLowerCase(),
    startMs: ms(span.startTime),
    durationMs: Number(span.durationNs) / 1e6,
    status: spanStatus(span.statusCode),
    statusMessage: "",
    attributes,
    resource: {},
    events: span.events.map((event) => ({
      timeMs: ms(event.time),
      name: event.name,
      attributes: viewer ? {} : { ...event.attributes },
    })),
    links: viewer ? undefined : span.links.map((link) => ({ traceId: link.traceId, spanId: link.spanId })),
    source: "",
    operatorData: false,
  };
}

// ── Completeness ────────────────────────────────────────────────────────────

/** Each PARTIAL reason in plain words: what is missing, and why. */
export function partialReasonText(reason: ChatTracePartialReason): string {
  switch (reason) {
    case ChatTracePartialReason.TRACE_EXPIRED:
      return "Timing older than 7 days has expired. Model calls are still listed.";
    case ChatTracePartialReason.SPANS_UNAVAILABLE:
      return "Timing couldn't be loaded right now. Model calls are still listed.";
    case ChatTracePartialReason.SPANS_TRUNCATED:
      return "This chat has more steps than one view holds, so only the earliest are drawn.";
    case ChatTracePartialReason.SPANS_MISSING:
      return "Model calls were recorded, but no timing was kept for them.";
    case ChatTracePartialReason.CALLS_EXPIRED:
      return "Model calls older than 90 days have expired.";
    case ChatTracePartialReason.CALLS_TRUNCATED:
      return "This chat has more model calls than one view holds, so only the earliest are listed.";
    default:
      return "Part of this chat's trace is missing.";
  }
}

/** The PARTIAL reasons to show, in plain words, deduplicated. Empty when the answer is complete. */
export function partialReasons(data: Pick<GetChatTraceResponse, "completeness" | "partialReasons">): string[] {
  if (data.completeness !== ChatTraceCompleteness.PARTIAL && data.partialReasons.length === 0) return [];
  const reasons = data.partialReasons.length > 0 ? data.partialReasons : [ChatTracePartialReason.UNSPECIFIED];
  return [...new Set(reasons.map(partialReasonText))];
}

/** Whether the answer has nothing to draw at all. */
export function isEmptyTrace(data: Pick<GetChatTraceResponse, "spans" | "modelCalls">): boolean {
  return data.spans.length === 0 && data.modelCalls.length === 0;
}

// ── Money ───────────────────────────────────────────────────────────────────

const NANOS_PER_USD = 1_000_000_000;

/**
 * Billionths of a dollar as dollars. Sub-cent amounts keep enough digits to
 * be non-zero ("$0.0042"), and a positive amount below a hundredth of a cent
 * reads "<$0.0001" rather than "$0.00".
 */
export function formatUsdNanos(nanos: bigint): string {
  const usd = Number(nanos) / NANOS_PER_USD;
  if (usd === 0) return "$0.00";
  if (usd > 0 && usd < 0.0001) return "<$0.0001";
  if (usd < 1) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}

export interface CostSummary {
  calls: number;
  /** Priced by the gateway: what was charged. */
  reported: { usd: string; calls: number } | null;
  /** Priced at list price on the call's date: an estimate, never added to reported. */
  estimated: { usd: string; calls: number } | null;
  /** Calls priced per seat or run locally: tokens only. */
  unpricedCalls: number;
  /** Calls with no recorded cost (before cost was recorded, or another person's). */
  unknownCalls: number;
  /** unpriced + unknown, over calls, in [0, 1]. */
  unpricedShare: number;
}

/** The owner's cost summary. Reported and estimated stay separate; what neither covers is counted. */
export function costSummary(totals: ChatTraceTotals | undefined): CostSummary {
  const calls = Number(totals?.calls ?? 0n);
  const reportedCalls = Number(totals?.reportedCalls ?? 0n);
  const estimatedCalls = Number(totals?.estimatedCalls ?? 0n);
  const unpricedCalls = Number(totals?.unpricedCalls ?? 0n);
  const unknownCalls = Number(totals?.unknownCostCalls ?? 0n);
  return {
    calls,
    reported: reportedCalls > 0 ? { usd: formatUsdNanos(totals?.reportedCostUsdNanos ?? 0n), calls: reportedCalls } : null,
    estimated: estimatedCalls > 0 ? { usd: formatUsdNanos(totals?.estimatedCostUsdNanos ?? 0n), calls: estimatedCalls } : null,
    unpricedCalls,
    unknownCalls,
    unpricedShare: calls > 0 ? (unpricedCalls + unknownCalls) / calls : 0,
  };
}

/** "3 calls", "1 call". */
export function plural(count: number, noun: string): string {
  return `${count.toLocaleString()} ${noun}${count === 1 ? "" : "s"}`;
}

/** 1234 → "1,234"; 1_234_567 → "1.2M". Tokens are counted, not measured, so no "k" below 10k. */
export function formatTokenCount(value: bigint | number): string {
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  if (Math.abs(n) < 10_000) return n.toLocaleString();
  if (Math.abs(n) < 1_000_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
}

/** The model a span ran, from its attributes; empty for a span that is not a model call. */
export function spanModel(span: Span): string {
  return span.attributes[ATTR.responseModel] || span.attributes[ATTR.model] || "";
}
