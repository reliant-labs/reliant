// Copyright (c) 2025 Reliant Labs

/**
 * The grid builds each panel's props — its menu's Explore hrefs
 * (drillSearches, viewerQuery) — inside the panel's PanelErrorBoundary, not in
 * DashboardView's own render. So a definition that throws while those props
 * are BUILT, not only one that throws while its card renders, stops at its own
 * cell. drillSearches is made to throw for one panel here: the throw has to
 * happen on DashboardView's real path, where a boundary that only wrapped an
 * already-built element would let it blank the dashboard.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { Panel } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardView, type DashboardHrefs } from "../DashboardView";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() } }));
vi.mock("../drill", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../drill")>();
  return {
    ...actual,
    drillSearches: (...args: Parameters<typeof actual.drillSearches>) => {
      if (args[0].id === "explodes-in-props") throw new TypeError("Cannot read properties of undefined (reading 'length')");
      return actual.drillSearches(...args);
    },
  };
});

const hrefs: DashboardHrefs = {
  logs: () => "/logs",
  traces: () => "/traces",
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};

const ref: DashboardRef = { kind: "release", projectId: FIXTURE_PROJECT, key: "workloads" };

describe("a panel whose props throw while the grid builds them", () => {
  it("fails in its own cell and leaves the rest of the dashboard drawn", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const base = createFixtureApi();
    const api = {
      ...base,
      getDashboard: async (req: Parameters<typeof base.getDashboard>[0]) => {
        const doc = await base.getDashboard(req);
        const extra = { id: "explodes-in-props", title: "Explodes", kind: "line", signal: "traces", series: [] } as unknown as Panel;
        return { ...doc, spec: { ...doc.spec, panels: [...doc.spec.panels, extra] } };
      },
    };
    renderWithApi(
      api,
      <DashboardView project={FIXTURE_PROJECT} dashRef={ref} env="prod" environments={FIXTURE_ENVS} search={{}} onSearch={vi.fn()} onEnvChange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />
    );
    const grid = await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(within(grid).getByTestId("dashboard-panel-failed-explodes-in-props")).toBeInTheDocument());
    expect(within(grid).getByTestId("dashboard-panel-cpu-usage")).toBeInTheDocument();
  });
});
