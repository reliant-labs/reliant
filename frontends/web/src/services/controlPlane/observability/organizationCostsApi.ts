// Copyright (c) 2025 Reliant Labs

import type { Transport } from "@connectrpc/connect";
import { BillingService, type GetOrganizationCostSummaryResponse, type GetProjectCostResponse, type GetStorageUsageResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "../client";

/** Raw typed accounting facts: optional bigint nanos must stay optional. */
export type OrganizationCostSummary = GetOrganizationCostSummaryResponse;

/** Organization accounting has no telemetry environment, time, process or RPC scope. */
export async function getOrganizationCostSummary(orgId: string, transport?: Transport): Promise<OrganizationCostSummary> {
  return getControlPlaneClient(BillingService, transport).getOrganizationCostSummary({ orgId });
}

/** One hosted project's month: MTD, forecast, daily by category and environment, drivers (hosted compute, databases, CDN). */
export type ProjectCost = GetProjectCostResponse;

/** `projectId` is the forge project name (hosted_projects); no environments is every environment of it. */
export async function getProjectCost(orgId: string, projectId: string, environmentIds: string[] = [], transport?: Transport): Promise<ProjectCost> {
  return getControlPlaneClient(BillingService, transport).getProjectCost({ orgId, projectId, environmentIds });
}

/** The org's storage pool: included GiB, used bytes, and each product's bytes and billing basis. */
export async function getStorageUsage(orgId: string, transport?: Transport): Promise<GetStorageUsageResponse> {
  return getControlPlaneClient(BillingService, transport).getStorageUsage({ orgId });
}
