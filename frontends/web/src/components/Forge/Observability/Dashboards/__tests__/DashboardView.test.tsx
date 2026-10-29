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
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { DashboardSearch } from "../dashboardModel";
import { DashboardView, type DashboardHrefs } from "../DashboardView";

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

function renderDashboard(dashRef: DashboardRef, initial: DashboardSearch = {}, options: FixtureOptions = {}, env = "prod") {
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
  renderWithApi(api, <Stateful />);
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
