// Copyright (c) 2026 Reliant Labs
//
// Typed GetLLMUsage responses (reliant#1875's shape) for the LLM usage view
// and its hook. Test data only.

import { create } from "@bufbuild/protobuf";

import { GetLLMUsageResponseSchema, LLMAuthMode, LLMUsageCompleteness, LLMUsageGroupBy, type GetLLMUsageRequest, type GetLLMUsageResponse } from "@/gen/reliant/v1/usage_pb";

import { toLLMUsage, type LLMAuthMode as Mode, type LLMUsage } from "../llmUsageModel";

/** The 30-day window the views open on at NOW_MS: 2026-09-26 to 2026-10-25 inclusive. */
export const LLM_START_MS = Date.parse("2026-09-26T00:00:00Z");
export const LLM_END_MS = Date.parse("2026-10-26T00:00:00Z");

const bucket = (key: string, calls: number, errorCalls: number, input: number, output: number, cacheRead: number, cacheWrite = 0) => ({
  key,
  calls: BigInt(calls),
  errorCalls: BigInt(errorCalls),
  inputTokens: BigInt(input),
  outputTokens: BigInt(output),
  cacheReadTokens: BigInt(cacheRead),
  cacheWriteTokens: BigInt(cacheWrite),
  latencyMsTotal: BigInt(calls * 1200),
  avgLatencyMs: calls ? 1200 : 0,
});

const response = (groupBy: LLMUsageGroupBy, buckets: ReturnType<typeof bucket>[], completeness = LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_COMPLETE) =>
  create(GetLLMUsageResponseSchema, {
    groupBy,
    buckets,
    totals: buckets.reduce(
      (t, b) => ({ ...t, calls: t.calls + b.calls, errorCalls: t.errorCalls + b.errorCalls, inputTokens: t.inputTokens + b.inputTokens, outputTokens: t.outputTokens + b.outputTokens, cacheReadTokens: t.cacheReadTokens + b.cacheReadTokens, cacheWriteTokens: t.cacheWriteTokens + b.cacheWriteTokens }),
      bucket("", 0, 0, 0, 0, 0)
    ),
    completeness,
    asOf: "2026-10-25T12:00:00Z",
    detailAvailableFrom: "2026-07-28",
    note: completeness === LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_PARTIAL ? "Days before 2026-07-28 come from the daily rollup." : "Every day in range has its individual calls.",
  });

/** Two days of the caller's own calls: a subscription, an API key and Reliant AI. */
export function llmResponses(completeness = LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_COMPLETE): { byDay: GetLLMUsageResponse; byModel: GetLLMUsageResponse; byPurpose: GetLLMUsageResponse; byMode: Record<Mode, GetLLMUsageResponse> } {
  return {
    byDay: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, [bucket("2026-10-20", 30, 3, 1000, 500, 3000, 400), bucket("2026-10-21", 22, 1, 2100, 670, 1000)], completeness),
    byModel: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_MODEL, [bucket("claude-sonnet-5", 40, 3, 2000, 1000, 4000, 400), bucket("claude-haiku-4-5", 7, 0, 700, 70, 0), bucket("gpt-6", 5, 1, 400, 100, 0)], completeness),
    byPurpose: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE, [bucket("chat", 45, 4, 4000, 1100, 5000, 600), bucket("compaction", 4, 0, 900, 120, 0), bucket("title", 3, 0, 60, 20, 0)], completeness),
    byMode: {
      managed: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, [bucket("2026-10-21", 7, 0, 700, 70, 0)], completeness),
      byok: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, [bucket("2026-10-21", 5, 1, 400, 100, 0)], completeness),
      subscription: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, [bucket("2026-10-20", 30, 3, 1000, 500, 3000, 400), bucket("2026-10-21", 10, 0, 1000, 500, 1000)], completeness),
      local: response(LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, [], completeness),
    },
  };
}

export function ownUsage(completeness = LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_COMPLETE): LLMUsage {
  const r = llmResponses(completeness);
  return toLLMUsage({ startMs: LLM_START_MS, endMs: LLM_END_MS }, r.byDay, r.byModel, r.byPurpose, r.byMode);
}

/** A mock GetLLMUsage server answering each request with the fixture of its shape. */
export function answer(req: GetLLMUsageRequest): GetLLMUsageResponse {
  const r = llmResponses();
  if (req.groupBy === LLMUsageGroupBy.LLM_USAGE_GROUP_BY_MODEL) return r.byModel;
  if (req.groupBy === LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE) return req.authMode === undefined ? r.byPurpose : r.byPurpose;
  const modes: Record<number, Mode> = { [LLMAuthMode.LLM_AUTH_MODE_MANAGED]: "managed", [LLMAuthMode.LLM_AUTH_MODE_BYOK]: "byok", [LLMAuthMode.LLM_AUTH_MODE_SUBSCRIPTION]: "subscription", [LLMAuthMode.LLM_AUTH_MODE_LOCAL]: "local" };
  return req.authMode === undefined ? r.byDay : r.byMode[modes[req.authMode]!];
}
