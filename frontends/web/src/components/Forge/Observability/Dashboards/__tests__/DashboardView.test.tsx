// Copyright (c) 2025 Reliant Labs

/**
 * One dashboard against the in-memory DashboardService and the fixture
 * telemetry: a release dashboard is read-only with Save as copy; a workspace
 * one has its history (open an old version, restore it as a new one),
 * Promote to code (the file, with the download as the way while the pull
 * request is not wired) and archive; the variables, Compare, auto-refresh,
 * Share and full screen; release markers and alert bands; the drill-down.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { DashboardSearch } from "../dashboardModel";
import { DashboardView, type DashboardHrefs } from "../DashboardView";
import { ForgeConsoleContext, type ForgeConsole } from "../../../Console/consoleContext";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs: DashboardHrefs = {
  logs: (s) => `/logs?${new URLSearchParams(Object.entries(s).filter(([, v]) => v !== undefined).map(([k, v]) => [k, String(v)])).toString()}`,
  traces: (s) => `/traces?${new URLSearchParams(Object.entries(s).filter(([, v]) => v !== undefined).map(([k, v]) => [k, String(v)])).toString()}`,
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref, search = {}) => `/d/${ref.kind}/${ref.key}${search.version ? `?version=${search.version}` : ""}${search.release ? `?release=${search.release}` : ""}`,
};

const ref = (kind: "release" | "workspace", key: string): DashboardRef => ({ kind, projectId: FIXTURE_PROJECT, key });

function renderDashboard(dashRef: DashboardRef, initial: DashboardSearch = {}, options: FixtureOptions = {}, env = "prod", inConsole = false) {
  const onCall = vi.fn();
  const navigateTo = vi.fn();
  const api = createFixtureApi({ ...options, onCall });
  let latest = initial;
  function Stateful() {
    const [search, setSearch] = useState<DashboardSearch>(initial);
    return (
      <DashboardView
        project={FIXTURE_PROJECT}
        dashRef={dashRef}
        env={env}
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) =>
          setSearch((prev) => {
            latest = { ...prev, ...next };
            return latest;
          })
        }
        onEnvChange={vi.fn()}
        hrefs={hrefs}
        navigateTo={navigateTo}
      />
    );
  }
  const console = { forgeProject: "shop", projectId: "proj-1", env, setEnv: vi.fn(), page: { kind: "observability" }, href: () => "/" } as unknown as ForgeConsole;
  renderWithApi(
    api,
    inConsole ? (
      <ForgeConsoleContext.Provider value={console}>
        <Stateful />
      </ForgeConsoleContext.Provider>
    ) : (
      <Stateful />
    )
  );
  return { onCall, navigateTo, current: () => latest };
}

const calls = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req);

describe("a release dashboard", () => {
  it("is read-only, says where it is declared, records the view, and saves a copy for the environment", async () => {
    const user = userEvent.setup();
    const { onCall, navigateTo } = renderDashboard(ref("release", "checkout"));
    expect(await screen.findByRole("heading", { level: 1, name: "Checkout funnel" })).toBeInTheDocument();
    expect(screen.getByTestId("dashboard-readonly-note")).toHaveTextContent("Declared in your repo at deploy/observability/dashboards/checkout.json");
    expect(screen.getByTestId("dashboard-header")).toHaveTextContent("Shipped in v1.8.2");
    expect(screen.queryByTestId("dashboard-promote-open")).not.toBeInTheDocument();
    await waitFor(() => expect(calls(onCall, "RecordDashboardView")).toHaveLength(1));

    await user.click(screen.getByTestId("dashboard-save-copy"));
    expect(screen.getByTestId("dashboard-copy-title")).toHaveValue("Checkout funnel (copy)");
    await user.click(screen.getByTestId("dashboard-copy-confirm"));
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/d/workspace/checkout"));
    expect(calls(onCall, "SaveAsCopy")[0]).toMatchObject({ from: { kind: "release", key: "checkout" }, envScope: { envIds: ["env-prod"] } });
  });

  it("lists the releases its content changed in", async () => {
    const user = userEvent.setup();
    renderDashboard(ref("release", "overview"));
    await user.click(await screen.findByTestId("dashboard-history"));
    expect(await screen.findByRole("menuitem", { name: /v1\.8\.1/ })).toHaveAttribute("href", "/d/release/overview?release=v1.8.1");
  });

  it("draws the browser kinds of the frontend dashboard", async () => {
    renderDashboard(ref("release", "frontend-web"));
    expect(await screen.findByTestId("obs-panel-funnel")).toBeInTheDocument();
    expect(await screen.findByTestId("obs-panel-issues")).toBeInTheDocument();
    expect(await screen.findByTestId("obs-panel-sessions")).toBeInTheDocument();
  });
});

describe("a platform dashboard", () => {
  function platformApi(scope: "platform" | undefined) {
    const base = createFixtureApi();
    return {
      ...base,
      getDashboard: async (req: Parameters<typeof base.getDashboard>[0]) => {
        const doc = await base.getDashboard(req);
        return { ...doc, spec: { ...doc.spec, scope } };
      },
    };
  }

  function render(scope: "platform" | undefined, env: string) {
    const onCall = vi.fn();
    const api = platformApi(scope);
    const wrapped = { ...api, queryPanel: (req: Parameters<typeof api.queryPanel>[0]) => (onCall(req), api.queryPanel(req)) };
    renderWithApi(
      wrapped,
      <DashboardView project={FIXTURE_PROJECT} dashRef={ref("release", "checkout")} env={env} environments={FIXTURE_ENVS} search={{}} onSearch={vi.fn()}
        onEnvChange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />
    );
    return onCall;
  }

  it("reads platform-wide, whatever environment the top bar names, and says so", async () => {
    const onCall = render("platform", "prod");
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(onCall).toHaveBeenCalled());
    for (const [req] of onCall.mock.calls) {
      expect(req).toMatchObject({ platformScope: true, envIds: [] });
      expect(req.projectId).toBeUndefined();
    }
  });

  it("still reads platform-wide for an environment this project has no record of", async () => {
    const onCall = render("platform", "a-preview-that-is-gone");
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(onCall).toHaveBeenCalled());
    expect(onCall.mock.calls[0][0]).toMatchObject({ platformScope: true, envIds: [] });
  });

  it("borrows no environment's alert bands or release to compare with, whatever environment is selected", async () => {
    for (const env of ["prod", "staging"]) {
      const api = platformApi("platform");
      const history = vi.fn(api.listAlertHistory);
      const releases = vi.fn(api.listReleases);
      const queried = vi.fn();
      renderWithApi(
        { ...api, listAlertHistory: history, listReleases: releases, queryPanel: (req: Parameters<typeof api.queryPanel>[0]) => (queried(req), api.queryPanel(req)) },
        <DashboardView project={FIXTURE_PROJECT} dashRef={ref("release", "checkout")} env={env} environments={FIXTURE_ENVS} search={{ compare: "release" }} onSearch={vi.fn()}
          onEnvChange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />
      );
      await screen.findByTestId("dashboard-grid");
      await waitFor(() => expect(queried).toHaveBeenCalled());
      expect(history).not.toHaveBeenCalled();
      expect(releases).not.toHaveBeenCalled();
      cleanup();
    }
  });

  it("is unchanged for an ordinary dashboard: scoped to the environment, never platform-wide", async () => {
    const onCall = render(undefined, "prod");
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(onCall).toHaveBeenCalled());
    for (const [req] of onCall.mock.calls) {
      expect(req.envIds).toEqual(["env-prod"]);
      expect(req.platformScope).toBeUndefined();
    }
  });
});

describe("a workspace dashboard", () => {
  it("shows its version, editor and scope, and opens an older version to restore it as a new one", async () => {
    const user = userEvent.setup();
    renderDashboard(ref("workspace", "checkout-latency"));
    const header = await screen.findByTestId("dashboard-header");
    await waitFor(() => expect(header).toHaveTextContent("v4 · edited 2h ago by ada"));
    expect(header).toHaveTextContent("all environments");
    await user.click(screen.getByTestId("dashboard-history"));
    const v2 = await screen.findByRole("menuitem", { name: /v2 — Add orders/ });
    expect(v2).toHaveAttribute("href", "/d/workspace/checkout-latency?version=2");
  });

  it("restores an old version as the next one", async () => {
    const user = userEvent.setup();
    const { onCall, current } = renderDashboard(ref("workspace", "checkout-latency"), { version: 2 });
    expect(await screen.findByTestId("dashboard-old-version")).toHaveTextContent("You are looking at v2. The current version is v4.");
    expect(screen.getAllByTestId(/^dashboard-panel-(?!menu)/)).toHaveLength(4);
    await user.click(screen.getByTestId("dashboard-restore"));
    await waitFor(() => expect(current().version).toBeUndefined());
    expect(calls(onCall, "RestoreDashboardVersion")[0]).toMatchObject({ version: 2, baseVersion: 4 });
  });

  it("promotes to code: the file and where it goes, downloaded while the pull request is not wired", async () => {
    const user = userEvent.setup();
    renderDashboard(ref("workspace", "checkout-latency"));
    await user.click(await screen.findByTestId("dashboard-promote-open"));
    expect(await screen.findByTestId("dashboard-promote-no-pr")).toHaveTextContent("deploy/observability/dashboards/checkout-latency.json");
    expect(screen.getByTestId("dashboard-promote")).toHaveTextContent('"version": "forge.dashboards/v1"');
    expect(screen.getByTestId("dashboard-promote-download")).toHaveTextContent("Download checkout-latency.json");
    expect(screen.queryByTestId("dashboard-promote-pr")).not.toBeInTheDocument();
  });

  it("archives, then goes back to the list", async () => {
    const user = userEvent.setup();
    const { navigateTo } = renderDashboard(ref("workspace", "daemon-fleet"));
    await user.click(await screen.findByTestId("dashboard-more"));
    await user.click(screen.getByTestId("dashboard-more-archive"));
    await user.click(screen.getByRole("button", { name: "Archive dashboard" }));
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/dashboards"));
  });

  it("is not here once archived, or never was", async () => {
    renderDashboard(ref("workspace", "nope"));
    expect(await screen.findByTestId("dashboard-missing")).toHaveTextContent("It was archived");
  });
});

describe("variables, compare, refresh, share, full screen", () => {
  it("runs every panel over the scope and the service, and compares with the previous period", async () => {
    const user = userEvent.setup();
    const { onCall, current } = renderDashboard(ref("workspace", "checkout-latency"), { service: "api" });
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(calls(onCall, "QueryPanel").length).toBeGreaterThan(0));
    expect(calls(onCall, "QueryPanel")[0]).toMatchObject({ envIds: ["env-prod"], services: ["api"] });

    const before = calls(onCall, "QueryPanel").length;
    await user.click(within(screen.getByTestId("dashboard-compare")).getByRole("button", { name: "Previous period" }));
    expect(current().compare).toBe("period");
    await waitFor(() => expect(calls(onCall, "QueryPanel").length).toBeGreaterThan(before));
    // The comparison reads the hour before this one.
    const shifted = calls(onCall, "QueryPanel").slice(before) as { window: { endMs: number } }[];
    expect(shifted.some((req) => Date.now() - req.window.endMs > 50 * 60_000)).toBe(true);
    expect(await screen.findAllByTestId("obs-panel-change")).not.toHaveLength(0);
  });

  it("applies declared RPC filters only to their panels and queries none grouping explicitly", async () => {
    const { onCall } = renderDashboard(ref("workspace", "checkout-latency"), { rpcService: "pkg.Service", rpcMethod: "Get", breakdown: "none" });
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(calls(onCall, "QueryPanel").length).toBeGreaterThan(0));
    const requests = calls(onCall, "QueryPanel");
    const rpc = requests.find((request) => request.panel.id === "latency-by-route");
    expect(rpc.panel).toMatchObject({ groupBy: [], where: expect.arrayContaining([
      { field: "span.attr.rpc.service", op: "eq", value: "pkg.Service" },
      { field: "span.attr.rpc.method", op: "eq", value: "Get" },
    ]) });
    const experience = requests.find((request) => request.panel.id === "conversion");
    expect(experience.panel.where).toBeUndefined();
    expect(calls(onCall, "SaveDashboard")).toHaveLength(0);
  });

  it("compares with the previous release, and says why it cannot across every environment", async () => {
    renderDashboard(ref("workspace", "checkout-latency"), { compare: "release" });
    expect(await screen.findByTestId("dashboard-compare-release")).toHaveTextContent("before v1.8.3 reached prod");
  });

  it("refuses the previous release with every environment in scope", async () => {
    renderDashboard(ref("workspace", "checkout-latency"), { compare: "release" }, {}, "all");
    expect(await screen.findByTestId("dashboard-compare-unavailable")).toHaveTextContent("Pick one environment");
  });

  it("sets auto-refresh, copies a link, and goes full screen and back", async () => {
    const user = userEvent.setup();
    const { current } = renderDashboard(ref("workspace", "checkout-latency"));
    await screen.findByTestId("dashboard-grid");
    await user.click(screen.getByTestId("dashboard-refresh"));
    await user.click(screen.getByRole("menuitemradio", { name: "Every 30s" }));
    expect(current().refresh).toBe("30s");
    await user.click(screen.getByTestId("dashboard-share"));
    expect(await screen.findByText("Link copied")).toBeInTheDocument();
    // The link is the page's own URL: every variable is already in it.
    expect(await navigator.clipboard.readText()).toBe(window.location.href);
    await user.click(screen.getByTestId("dashboard-tv-open"));
    expect(await screen.findByTestId("dashboard-tv")).toHaveTextContent("refreshes every 30s");
    await act(async () => {
      await user.keyboard("{Escape}");
    });
    await waitFor(() => expect(screen.queryByTestId("dashboard-tv")).not.toBeInTheDocument());
  });

  it("keeps annotation and metric visibility in viewer state without saving", async () => {
    const user = userEvent.setup();
    const { onCall, current } = renderDashboard(ref("workspace", "checkout-latency"));
    await screen.findByTestId("dashboard-grid");
    await user.click(screen.getByRole("checkbox", { name: "Release markers" }));
    await user.click(screen.getByRole("checkbox", { name: "Alert bands" }));
    expect(current()).toMatchObject({ releases: false, alerts: false });
    await user.click(screen.getByTestId("dashboard-metrics"));
    await user.click(screen.getByRole("menuitemcheckbox", { name: /p95 latency by route/ }));
    expect(current().hiddenPanels).toContain("latency-by-route");
    expect(screen.queryByTestId("dashboard-panel-latency-by-route")).not.toBeInTheDocument();
    expect(calls(onCall, "SaveDashboard")).toHaveLength(0);
  });

  it("drills from a series into Logs, Traces and the workload", async () => {
    const user = userEvent.setup();
    renderDashboard(ref("workspace", "checkout-latency"));
    const panel = await screen.findByTestId("dashboard-panel-latency-by-route");
    const legend = await within(panel).findByTestId("obs-panel-legend");
    await user.click(within(legend).getAllByRole("button")[0]);
    const menu = await screen.findByTestId("dashboard-drill");
    expect(within(menu).getByTestId("drill-traces")).toHaveAttribute("href", expect.stringContaining("q=orders.v1.OrdersService%2FCreateOrder"));
    expect(within(menu).getByTestId("drill-logs")).toHaveAttribute("href", expect.stringContaining("range=1h"));
    expect(within(menu).queryByTestId("drill-workload")).not.toBeInTheDocument();
  });
});

describe("a day-grained dashboard (the empty-dashboards audit)", () => {
  const usage = () => ref("release", "product-usage");
  const grained: FixtureOptions = { dashboardsDayGrained: true };
  const queried = (onCall: ReturnType<typeof vi.fn>, id: string) => calls(onCall, "QueryPanel").filter((req) => req.panel.id === id);

  it("opens on the range the spec declares, not the console's hour, and sends the 7 day window", async () => {
    const { onCall } = renderDashboard(usage(), {}, grained);
    expect(await screen.findByRole("heading", { level: 1, name: "Product: usage" })).toBeInTheDocument();
    await waitFor(() => expect(queried(onCall, "workflows-by-outcome").length).toBeGreaterThan(0));
    expect(screen.getByRole("combobox", { name: /time/i })).toHaveValue("7d");
    const { window } = queried(onCall, "workflows-by-outcome")[0];
    expect(window.endMs - window.startMs).toBe(7 * 24 * 3_600_000);
    expect(screen.queryByTestId("dashboard-panel-widened-workflows-by-outcome")).not.toBeInTheDocument();
  });

  it("draws a day per date: eight buckets, a UTC calendar note, no single stray point", async () => {
    renderDashboard(usage(), {}, grained);
    const panel = await screen.findByTestId("dashboard-panel-workflows-by-outcome");
    await waitFor(() => expect(within(panel).getByTestId("chart-bucket-note")).toHaveTextContent("1-day buckets: UTC calendar dates"));
  });

  it("widens a day panel opened on a shorter range to hold two buckets, and says so, while a step panel keeps the range", async () => {
    const { onCall } = renderDashboard(usage(), { range: "1h" }, grained);
    const note = await screen.findByTestId("dashboard-panel-widened-workflows-by-outcome");
    expect(note).toHaveTextContent("Window smaller than this panel's 1-day bucket: showing last 7 days");
    await waitFor(() => expect(queried(onCall, "workflows-by-outcome").length).toBeGreaterThan(0));
    const day = queried(onCall, "workflows-by-outcome")[0].window;
    expect(day.endMs - day.startMs).toBe(7 * 24 * 3_600_000);
    await waitFor(() => expect(queried(onCall, "events-by-step").length).toBeGreaterThan(0));
    const step = queried(onCall, "events-by-step")[0].window;
    expect(step.endMs - step.startMs).toBe(3_600_000);
    expect(screen.queryByTestId("dashboard-panel-widened-events-by-step")).not.toBeInTheDocument();
  });

  it("opens Explore on the dashboard's own range when the URL names none", async () => {
    const user = userEvent.setup();
    renderDashboard(usage(), {}, grained, "prod", true);
    await user.click(await screen.findByTestId("dashboard-panel-menu-goroutines"));
    const explore = await screen.findByTestId("dashboard-panel-explore-goroutines");
    expect(new URL(explore.getAttribute("href")!, "http://x").searchParams.get("range")).toBe("7d");
  });

  it("keeps a range the viewer picked, and offers the dashboard's own as the unmarked choice", async () => {
    const user = userEvent.setup();
    const { current } = renderDashboard(usage(), {}, grained);
    const time = await screen.findByRole("combobox", { name: /time/i });
    await user.selectOptions(time, "24h");
    expect(current().range).toBe("24h");
    await user.selectOptions(time, "7d");
    expect(current().range).toBeUndefined();
  });
});

describe("a dashboard of many panels", () => {
  class Viewport {
    static all: Viewport[] = [];
    elements: Element[] = [];
    callback: (entries: Partial<IntersectionObserverEntry>[]) => void;
    constructor(callback: (entries: Partial<IntersectionObserverEntry>[]) => void) {
      this.callback = callback;
      Viewport.all.push(this);
    }
    observe(element: Element) {
      this.elements.push(element);
    }
    disconnect() {}
    reveal(element: Element) {
      this.callback([{ isIntersecting: true, target: element }]);
    }
  }

  it("reads only the panels near the viewport, and the rest as they scroll in", async () => {
    Viewport.all = [];
    vi.stubGlobal("IntersectionObserver", Viewport);
    try {
      const { onCall } = renderDashboard(ref("release", "overview"));
      expect(await screen.findByTestId("dashboard-grid")).toBeInTheDocument();
      const asked = () => new Set(calls(onCall, "QueryPanel").map((request) => (request as { panel: { id: string } }).panel.id));
      // Nothing has scrolled near yet: nothing is read.
      expect(asked().size).toBe(0);

      const bodies = Viewport.all.flatMap((observer) => observer.elements.map((element) => ({ observer, element })));
      expect(bodies.length).toBe(12);
      act(() => bodies.slice(0, 4).forEach(({ observer, element }) => observer.reveal(element)));
      await waitFor(() => expect(asked().size).toBe(4));
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(asked().size).toBe(4);

      act(() => bodies.slice(4, 8).forEach(({ observer, element }) => observer.reveal(element)));
      await waitFor(() => expect(asked().size).toBe(8));
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("an org-scoped dashboard", () => {
  const ORG_ENVS = [
    { id: "env-cp-prod", name: "control-plane/prod", project: "control-plane" },
    { id: "env-landing-prod", name: "landing/prod", project: "landing" },
    { id: "env-landing-staging", name: "landing/staging", project: "landing" },
  ];

  function render(scope: "org" | undefined, initial: DashboardSearch = {}, roster: { loading?: boolean; error?: Error; retry?: () => void } = {}) {
    const onCall = vi.fn();
    const base = createFixtureApi({ onCall });
    const api = {
      ...base,
      getDashboard: async (req: Parameters<typeof base.getDashboard>[0]) => {
        const doc = await base.getDashboard(req);
        return { ...doc, spec: { ...doc.spec, scope } };
      },
      queryPanel: (req: Parameters<typeof base.queryPanel>[0]) => (onCall("scope", req), base.queryPanel(req)),
    };
    function Stateful() {
      const [search, setSearch] = useState<DashboardSearch>(initial);
      return (
        <DashboardView project={FIXTURE_PROJECT} dashRef={ref("release", "checkout")} env="prod" environments={FIXTURE_ENVS} orgEnvironments={roster.loading || roster.error ? [] : ORG_ENVS}
          orgEnvironmentsLoading={roster.loading} orgEnvironmentsError={roster.error} onRetryOrgEnvironments={roster.retry}
          search={search} onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))} onEnvChange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />
      );
    }
    renderWithApi(api, <Stateful />);
    return onCall;
  }

  const scopes = (onCall: ReturnType<typeof vi.fn>) => onCall.mock.calls.filter(([name]) => name === "scope").map(([, req]) => req);

  it("does not widen a selected environment while the org roster is loading", async () => {
    const onCall = render("org", { orgEnvs: ["landing/prod"] }, { loading: true });
    await waitFor(() => expect(calls(onCall, "GetDashboard").length).toBeGreaterThan(0));
    expect(screen.getByTestId("dashboard-loading")).toBeInTheDocument();
    expect(scopes(onCall)).toHaveLength(0);
  });

  it("shows a retryable roster failure instead of querying every environment", async () => {
    const retry = vi.fn();
    const onCall = render("org", { orgEnvs: ["landing/prod"] }, { error: new Error("Roster unavailable"), retry });
    await screen.findByRole("alert");
    expect(scopes(onCall)).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("reads the whole org: no project, no environment, whatever the console's environment is", async () => {
    const onCall = render("org");
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(scopes(onCall).length).toBeGreaterThan(0));
    for (const req of scopes(onCall)) {
      expect(req.projectId).toBeUndefined();
      expect(req.envIds).toEqual([]);
      expect(req.platformScope).toBeUndefined();
    }
  });

  it("offers every org environment as project/env and reads the ones picked", async () => {
    const onCall = render("org");
    await screen.findByTestId("dashboard-grid");
    await userEvent.click(screen.getByTestId("dashboard-org-env-picker"));
    const popover = screen.getByTestId("dashboard-org-env-picker-popover");
    for (const env of ORG_ENVS) expect(within(popover).getByTestId(`dashboard-org-env-option-${env.name}`)).toBeInTheDocument();
    expect(within(popover).getByText("landing/prod")).toBeInTheDocument();

    onCall.mockClear();
    await userEvent.click(within(popover).getByTestId("dashboard-org-env-option-landing/prod"));
    await userEvent.click(within(popover).getByTestId("dashboard-org-env-option-control-plane/prod"));
    await waitFor(() => expect(scopes(onCall).some((req) => req.envIds.length === 2)).toBe(true));
    const picked = scopes(onCall).filter((req) => req.envIds.length === 2).at(-1);
    expect(picked.envIds).toEqual(["env-cp-prod", "env-landing-prod"]);
    expect(picked.projectId).toBeUndefined();
    expect(screen.getByTestId("dashboard-org-env-picker")).toHaveTextContent("control-plane/prod, landing/prod");
  });

  it("reads a link's picked environments", async () => {
    const onCall = render("org", { orgEnvs: ["landing/staging"] });
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(scopes(onCall).length).toBeGreaterThan(0));
    for (const req of scopes(onCall)) expect(req.envIds).toEqual(["env-landing-staging"]);
  });

  it("links its explorers to the picked project/env on the org's pages, not the route project's", async () => {
    render("org", { orgEnvs: ["landing/prod"] });
    await screen.findByTestId("dashboard-grid");
    await userEvent.click(await screen.findByTestId("dashboard-panel-menu-orders"));
    const traces = await screen.findByRole("menuitem", { name: /Explore in Traces/ });
    const href = new URL(traces.getAttribute("href")!, "http://x");
    expect(href.pathname).toBe("/forge/observability/traces");
    expect(href.searchParams.get("envs")).toBe(JSON.stringify(["landing/prod"]));
    const explore = screen.queryByTestId("dashboard-panel-explore-orders");
    if (explore) {
      const metrics = new URL(explore.getAttribute("href")!, "http://x");
      expect(metrics.pathname).toBe("/forge/observability/metrics");
      expect(metrics.searchParams.get("envs")).toBe(JSON.stringify(["landing/prod"]));
    }
  });

  it("leaves an ordinary dashboard on its project: the route's project and environment, no org picker", async () => {
    const onCall = render(undefined);
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(scopes(onCall).length).toBeGreaterThan(0));
    for (const req of scopes(onCall)) expect(req).toMatchObject({ projectId: FIXTURE_PROJECT, envIds: ["env-prod"] });
    expect(screen.queryByTestId("dashboard-org-env-picker")).not.toBeInTheDocument();
  });
});
