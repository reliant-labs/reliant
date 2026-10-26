// Copyright (c) 2025 Reliant Labs

/**
 * What listDashboards sends control-plane. The project is optional on the
 * wire with a meaning for each state: unset is every app of the org, "" is
 * only the project-less dashboards. The org list depends on the first, so the
 * field must be absent (not "") when no project is given.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

import { listDashboards } from "../dashboardsApi";

const client = { listDashboards: vi.fn() };

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
