// Copyright (c) 2025 Reliant Labs

/**
 * Editing a workspace dashboard against the in-memory DashboardService: the
 * layout grid (move, resize, remove, saved as one version), the add / edit
 * panel drawer (builder, spec, live preview), duplicate and remove from a
 * panel's menu, a stale version refused, and a release dashboard's spec
 * shown read-only.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { DashboardSearch } from "../dashboardModel";
import { DashboardView, type DashboardHrefs } from "../DashboardView";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs: DashboardHrefs = {
  logs: () => "/logs",
  traces: () => "/traces",
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};

function renderDashboard(dashRef: DashboardRef, api?: ObservabilityApi) {
  const onCall = vi.fn();
  const fixture = api ?? createFixtureApi({ onCall });
  function Stateful() {
    const [search, setSearch] = useState<DashboardSearch>({});
    return (
      <DashboardView
        project={FIXTURE_PROJECT}
        dashRef={dashRef}
        env="prod"
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
        onEnvChange={vi.fn()}
        hrefs={hrefs}
        navigateTo={vi.fn()}
      />
    );
  }
  renderWithApi(fixture, <Stateful />);
  return { onCall };
}

const workspace: DashboardRef = { kind: "workspace", projectId: FIXTURE_PROJECT, key: "checkout-latency" };
const saves = (onCall: ReturnType<typeof vi.fn>) => onCall.mock.calls.filter(([rpc]) => rpc === "SaveDashboard").map(([, req]) => req);
const panelIds = () =>
  [...screen.getByTestId("dashboard-grid").querySelectorAll("[data-kind]")].map((el) => el.getAttribute("data-testid")!.replace("dashboard-panel-", ""));

describe("editing the layout", () => {
  it("moves, resizes and removes panels, then saves them as the next version", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-edit-layout"));
    expect(screen.getByTestId("dashboard-layout-bar")).toHaveTextContent("Save layout v4 → v5");
    await user.click(screen.getByRole("button", { name: "Move p95 checkout later" }));
    await user.click(screen.getByTestId("dashboard-panel-resize-orders"));
    await user.click(screen.getByTestId("dashboard-panel-layout-remove-recent-errors"));
    expect(panelIds().slice(0, 2)).toEqual(["orders", "p95"]);
    expect(panelIds()).not.toContain("recent-errors");
    await user.click(screen.getByTestId("dashboard-layout-save"));
    await waitFor(() => expect(screen.queryByTestId("dashboard-layout-bar")).not.toBeInTheDocument());
    const [saved] = saves(onCall);
    expect(saved).toMatchObject({ slug: "checkout-latency", baseVersion: 4, message: "Layout" });
    expect(saved.spec.panels.map((p: { id: string }) => p.id).slice(0, 2)).toEqual(["orders", "p95"]);
    expect(saved.spec.panels[0].width).toBe(12);
    expect(await screen.findByTestId("dashboard-header")).toHaveTextContent("v5");
  });

  it("moves a panel by dragging it onto another", async () => {
    const user = userEvent.setup();
    renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-edit-layout"));
    const dataTransfer = { setData: vi.fn(), effectAllowed: "" };
    fireEvent.dragStart(screen.getByTestId("dashboard-panel-slowest"), { dataTransfer });
    fireEvent.dragOver(screen.getByTestId("dashboard-panel-p95"), { dataTransfer });
    fireEvent.drop(screen.getByTestId("dashboard-panel-p95"), { dataTransfer });
    expect(panelIds()[0]).toBe("slowest");
  });

  it("cancels without writing", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-edit-layout"));
    await user.click(screen.getByTestId("dashboard-panel-layout-remove-p95"));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(panelIds()).toContain("p95");
    expect(saves(onCall)).toEqual([]);
  });
});

describe("the panel drawer", () => {
  it("adds a panel from the builder: p95 by route, in milliseconds, previewed live", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-add-panel"));
    const drawer = screen.getByTestId("panel-drawer");
    const title = within(drawer).getByTestId("panel-builder-title");
    await user.clear(title);
    await user.type(title, "p95 by route");
    await user.click(within(screen.getByTestId("panel-builder-agg-0")).getByRole("radio", { name: "p95" }));
    await user.click(within(drawer).getByTestId("panel-builder-add-group"));
    await user.type(within(drawer).getByTestId("panel-builder-group-0"), "span.name");
    await waitFor(() =>
      expect(onCall).toHaveBeenCalledWith("QueryPanel", expect.objectContaining({ panel: expect.objectContaining({ title: "p95 by route", groupBy: ["span.name"] }) }))
    );
    await user.click(within(drawer).getByTestId("panel-drawer-save"));
    await waitFor(() => expect(screen.queryByTestId("panel-drawer")).not.toBeInTheDocument());
    const [saved] = saves(onCall);
    expect(saved.message).toBe("Added p95 by route");
    expect(saved.spec.panels.at(-1)).toMatchObject({
      id: "panel-1",
      title: "p95 by route",
      unit: "ms",
      groupBy: ["span.name"],
      series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }],
    });
  });

  it("refuses to save a panel forge would refuse, and says why", async () => {
    const user = userEvent.setup();
    renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-add-panel"));
    await user.click(within(screen.getByTestId("panel-builder-kind")).getByRole("radio", { name: "Funnel" }));
    expect(screen.getByTestId("panel-drawer-problems")).toHaveTextContent("Name every funnel step.");
    expect(screen.getByTestId("panel-drawer-save")).toBeDisabled();
  });

  it("edits a panel as its spec", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-panel-menu-orders"));
    await user.click(screen.getByTestId("dashboard-panel-edit-orders"));
    await user.click(screen.getByTestId("panel-drawer-tab-spec"));
    const json = screen.getByTestId("panel-spec-json");
    fireEvent.change(json, { target: { value: "{" } });
    expect(screen.getByTestId("panel-spec-problem")).toHaveTextContent("Not JSON");
    const edited = JSON.parse(JSON.stringify({ id: "orders", title: "Orders", kind: "number", signal: "traces", series: [{ label: "orders", agg: "count" }], unit: "count", width: 6 }));
    fireEvent.change(json, { target: { value: JSON.stringify(edited) } });
    await user.click(screen.getByTestId("panel-drawer-save"));
    await waitFor(() => expect(saves(onCall)).toHaveLength(1));
    expect(saves(onCall)[0].spec.panels.find((p: { id: string }) => p.id === "orders").title).toBe("Orders");
  });

  it("says when someone saved a newer version first", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    renderDashboard(workspace, api);
    await screen.findByTestId("dashboard-add-panel");
    // Another tab saves v5 while this one still shows v4.
    const doc = await api.getDashboard({ orgId: "org-fixture", ref: workspace });
    await api.saveDashboard({ orgId: "org-fixture", projectId: FIXTURE_PROJECT, slug: "checkout-latency", spec: doc.spec, baseVersion: 4, message: "elsewhere" });
    await user.click(screen.getByTestId("dashboard-add-panel"));
    await user.click(screen.getByTestId("panel-drawer-save"));
    expect(await screen.findByTestId("panel-drawer-error")).toHaveTextContent("Someone saved a newer version");
  });
});

describe("a panel's menu", () => {
  it("duplicates and removes, each as a new version", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDashboard(workspace);
    await user.click(await screen.findByTestId("dashboard-panel-menu-p95"));
    await user.click(screen.getByTestId("dashboard-panel-duplicate-p95"));
    await waitFor(() => expect(saves(onCall)).toHaveLength(1));
    expect(saves(onCall)[0].spec.panels[1]).toMatchObject({ title: "p95 checkout (copy)" });

    await user.click(await screen.findByTestId("dashboard-panel-menu-orders"));
    await user.click(screen.getByTestId("dashboard-panel-remove-orders"));
    await user.click(screen.getByRole("button", { name: "Remove panel" }));
    await waitFor(() => expect(saves(onCall)).toHaveLength(2));
    expect(saves(onCall)[1]).toMatchObject({ baseVersion: 5, message: "Removed Orders per bucket" });
  });

  it("shows a release dashboard's panel spec read-only, with no edit", async () => {
    const user = userEvent.setup();
    renderDashboard({ kind: "release", projectId: FIXTURE_PROJECT, key: "checkout" });
    await user.click(await screen.findByTestId("dashboard-panel-menu-orders"));
    expect(screen.queryByTestId("dashboard-panel-edit-orders")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("dashboard-panel-view-orders"));
    expect(screen.getByTestId("panel-drawer")).toHaveAttribute("data-mode", "view");
    expect(screen.getByTestId("panel-spec-json")).toHaveAttribute("readonly");
    expect(screen.queryByTestId("panel-drawer-save")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboard-edit-layout")).not.toBeInTheDocument();
  });
});
