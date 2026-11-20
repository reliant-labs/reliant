// Copyright (c) 2025 Reliant Labs

import { BillingService, type GetOrganizationCostSummaryResponse, type GetProjectCostResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "../client";

/** Raw typed accounting facts: optional bigint nanos must stay optional. */
export type OrganizationCostSummary = GetOrganizationCostSummaryResponse;

/** Organization accounting has no telemetry environment, time, process or RPC scope. */
export async function getOrganizationCostSummary(orgId: string): Promise<OrganizationCostSummary> {
  return getControlPlaneClient(BillingService).getOrganizationCostSummary({ orgId });
}

/** One hosted project's month: MTD, forecast, daily by category and environment, drivers (hosted compute, databases, CDN). */
export type ProjectCost = GetProjectCostResponse;

/** `projectId` is the forge project name (hosted_projects); no environments is every environment of it. */
export async function getProjectCost(orgId: string, projectId: string, environmentIds: string[] = []): Promise<ProjectCost> {
  return getControlPlaneClient(BillingService).getProjectCost({ orgId, projectId, environmentIds });
}
