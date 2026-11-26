// Copyright (c) 2025 Reliant Labs

import type { Transport } from "@connectrpc/connect";
import { BillingService, type GetOrganizationCostSummaryResponse, type GetProjectCostResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "../client";

/** Raw typed accounting facts: optional bigint nanos must stay optional. */
export type OrganizationCostSummary = GetOrganizationCostSummaryResponse;

/** Organization accounting has no telemetry environment, time, process or RPC scope. */
export async function getOrganizationCostSummary(orgId: string, transport?: Transport): Promise<OrganizationCostSummary> {
  return getControlPlaneClient(BillingService, transport).getOrganizationCostSummary({ orgId });
}

/**
 * One hosted project's month: MTD, forecast, daily by category and environment, drivers (hosted compute, databases, CDN).
 * Read by useProjectCost (hooks/organization-usage-queries.ts) on the generated BillingService hook; `projectId` is the
 * forge project name (hosted_projects), and no environments is every environment of it.
 */
export type ProjectCost = GetProjectCostResponse;
