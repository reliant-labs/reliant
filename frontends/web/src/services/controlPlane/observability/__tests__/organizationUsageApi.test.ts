// Copyright (c) 2026 Reliant Labs

/**
 * The wire contract of the usage dashboards: what ListOrganizationUsage is
 * sent, and how the two new built-in view kinds come back from
 * DashboardService.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { OrganizationUsageGranularity, OrganizationUsageGroupBy } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { DashboardSource, DashboardViewKind, ListDashboardsResponseSchema } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";

import { listDashboards } from "../dashboardsApi";
import { listOrganizationUsage } from "../organizationUsageApi";

const client = vi.hoisted(() => ({ listOrganizationUsage: vi.fn(), listDashboards: vi.fn() }));
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

beforeEach(() => vi.clearAllMocks());

describe("listOrganizationUsage request", () => {
  it("sends the organization, the UTC window as timestamps, and the typed granularity and grouping", async () => {
    client.listOrganizationUsage.mockResolvedValue({});
    await listOrganizationUsage({ orgId: "org-1", startMs: Date.parse("2026-10-01T00:00:00Z"), endMs: Date.parse("2026-11-01T00:00:00Z"), granularity: "month", groupBy: "model" });
    const req = client.listOrganizationUsage.mock.calls[0]![0];
    expect(req.orgId).toBe("org-1");
    expect(timestampMs(req.start)).toBe(Date.parse("2026-10-01T00:00:00Z"));
    expect(timestampMs(req.end)).toBe(Date.parse("2026-11-01T00:00:00Z"));
    expect(req.granularity).toBe(OrganizationUsageGranularity.MONTH);
    expect(req.groupBy).toBe(OrganizationUsageGroupBy.MODEL);
  });
});

describe("usage built-in view kinds", () => {
  it("maps ORGANIZATION_USAGE and ORGANIZATION_BILLING to their views, and an unknown kind to a spec dashboard", async () => {
    const builtin = (key: string, viewKind: DashboardViewKind) => ({ ref: { source: DashboardSource.BUILTIN, key }, source: DashboardSource.BUILTIN, viewKind, title: key, readOnly: true, group: "product" });
    client.listDashboards.mockResolvedValue(
      create(ListDashboardsResponseSchema, {
        dashboards: [builtin("org-usage", DashboardViewKind.ORGANIZATION_USAGE), builtin("org-billing", DashboardViewKind.ORGANIZATION_BILLING), builtin("future", 99 as DashboardViewKind)],
      })
    );
    const index = await listDashboards({ orgId: "org" });
    expect(index.dashboards.map((d) => [d.ref.key, d.viewKind])).toEqual([
      ["org-usage", "organization-usage"],
      ["org-billing", "organization-billing"],
      ["future", undefined],
    ]);
  });
});
