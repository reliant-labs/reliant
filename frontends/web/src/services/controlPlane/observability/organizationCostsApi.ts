// Copyright (c) 2025 Reliant Labs

import { BillingService, type GetOrganizationCostSummaryResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "../client";

/** Raw typed accounting facts: optional bigint nanos must stay optional. */
export type OrganizationCostSummary = GetOrganizationCostSummaryResponse;

/** Organization accounting has no telemetry environment, time, process or RPC scope. */
export async function getOrganizationCostSummary(orgId: string): Promise<OrganizationCostSummary> {
  return getControlPlaneClient(BillingService).getOrganizationCostSummary({ orgId });
}
