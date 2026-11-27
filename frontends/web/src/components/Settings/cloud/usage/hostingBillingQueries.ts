// Copyright (c) 2026 Reliant Labs

/**
 * The org-scoped app hosting reads and writes that sit beside the agreement:
 * editable budget versions, the org's usage summary with its month-end
 * projection, and the free static tier.
 *
 * Reads use the vendored BillingService hooks and share their transport and
 * cache keys. Older servers can still return Code.Unimplemented; the surfaces
 * show that as unavailable, never zero usage or an empty history.
 *
 * The shapes below are what the hosting surfaces read. The generated messages
 * satisfy them structurally (int64 → bigint, Timestamp → wkt Timestamp), so
 * no translation layer is needed. The write workflows keep their own mutation
 * keys and invalidation rules.
 */

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import {
  billingServiceKeys,
  billingServiceMutations,
  useGetFreeHostingStatus,
  useGetOrgHostingUsageSummary,
  useListUsageBillingBudgetVersions,
} from "@/hooks/controlplane/billing-service-hooks_gen";
import { getControlPlaneClient } from "@/services/controlPlane/client";

type Int = bigint | number;

export interface UsageBillingBudgetVersion {
  /** 0 is the budget consented at checkout. */
  version: Int;
  infraBudgetCents: Int;
  actorUserId: string;
  actorEmail: string;
  createdAt?: Timestamp;
  policyVersion: string;
  /** "checkout_consent" | "budget_change". */
  source: string;
  agreementId: string;
  current: boolean;
}

export interface SetUsageBillingBudgetRequest {
  orgId: string;
  infraBudgetCents: bigint;
  policyVersion: string;
  consent: boolean;
  /** Optimistic concurrency: the version the user was looking at. */
  expectedCurrentVersion?: bigint;
}

export interface HostingAppUsage {
  deploymentId: string;
  deploymentName: string;
  environmentId: string;
  environmentName: string;
  spentCents: Int;
  projectedCents: Int;
  staticStorageBytes: Int;
  staticTransferBytes: Int;
  /** False until the static meter has read this site. */
  staticStorageMeasured?: boolean;
}

export interface OrgHostingUsageSummary {
  spentCents: Int;
  projectedMonthEndCents: Int;
  projectionIsEstimate: boolean;
  budgetCents: Int;
  periodStart?: Timestamp;
  periodEnd?: Timestamp;
  /** When new deploys would be refused at the current rate; absent when the budget holds. */
  blockedAt?: Timestamp;
  measuredThrough?: Timestamp;
  runningRateCentsPerHour: number;
  budgetReached: boolean;
  billingActive: boolean;
  apps: HostingAppUsage[];
}

export interface FreeHostingSite {
  deploymentId: string;
  environmentName: string;
  name: string;
  url: string;
  storageBytes: Int;
  storageMeasured: boolean;
}

export interface FreeHostingStatus {
  /** This org holds its founder's free static allowance. */
  isDesignated: boolean;
  /** The org that holds it instead; empty unless the caller is that founder. */
  heldByOrgName: string;
  callerIsFounder: boolean;
  sites: FreeHostingSite[];
  /** Zero means there is no fixed site count limit. */
  sitesLimit: Int;
  storageBytes: Int;
  storageLimitBytes: Int;
  transferBytes: Int;
  transferLimitBytes: Int;
  periodEnd?: Timestamp;
  nextMoveAllowedAt?: Timestamp;
  /** The legacy per-site cap, when positive. Zero or absent means no cap is reported. */
  siteStorageLimitBytes?: Int;
  /** False until storage has been measured for this org. */
  storageMeasured?: boolean;
}

/** A site named by the designation move: the org and project it belongs to. */
export interface FreeHostingMoveSite {
  deploymentId: string;
  orgId: string;
  project: string;
  env: string;
  name: string;
  url: string;
}

export interface MoveFreeHostingDesignationResponse {
  fromOrgId: string;
  toOrgId: string;
  /** Sites that stop being served free when the designation leaves their org. */
  stoppingSites: FreeHostingMoveSite[];
  servedSites: FreeHostingMoveSite[];
  unservedSites: FreeHostingMoveSite[];
  /**
   * The move is blocked by the 30-day cooldown. AUTHORITATIVE: a dry run sets
   * it instead of failing, and a real move inside it fails with
   * free_hosting_move_cooldown.
   */
  cooldownApplies: boolean;
  /**
   * When the next move is allowed; unset with no cooldown. It can be set while
   * this move is exempt (leaving an org that pays for hosting), so it never
   * decides "blocked" on its own.
   */
  nextMoveAllowedAt?: Timestamp;
  /** False on every dry run, and on a real move to the org that already holds it. */
  moved: boolean;
}

