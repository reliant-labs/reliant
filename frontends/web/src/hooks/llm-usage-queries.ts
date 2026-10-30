// Copyright (c) 2026 Reliant Labs

/**
 * The caller's own LLM usage for the usage dashboards, from reliant
 * `UsageService.GetLLMUsage` (caller-scoped, over reliant#1869's ledger).
 *
 * One read answers one grouping, so `useOwnLLMUsage` makes seven over the same
 * days: by day, by model, by purpose, and by day for each access mode (the
 * stacked tokens chart). Together they are one answer: loading until all have
 * landed, an error if any failed, and "not-deployed" when the server does not
 * serve the RPC at all, so the dashboard says so instead of guessing.
 * `refresh` refetches every read in any state, ready included.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { LLMUsageGroupBy, type GetLLMUsageResponse } from "@/gen/reliant/v1/usage_pb";
import {
  AUTH_MODES,
  llmUsageDates,
  toBucket,
  toLLMUsage,
  WIRE_AUTH_MODE,
  type LLMAuthMode,
  type LLMBucket,
  type OwnLLMUsageRead,
} from "@/components/Forge/Observability/Dashboards/llmUsageModel";

import { useGetLLMUsage } from "./usage-service-hooks_gen";

export interface OwnLLMUsageRequest {
  /** Inclusive UTC day start, epoch ms. */
  startMs: number;
  /** Exclusive UTC day start, epoch ms. */
  endMs: number;
}

const OPTIONS = { retry: false, staleTime: 60_000 } as const;

const unimplemented = (error: unknown) => error instanceof ConnectError && error.code === Code.Unimplemented;

export function useOwnLLMUsage(req: OwnLLMUsageRequest): OwnLLMUsageRead {
  const dates = llmUsageDates(req.startMs, req.endMs);
  const byDay = useGetLLMUsage({ ...dates, groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY }, OPTIONS);
  const byModel = useGetLLMUsage({ ...dates, groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_MODEL }, OPTIONS);
  const byPurpose = useGetLLMUsage({ ...dates, groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE }, OPTIONS);
  const day = LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY;
  const managed = useGetLLMUsage({ ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.managed }, OPTIONS);
  const byok = useGetLLMUsage({ ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.byok }, OPTIONS);
  const subscription = useGetLLMUsage({ ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.subscription }, OPTIONS);
  const local = useGetLLMUsage({ ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.local }, OPTIONS);
  const byMode = { managed, byok, subscription, local };
  const all = [byDay, byModel, byPurpose, managed, byok, subscription, local];
  const control = { refresh: () => all.forEach((q) => void q.refetch()), fetching: all.some((q) => q.isFetching) };

  if (all.some((q) => unimplemented(q.error))) return { status: "not-deployed", ...control };
  if (all.some((q) => q.isError)) return { status: "error", ...control };
  const modes = AUTH_MODES.map((m) => [m, byMode[m].data] as const);
  if (!byDay.data || !byModel.data || !byPurpose.data || modes.some(([, data]) => !data)) return { status: "loading", ...control };
  return {
    status: "ready",
    usage: toLLMUsage(req, byDay.data, byModel.data, byPurpose.data, Object.fromEntries(modes) as Record<LLMAuthMode, GetLLMUsageResponse>),
    ...control,
  };
}

/** The caller's Reliant AI (managed) calls by purpose: one read, for the spend dashboard's breakdown of where managed tokens went. */
export type OwnPurposeUsage =
  | { status: "not-deployed" | "loading" | "error" }
  | { status: "ready"; buckets: LLMBucket[]; note: string; completeness: "complete" | "partial" | "unknown" };

export function useOwnManagedPurposeUsage(req: OwnLLMUsageRequest): OwnPurposeUsage & { refresh: () => void; fetching: boolean } {
  const q = useGetLLMUsage({ ...llmUsageDates(req.startMs, req.endMs), groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE, authMode: WIRE_AUTH_MODE.managed }, OPTIONS);
  const control = { refresh: () => void q.refetch(), fetching: q.isFetching };
  if (unimplemented(q.error)) return { status: "not-deployed", ...control };
  if (q.isError) return { status: "error", ...control };
  if (!q.data) return { status: "loading", ...control };
  return {
    status: "ready",
    buckets: q.data.buckets.map(toBucket),
    note: q.data.note,
    completeness: q.data.completeness === 1 ? "complete" : q.data.completeness === 2 ? "partial" : "unknown",
    ...control,
  };
}
