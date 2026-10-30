// Copyright (c) 2026 Reliant Labs

/**
 * The caller's own LLM usage, as the "LLM usage" dashboard reads it from
 * reliant `UsageService.GetLLMUsage` (caller-scoped; the count-only ledger of
 * reliant#1869). Counts only: never prompts, replies or tool I/O.
 *
 * One request answers one grouping (day, model, provider or auth mode), so
 * the dashboard reads several: by day, by model, and by day once per access
 * mode (the stacked tokens chart). A group with no usage is absent from a
 * response; inside the requested days that is a real zero calls, so the day
 * charts draw 0 and the model tables simply do not list it.
 *
 * Scope is the signed-in user (v1): organization admins do not see members'
 * own-key or subscription usage.
 */

import type { GetLLMUsageResponse, LLMUsageBucket } from "@/gen/reliant/v1/usage_pb";
import { LLMAuthMode as WireAuthMode, LLMUsageCompleteness } from "@/gen/reliant/v1/usage_pb";

import { DAY_MS, type UsageSeries } from "./organizationUsageModel";

export type LLMAuthMode = "managed" | "byok" | "subscription" | "local";

export const AUTH_MODES: LLMAuthMode[] = ["managed", "byok", "subscription", "local"];

export const AUTH_MODE_LABELS: Record<LLMAuthMode, string> = {
  managed: "Reliant AI",
  byok: "Your API keys",
  subscription: "Subscriptions",
  local: "Local models",
};

export type LLMPurpose = "chat" | "compaction" | "title" | "draft_automation";

/** Bucket keys of group_by PURPOSE, in the order the dashboards list them. */
export const PURPOSES: LLMPurpose[] = ["chat", "compaction", "title", "draft_automation"];

export const PURPOSE_LABELS: Record<LLMPurpose, string> = {
  chat: "Chat",
  compaction: "Context compaction",
  title: "Chat titles",
  draft_automation: "Automation drafts",
};

export function purposeLabel(key: string): string {
  return PURPOSE_LABELS[key as LLMPurpose] ?? (key || "Unspecified");
}

export const WIRE_AUTH_MODE: Record<LLMAuthMode, WireAuthMode> = {
  managed: WireAuthMode.LLM_AUTH_MODE_MANAGED,
  byok: WireAuthMode.LLM_AUTH_MODE_BYOK,
  subscription: WireAuthMode.LLM_AUTH_MODE_SUBSCRIPTION,
  local: WireAuthMode.LLM_AUTH_MODE_LOCAL,
};

/** One bucket's sums, as plain numbers (counts fit a double exactly). */
export interface LLMBucket {
  key: string;
  calls: number;
  errorCalls: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  latencyMsTotal: number;
  avgLatencyMs: number;
}

export interface LLMUsage {
  /** Inclusive, epoch ms (UTC day start). */
  startMs: number;
  /** Exclusive, epoch ms (UTC day start). */
  endMs: number;
  completeness: "complete" | "partial" | "unknown";
  /** The server's own account of completeness and retention, for display. */
  note: string;
  asOf: string;
  totals: LLMBucket;
  byDay: LLMBucket[];
  byModel: LLMBucket[];
  /** Calls by why they were made, most tokens first. */
  byPurpose: LLMBucket[];
  /** Per access mode, by day. A mode with no usage has no buckets. */
  byDayByMode: Record<LLMAuthMode, LLMBucket[]>;
}

/**
 * Where the caller's own usage stands. "not-deployed" is the honest state on
 * a server that does not serve the usage RPC: the panels say so and draw
 * nothing.
 */
export type OwnLLMUsage =
  | { status: "not-deployed" }
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; usage: LLMUsage };

/** The state plus what Refresh needs: a refetch that works in every state, and whether any read is in flight. */
export type OwnLLMUsageRead = OwnLLMUsage & { refresh: () => void; fetching: boolean };

/** "YYYY-MM-DD" of the UTC day holding `ms`. */
export function utcDate(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10);
}

/** The request dates for a window whose end is exclusive: GetLLMUsage's end_date is inclusive. */
export function llmUsageDates(startMs: number, endMs: number): { startDate: string; endDate: string } {
  return { startDate: utcDate(startMs), endDate: utcDate(endMs - DAY_MS) };
}

export function toBucket(b: LLMUsageBucket | undefined): LLMBucket {
  return {
    key: b?.key ?? "",
    calls: Number(b?.calls ?? 0n),
    errorCalls: Number(b?.errorCalls ?? 0n),
    inputTokens: Number(b?.inputTokens ?? 0n),
    outputTokens: Number(b?.outputTokens ?? 0n),
    cacheReadTokens: Number(b?.cacheReadTokens ?? 0n),
    cacheWriteTokens: Number(b?.cacheWriteTokens ?? 0n),
    latencyMsTotal: Number(b?.latencyMsTotal ?? 0n),
    avgLatencyMs: b?.avgLatencyMs ?? 0,
  };
}

