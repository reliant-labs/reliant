// Copyright (c) 2025 Reliant Labs

/**
 * What listDashboards sends control-plane. The project is optional on the
 * wire with a meaning for each state: unset is every app of the org, "" is
 * only the project-less dashboards. The org list depends on the first, so the
 * field must be absent (not "") when no project is given.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { DashboardSource, DashboardViewKind, ListDashboardsResponseSchema, GetDashboardResponseSchema } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { getDashboard, listDashboards } from "../dashboardsApi";

const client = vi.hoisted(() => ({ listDashboards: vi.fn(), getDashboard: vi.fn() }));
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

beforeEach(() => {
  vi.clearAllMocks();
  client.listDashboards.mockResolvedValue({ dashboards: [], canWrite: false });
});

describe("listDashboards request", () => {
  it("omits the project for the whole org", async () => {
    await listDashboards({ orgId: "org-1" });
    const req = client.listDashboards.mock.calls[0]![0];
    expect(req).not.toHaveProperty("projectId");
    expect(req.orgId).toBe("org-1");
  });

  it("sends a project as given, including the empty project-less one", async () => {
    await listDashboards({ orgId: "org-1", projectId: "shop", envId: "env-prod" });
    expect(client.listDashboards.mock.calls[0]![0]).toMatchObject({ projectId: "shop", envId: "env-prod" });
    await listDashboards({ orgId: "org-1", projectId: "" });
    expect(client.listDashboards.mock.calls[1]![0]).toHaveProperty("projectId", "");
  });
});

const summary = { ref: { source: DashboardSource.BUILTIN, projectId: "", key: "org-costs" }, source: DashboardSource.BUILTIN,
  viewKind: DashboardViewKind.ORGANIZATION_COSTS, title: "Organization costs", readOnly: true };

describe("builtin dashboard wire mapping", () => {
  it("retains the typed view discriminator and org-only ref for discovery and detail", async () => {
    client.listDashboards.mockResolvedValue(create(ListDashboardsResponseSchema, { dashboards: [summary] }));
    const index = await listDashboards({ orgId: "org", projectId: "app", envId: "prod" });
    expect(index.dashboards[0]).toMatchObject({ source: "builtin", ref: { kind: "builtin", projectId: "", key: "org-costs" }, viewKind: "organization-costs" });
    client.getDashboard.mockResolvedValue(create(GetDashboardResponseSchema, { summary }));
    const doc = await getDashboard({ orgId: "org", ref: index.dashboards[0].ref });
    expect(doc.summary.viewKind).toBe("organization-costs");
    expect(client.getDashboard).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org", ref: { source: DashboardSource.BUILTIN, projectId: "", key: "org-costs" } }));
  });
});

describe("group and tags", () => {
  it("reads DashboardSummary.group and .tags when the server sends them", async () => {
    const wire = create(ListDashboardsResponseSchema, { dashboards: [summary] });
    Object.assign(wire.dashboards[0]!, { group: "platform", tags: ["billing", "org"] });
    client.listDashboards.mockResolvedValue(wire);
    const index = await listDashboards({ orgId: "org" });
    expect(index.dashboards[0]).toMatchObject({ group: "platform", tags: ["billing", "org"] });
  });

  it("leaves both absent from a server that predates them, so the page infers the group", async () => {
    client.listDashboards.mockResolvedValue(create(ListDashboardsResponseSchema, { dashboards: [summary] }));
    const index = await listDashboards({ orgId: "org" });
    expect(index.dashboards[0]!.group).toBeUndefined();
    expect(index.dashboards[0]!.tags).toBeUndefined();
  });
});