interface PendingOrganizationMethods {
  moveFreeHostingDesignation(req: { orgId: string; dryRun: boolean }): Promise<MoveFreeHostingDesignationResponse>;
}

/** A pending OrganizationService RPC, or Unimplemented when this build does not carry it. */
export function pendingOrganizationMethod<K extends keyof PendingOrganizationMethods>(name: K): PendingOrganizationMethods[K] {
  return pendingMethod(OrganizationService, name) as PendingOrganizationMethods[K];
}

function pendingMethod(service: DescService, name: string): (request: never) => Promise<unknown> {
  return (request: never) => {
    const client = getControlPlaneClient(service) as unknown as Record<string, unknown>;
    const method = client[name];
    if (typeof method !== "function") {
      return Promise.reject(new ConnectError(`${name} is not in this build's control-plane client`, Code.Unimplemented));
    }
    return (method as (r: never) => Promise<unknown>).call(client, request);
  };
}

/** Code.Unimplemented: the server (or this build) does not serve the RPC yet. */
export function isUnimplemented(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.Unimplemented;
}

// Rollout and permission failures are not transient, and must never turn into zeros.
const retryHosting = (failures: number, error: unknown) =>
  failures < 2 &&
  !(error instanceof ConnectError && [Code.Unimplemented, Code.PermissionDenied, Code.InvalidArgument, Code.Unauthenticated].includes(error.code));

/**
 * The keys these reads and the agreement read share: the generated
 * BillingService keys, so an org switch purges them with the rest of the
 * service (store/activeOrgStore) and the agreement's entry is the one
 * UsageBillingPanel and LimitsStrip read through the generated hook.
 */
export const hostingBillingKeys = {
  agreement: (orgId: string) => billingServiceKeys.getCurrentUserUsageBilling({ orgId }),
  budgetVersions: (orgId: string) => billingServiceKeys.listUsageBillingBudgetVersions({ orgId }),
  usageSummary: (orgId: string) => billingServiceKeys.getOrgHostingUsageSummary({ orgId }),
  freeTier: (orgId: string) => billingServiceKeys.getFreeHostingStatus({ orgId }),
};

export function useUsageBillingBudgetVersions(orgId: string, enabled = true) {
  return useListUsageBillingBudgetVersions({ orgId }, {
    enabled: enabled && !!orgId,
    staleTime: 30_000,
    retry: retryHosting,
  });
}

export function useSetUsageBillingBudget(orgId: string) {
  const cache = useQueryClient();
  return useMutation({
    // Keyed to the org it was started in: an org switch detaches this observer
    // without cancelling that org's valid write.
    mutationKey: ["hosting-billing", "set-budget", orgId],
    // Refresh only this org's hosting reads, even when the write is rejected.
    mutationFn: (request: Omit<SetUsageBillingBudgetRequest, "orgId">) =>
      billingServiceMutations(cache).setUsageBillingBudget({ ...request, orgId }, { invalidate: false }),
    onSettled: () => {
      void cache.invalidateQueries({ queryKey: hostingBillingKeys.agreement(orgId) });
      void cache.invalidateQueries({ queryKey: hostingBillingKeys.budgetVersions(orgId) });
      void cache.invalidateQueries({ queryKey: hostingBillingKeys.usageSummary(orgId) });
    },
  });
}

export function useOrgHostingUsageSummary(orgId: string | null) {
  return useGetOrgHostingUsageSummary({ orgId: orgId ?? "" }, {
    enabled: !!orgId,
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
    retry: retryHosting,
  });
}

/**
 * Founder-only: make `orgId` the free hosting org. Always previewed with
 * dryRun first (the sites that would stop, whether the 30-day cooldown
 * applies); only an explicit confirmation sends dryRun=false.
 */
export function useMoveFreeHostingDesignation(orgId: string) {
  const cache = useQueryClient();
  return useMutation({
    mutationKey: ["hosting-billing", "move-free-designation", orgId],
    mutationFn: (dryRun: boolean) => pendingOrganizationMethod("moveFreeHostingDesignation")({ orgId, dryRun }),
    onSuccess: (_response, dryRun) => {
      // A real move that answered moved=false means this org already held it:
      // the status read is stale either way.
      if (!dryRun) void cache.invalidateQueries({ queryKey: billingServiceKeys.getFreeHostingStatus.all });
    },
  });
}

export function useFreeHostingStatus(orgId: string | null) {
  return useGetFreeHostingStatus({ orgId: orgId ?? "" }, {
    enabled: !!orgId,
    staleTime: 60_000,
    retry: retryHosting,
  });
}