function completenessOf(value: LLMUsageCompleteness | undefined): LLMUsage["completeness"] {
  if (value === LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_COMPLETE) return "complete";
  if (value === LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_PARTIAL) return "partial";
  return "unknown";
}

/** The dashboard's view of the responses: the day and model reads, and one day read per access mode. */
export function toLLMUsage(
  span: { startMs: number; endMs: number },
  byDay: GetLLMUsageResponse,
  byModel: GetLLMUsageResponse,
  byPurpose: GetLLMUsageResponse,
  byMode: Record<LLMAuthMode, GetLLMUsageResponse>
): LLMUsage {
  // The least complete read decides.
  const levels = [byDay, byModel, byPurpose, ...AUTH_MODES.map((m) => byMode[m])].map((r) => completenessOf(r.completeness));
  const completeness = levels.includes("unknown") ? "unknown" : levels.includes("partial") ? "partial" : "complete";
  return {
    startMs: span.startMs,
    endMs: span.endMs,
    completeness,
    note: byDay.note,
    asOf: byDay.asOf,
    totals: toBucket(byDay.totals),
    byDay: byDay.buckets.map(toBucket),
    byModel: byModel.buckets.map(toBucket),
    byPurpose: byPurpose.buckets.map(toBucket),
    byDayByMode: Object.fromEntries(AUTH_MODES.map((m) => [m, byMode[m].buckets.map(toBucket)])) as Record<LLMAuthMode, LLMBucket[]>,
  };
}

export function dayStarts(usage: Pick<LLMUsage, "startMs" | "endMs">): number[] {
  const out: number[] = [];
  for (let at = usage.startMs; at < usage.endMs && out.length < 400; at += DAY_MS) out.push(at);
  return out;
}

/**
 * Every token the provider processed for a bucket. Drivers report input_tokens
 * as the uncached remainder, with cache reads and writes held separately, so
 * a call with 904 input, 4096 cache-read and 300 output tokens is 5300 tokens,
 * not 1204.
 */
export function totalTokens(b: Pick<LLMBucket, "inputTokens" | "outputTokens" | "cacheReadTokens" | "cacheWriteTokens">): number {
  return b.inputTokens + b.outputTokens + b.cacheReadTokens + b.cacheWriteTokens;
}

/** A day-keyed metric over every day of the range: a day with no bucket had no calls. */
function perDay(usage: LLMUsage, buckets: readonly LLMBucket[], pick: (b: LLMBucket) => number): number[] {
  const byKey = new Map(buckets.map((b) => [b.key, b]));
  return dayStarts(usage).map((day) => {
    const b = byKey.get(utcDate(day));
    return b ? pick(b) : 0;
  });
}

/** Total tokens per day, one series per access mode that has any. */
export function tokensByAuthMode(usage: LLMUsage): UsageSeries[] {
  return AUTH_MODES.flatMap((mode) => {
    const buckets = usage.byDayByMode[mode];
    if (buckets.length === 0) return [];
    return [{ key: mode, label: AUTH_MODE_LABELS[mode], values: perDay(usage, buckets, totalTokens) }];
  });
}

/** Calls and failed calls per day. */
export function callsAndErrors(usage: LLMUsage): UsageSeries[] {
  return [
    { key: "calls", label: "Calls", values: perDay(usage, usage.byDay, (b) => b.calls) },
    { key: "errors", label: "Errors", values: perDay(usage, usage.byDay, (b) => b.errorCalls) },
  ];
}

/** Totals per access mode over the range, from the per-mode day reads. Modes with no usage are left out. */
export function modeTotals(usage: LLMUsage): { mode: LLMAuthMode; calls: number; tokens: number }[] {
  return AUTH_MODES.flatMap((mode) => {
    const buckets = usage.byDayByMode[mode];
    if (buckets.length === 0) return [];
    return [{ mode, calls: buckets.reduce((t, b) => t + b.calls, 0), tokens: buckets.reduce((t, b) => t + totalTokens(b), 0) }];
  });
}

/**
 * The share of prompt tokens served from the provider's cache:
 * cache reads / (uncached input + cache reads + cache writes). Undefined with
 * no prompt tokens.
 */
export function cacheHitRatio(m: Pick<LLMBucket, "inputTokens" | "cacheReadTokens" | "cacheWriteTokens">): number | undefined {
  const prompt = m.inputTokens + m.cacheReadTokens + m.cacheWriteTokens;
  return prompt > 0 ? m.cacheReadTokens / prompt : undefined;
}

export function errorRate(m: Pick<LLMBucket, "calls" | "errorCalls">): number | undefined {
  return m.calls > 0 ? m.errorCalls / m.calls : undefined;
}
