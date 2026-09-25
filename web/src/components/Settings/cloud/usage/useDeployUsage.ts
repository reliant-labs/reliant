// The ONE seam between the deploy usage surface and the control plane.
//
// Four reads and one write, all on the PUBLIC control-plane listener
// (`publicAPIMounts()`), through the shared control-plane transport:
//
//   BillingService.GetCurrentUserInfraOverage   consumption, allowance, cap, rung
//   BillingService.SetCurrentUserInfraOverage   the ceiling write
//   DeployService.ListUsage                     attribution rows
//   DeployService.ListDeployments / ListEnvironments   id → name for those rows
//
// THE ADAPTER IS ASSIGNMENT, NOT TRANSLATION. ./usageModel declares the wire
// shapes it reads as local interfaces that the generated messages satisfy
// structurally, and ListUsage reports quantities in exactly the units
// buildAttribution divides (milli-vCPU-hours, MiB-hours, GiB-hours; see
// control-plane's rpc_list_usage.go). Nothing here converts a unit or re-derives
// a cost.
//
// THE ATTRIBUTION WINDOW IS THE BILLING PERIOD, CLAMPED TO THE LAST 7 DAYS.
// Raw meter ticks are retained for 7 days, and ListUsage refuses a window over
// 31. So when a period is longer than what is retained, the attribution rows
// and the `accruedCents` summed from them cover its most recent week, not
// the whole period. The allowance meters and the overage figure come from the
// overage RPC, which reads the whole period. KNOWN GAP: the "Accrued this
// period" card therefore understates a period older than a week until
// control-plane serves ListUsage from resource_usage_rollups (90-day retention),
// which nothing populates today.
//
// `available` is a BUILD fact (the generated client carries every method
// used below), so it is true. An org that has never deployed simply gets an
// empty summary. A failed read is `error`, and the section renders its own
// error state rather than a confident zero.

import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";

import { buildUsageSummary, timestampToDate } from "./usageModel";

import type {
  InfraOverageResponse,
  OverageRequestFields,
  UsageSummary,
} from "./usageModel";

export interface DeployUsageState {
  /**
   * Whether the RPCs backing this surface can be called at all in this build.
   *
   * A BUILD fact, not a server read — it reflects whether the generated client
   * carries the methods, so it cannot flap and callers may branch on it
   * structurally.
   */
  available: boolean;
  summary: UsageSummary;
  isLoading: boolean;
  error: { message?: string } | null;
  refetch: () => void;
  saveCap: (fields: OverageRequestFields) => void;
  isSavingCap: boolean;
  deploymentNames: Map<string, string>;
  environmentNames: Map<string, string>;
}

/** Raw meter ticks are retained this long (control-plane migration 00071). */
export const USAGE_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;

export const deployUsageKeys = {
  all: ["deploy-usage"] as const,
  overage: ["deploy-usage", "infra-overage"] as const,
  rows: (startMs: number, endMs: number) =>
    ["deploy-usage", "rows", startMs, endMs] as const,
  names: ["deploy-usage", "names"] as const,
};

/**
 * The window ListUsage is asked for: the billing period, clamped to what the
 * meter retains. Exported so the clamp is assertable without a render.
 *
 * With no period (an org with no plan) it is the retention window ending now.
 */
export function attributionWindow(
  periodStart: Date | undefined,
  periodEnd: Date | undefined,
  now: Date,
): { start: Date; end: Date } {
  const end = periodEnd && periodEnd < now ? periodEnd : now;
  const earliest = new Date(end.getTime() - USAGE_RETENTION_MS);
  const start = periodStart && periodStart > earliest ? periodStart : earliest;
  return { start, end };
}

/** Floors a time to the minute, so the query key does not change every render. */
function floorToMinute(d: Date): Date {
  return new Date(Math.floor(d.getTime() / 60_000) * 60_000);
}

export function useDeployUsage(): DeployUsageState {
  const queryClient = useQueryClient();

  const overage = useQuery({
    queryKey: deployUsageKeys.overage,
    queryFn: () =>
      getControlPlaneClient(BillingService).getCurrentUserInfraOverage({}),
    staleTime: 60_000,
  });

  const window = useMemo(
    () =>
      attributionWindow(
        timestampToDate(overage.data?.periodStart),
        timestampToDate(overage.data?.periodEnd),
        floorToMinute(new Date()),
      ),
    [overage.data?.periodStart, overage.data?.periodEnd],
  );

  const rows = useQuery({
    queryKey: deployUsageKeys.rows(window.start.getTime(), window.end.getTime()),
    queryFn: () =>
      getControlPlaneClient(DeployService).listUsage({
        startTime: timestampFromDate(window.start),
        endTime: timestampFromDate(window.end),
      }),
    // Wait for the period: asking before it is known would fetch the fallback
    // window and then immediately refetch the real one.
    enabled: overage.isSuccess || overage.isError,
    staleTime: 60_000,
  });

  const names = useQuery({
    queryKey: deployUsageKeys.names,
    queryFn: async () => {
      const client = getControlPlaneClient(DeployService);
      const [deployments, environments] = await Promise.all([
        client.listDeployments({}),
        client.listEnvironments({}),
      ]);
      return {
        deployments: new Map(deployments.deployments.map((d) => [d.id, d.name])),
        environments: new Map(environments.environments.map((e) => [e.id, e.name])),
      };
    },
    staleTime: 5 * 60_000,
  });

  const saveCapMutation = useMutation({
    mutationFn: (fields: OverageRequestFields) =>
      getControlPlaneClient(BillingService).setCurrentUserInfraOverage({
        budgetCents: fields.budgetCents,
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: deployUsageKeys.overage }),
  });

  const summary = useMemo(
    () =>
      buildUsageSummary({
        // Structurally compatible by design (see ./usageModel's header).
        overage: overage.data as InfraOverageResponse | undefined,
        rows: rows.data?.rows ?? [],
      }),
    [overage.data, rows.data],
  );

  const refetch = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: deployUsageKeys.all });
  }, [queryClient]);

  // Names are decoration: a failure to resolve them leaves ids in the table,
  // which is correct if ugly, so it is not surfaced as the section's error.
  const error = (overage.error ?? rows.error ?? null) as { message?: string } | null;

  return {
    available: true,
    summary,
    isLoading: overage.isLoading || rows.isLoading,
    error,
    refetch,
    saveCap: (fields) => saveCapMutation.mutate(fields),
    isSavingCap: saveCapMutation.isPending,
    deploymentNames: names.data?.deployments ?? new Map(),
    environmentNames: names.data?.environments ?? new Map(),
  };
}
