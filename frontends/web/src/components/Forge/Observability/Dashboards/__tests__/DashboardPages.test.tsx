// Copyright (c) 2026 Reliant Labs
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardPage, DashboardsPage } from "../DashboardsPages";

const route = vi.hoisted(() => ({ params: { kind: "builtin", key: "org-costs" }, search: { env: "unrecorded", service: "worker", range: "7d", rpcMethod: "Get", version: 9 } }));
vi.mock("@tanstack/react-router", async (original) => ({ ...(await original<typeof import("@tanstack/react-router")>()), useParams: () => route.params, useSearch: () => route.search, useNavigate: () => vi.fn() }));
const EMPTY_ROSTER = { envs: [], isLoading: false, resolvingName: false, live: {} as Record<string, unknown> };
const consoleState = vi.hoisted(() => ({ roster: { envs: [], isLoading: false, resolvingName: false, live: {} as Record<string, unknown> } }));
vi.mock("../../../Console/consoleContext", async (original) => ({ ...(await original<typeof import("../../../Console/consoleContext")>()), useForgeConsole: () => ({ forgeProject: "shop", env: "unrecorded", setEnv: vi.fn(), href: () => "/dashboard", roster: consoleState.roster }) }));
vi.mock("../OrganizationCostsView", () => ({ OrganizationCostsView: ({ orgId }: { orgId: string }) => <h1>Organization costs for {orgId}</h1> }));
vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const builtin = {
  ref: { kind: "builtin" as const, projectId: "", key: "org-costs" }, source: "builtin" as const, viewKind: "organization-costs" as const,
  title: "Organization costs", description: "Canonical accounting", readOnly: true, starred: false, lastViewedAtMs: null,
  panelCount: 0, previewPanels: [], release: null, workspace: null, category: "organization",
};

describe("organization dashboard routes without deployments", () => {
  afterEach(() => {
    consoleState.roster = { ...EMPTY_ROSTER };
  });

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

  // 2026-11-01: control-plane has two prod environments, GetLiveView was aborted, and the page told
  // its owner "No environments to observe yet". An unread roster is not an empty one.
  it("does not say there are no environments when the roster read failed, and offers to read it again", async () => {
    const refetch = vi.fn();
    consoleState.roster = { ...EMPTY_ROSTER, live: { data: { availability: "unreachable", envs: [], detail: "GetLiveView timed out" }, refetch, isFetching: false } };
    const api = createFixtureApi();
    api.listDashboards = vi.fn().mockResolvedValue({ dashboards: [builtin], canWrite: false });
    renderWithApi(api, <DashboardsPage />);
    expect(await screen.findByTestId("hub-health-unread")).toHaveTextContent("once shop's environments load");
    expect(screen.queryByTestId("observability-no-envs")).not.toBeInTheDocument();
    expect(screen.queryByTestId("hub-health-undeployed")).not.toBeInTheDocument();
    expect(screen.getByTestId("forge-cloud-unreachable")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("obs-environments-retry"));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  // A project's standard built-in reads its environments: unlike an
  // organization view, it waits behind the roster like a release dashboard.
  it("opens a project's standard built-in in the project, behind the roster", async () => {
    route.params = { kind: "builtin", key: "usage" };
    try {
      const api = createFixtureApi();
      api.getDashboard = vi.fn().mockRejectedValue(new Error("not reached"));
      renderWithApi(api, <DashboardPage />);
      expect(await screen.findByTestId("observability-no-envs")).toBeInTheDocument();
      expect(api.getDashboard).not.toHaveBeenCalled();
    } finally {
      route.params = { kind: "builtin", key: "org-costs" };
    }
  });

  it("claims nothing about environments while the roster is still loading", async () => {
    consoleState.roster = { ...EMPTY_ROSTER, isLoading: true };
    const api = createFixtureApi();
    api.listDashboards = vi.fn().mockResolvedValue({ dashboards: [builtin], canWrite: false });
    renderWithApi(api, <DashboardsPage />);
    expect(await screen.findByTestId("hub-group-platform")).toBeInTheDocument();
    expect(screen.queryByTestId("observability-no-envs")).not.toBeInTheDocument();
  });
});
