// Copyright (c) 2026 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardPage, DashboardsPage } from "../DashboardsPages";

const route = vi.hoisted(() => ({ params: { kind: "builtin", key: "org-costs" }, search: { env: "unrecorded", service: "worker", range: "7d", rpcMethod: "Get", version: 9 } }));
vi.mock("@tanstack/react-router", async (original) => ({ ...(await original<typeof import("@tanstack/react-router")>()), useParams: () => route.params, useSearch: () => route.search, useNavigate: () => vi.fn() }));
vi.mock("../../../Console/consoleContext", async (original) => ({ ...(await original<typeof import("../../../Console/consoleContext")>()), useForgeConsole: () => ({ forgeProject: "shop", env: "unrecorded", setEnv: vi.fn(), href: () => "/dashboard", roster: { envs: [], isLoading: false, resolvingName: false, live: {} } }) }));
vi.mock("../OrganizationCostsView", () => ({ OrganizationCostsView: ({ orgId }: { orgId: string }) => <h1>Organization costs for {orgId}</h1> }));
vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const builtin = {
  ref: { kind: "builtin" as const, projectId: "", key: "org-costs" }, source: "builtin" as const, viewKind: "organization-costs" as const,
  title: "Organization costs", description: "Canonical accounting", readOnly: true, starred: false, lastViewedAtMs: null,
  panelCount: 0, previewPanels: [], release: null, workspace: null, category: "organization",
};

describe("organization dashboard routes without deployments", () => {
  it("opens the typed cost view without roster, environment or viewer controls", async () => {
    const onCall = vi.fn();
    const api = createFixtureApi({ onCall });
    api.getDashboard = vi.fn().mockResolvedValue({ summary: builtin, spec: { id: "org-costs", title: "Organization costs", filters: [], panels: [] }, version: 0, canWrite: false });
    api.recordDashboardView = vi.fn().mockResolvedValue(undefined);
    renderWithApi(api, <DashboardPage />);
    expect(await screen.findByRole("heading", { name: /Organization costs for/ })).toBeInTheDocument();
    expect(api.getDashboard).toHaveBeenCalledWith(expect.objectContaining({ ref: builtin.ref, version: undefined, releaseVersion: undefined }));
    expect(screen.queryByTestId("observability-no-envs")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboard-variables")).not.toBeInTheDocument();
    expect(onCall.mock.calls.some(([name]) => ["QueryPanel", "GetServiceOverview", "ListAlertHistory"].includes(name))).toBe(false);
  });

  // main's getting-started guidance (#1632) still shows with no environment,
  // but above the list rather than in place of it: the builtin cost view needs
  // no deployment, so it must stay reachable.
  it("keeps automatic discovery reachable when the environment roster is empty", async () => {
    const api = createFixtureApi();
    api.listDashboards = vi.fn().mockResolvedValue({ dashboards: [builtin], canWrite: false });
    renderWithApi(api, <DashboardsPage />);
    // The Overview hub lands first; the built-in is in its Platform group.
    expect(await screen.findByTestId("hub-group-platform")).toHaveTextContent("Organization costs");
    expect(screen.getByTestId("hub-health-undeployed")).toBeInTheDocument();
    expect(screen.getByTestId("observability-no-envs")).toBeInTheDocument();
  });
});
