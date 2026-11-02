// Copyright (c) 2026 Reliant Labs

import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  BillingService,
  OrganizationUsageGranularity,
  OrganizationUsageGroupBy,
  type ListOrganizationUsageResponse,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "../client";

/**
 * Raw typed history from `BillingService.ListOrganizationUsage`. Optional
 * bigint nanos stay optional: an absent amount means "not measured", which the
 * views render as n/a, never as $0.
 */
export type OrganizationUsage = ListOrganizationUsageResponse;
export type OrganizationUsagePoint = OrganizationUsage["points"][number];
export type OrganizationUsageCoverage = OrganizationUsage["coverage"][number];
export type OrganizationUsageRate = OrganizationUsage["spendRates"][number];
export type OrganizationUsageInventoryItem = OrganizationUsage["inventory"][number];

export type UsageGranularity = "day" | "month";
export type UsageGroupBy = "category" | "env" | "model";

/** One history read. Start is inclusive and end exclusive, both UTC; the server caps the range at 400 days. */
export interface OrganizationUsageRequest {
  orgId: string;
  startMs: number;
  endMs: number;
  granularity: UsageGranularity;
  groupBy: UsageGroupBy;
}

const GRANULARITY: Record<UsageGranularity, OrganizationUsageGranularity> = {
  day: OrganizationUsageGranularity.DAY,
  month: OrganizationUsageGranularity.MONTH,
};

const GROUP_BY: Record<UsageGroupBy, OrganizationUsageGroupBy> = {
  category: OrganizationUsageGroupBy.CATEGORY,
  env: OrganizationUsageGroupBy.ENV,
  model: OrganizationUsageGroupBy.MODEL,
};

/** Scoped to the organization; the server re-checks billing:read on every call. */
export async function listOrganizationUsage(req: OrganizationUsageRequest): Promise<OrganizationUsage> {
  return getControlPlaneClient(BillingService).listOrganizationUsage({
    orgId: req.orgId,
    start: timestampFromMs(req.startMs),
    end: timestampFromMs(req.endMs),
    granularity: GRANULARITY[req.granularity],
    groupBy: GROUP_BY[req.groupBy],
  });
}
