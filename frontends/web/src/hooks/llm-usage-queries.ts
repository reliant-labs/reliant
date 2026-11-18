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

import type { MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";

import { LLMUsageGroupBy, type GetLLMUsageRequestSchema, type GetLLMUsageResponse } from "@/gen/reliant/v1/usage_pb";
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
import { orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure, type OrgScope } from "@/lib/orgScope";
import { usageServiceQueries } from "./usage-service-hooks_gen";

export interface OwnLLMUsageRequest {
  /** Inclusive UTC day start, epoch ms. */
  startMs: number;
  /** Exclusive UTC day start, epoch ms. */
  endMs: number;
}

const OPTIONS = { retry: false, staleTime: 60_000 } as const;

type UsageRequest = MessageInitShape<typeof GetLLMUsageRequestSchema>;

/**
 * One GetLLMUsage read in the tab's org. The interceptor would name the active
 * org on the wire anyway (unset means every org), so the request names it here,
 * where the key sees it: one org's usage is never served as another's, and
 * nothing is read before the org is known (lib/orgScope).
 */
function useUsage(org: OrgScope, req: UsageRequest, enabled = true) {
  return useQuery(
    withOrgFailure(usageServiceQueries.getLLMUsage(orgRequest(req, org.orgId), { ...OPTIONS, enabled: enabled && orgQueryEnabled(org) }), org),
  );
}

const unimplemented = (error: unknown) => error instanceof ConnectError && error.code === Code.Unimplemented;

export function useOwnLLMUsage(req: OwnLLMUsageRequest): OwnLLMUsageRead {
  const org = useOrgScope();
  const dates = llmUsageDates(req.startMs, req.endMs);
  const byDay = useUsage(org, { ...dates, groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY });
  const byModel = useUsage(org, { ...dates, groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_MODEL });
  const byPurpose = useUsage(org, { ...dates, groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE });
  const day = LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY;
  const managed = useUsage(org, { ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.managed });
  const byok = useUsage(org, { ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.byok });
  const subscription = useUsage(org, { ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.subscription });
  const local = useUsage(org, { ...dates, groupBy: day, authMode: WIRE_AUTH_MODE.local });
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

export function useOwnManagedPurposeUsage(
  req: OwnLLMUsageRequest,
  { enabled = true }: { enabled?: boolean } = {},
): OwnPurposeUsage & { refresh: () => void; fetching: boolean } {
  const org = useOrgScope();
  const q = useUsage(org, { ...llmUsageDates(req.startMs, req.endMs), groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE, authMode: WIRE_AUTH_MODE.managed }, enabled);
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
