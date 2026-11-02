// Copyright (c) 2025 Reliant Labs

/**
 * One panel's bad definition stops at its own cell.
 *
 *   - the boundary: a panel that throws (while its props are built, or while
 *     it renders) shows "This panel couldn't be drawn" with its definition,
 *     Remove and Try again, and its siblings keep rendering;
 *   - the dashboard: forge's generated "Kubernetes workloads" dashboard, read
 *     raw from forge-dashboard-spec.json (search panels with no `series`), and a
 *     panel injected with no `series` at all, render without blanking the app.
 */

import { useState, type ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { Panel } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { countsErrors } from "../drill";
import { DashboardView, type DashboardHrefs } from "../DashboardView";
import { PanelErrorBoundary } from "../PanelErrorBoundary";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() } }));

const panel = (id: string, extra: Partial<Panel> = {}): Panel => ({ id, title: `Panel ${id}`, kind: "line", signal: "traces", series: [], ...extra }) as Panel;

function Boom({ when }: { when: boolean }): ReactElement {
  if (when) throw new TypeError("Cannot read properties of undefined (reading 'length')");
  return <p>drawn</p>;
}

describe("PanelErrorBoundary", () => {
  it("contains a throw to its panel, shows the definition, and leaves siblings drawn", () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    render(
      <div>
        <PanelErrorBoundary panel={panel("bad", { title: "Restarts" })}>{() => <Boom when />}</PanelErrorBoundary>
        <PanelErrorBoundary panel={panel("good")}>{() => <Boom when={false} />}</PanelErrorBoundary>
      </div>
    );
    const failed = screen.getByTestId("dashboard-panel-failed-bad");
    expect(within(failed).getByRole("alert")).toHaveTextContent("Restarts");
    expect(failed).toHaveTextContent("This panel couldn't be drawn");
    expect(within(failed).getByTestId("dashboard-panel-failed-spec-bad")).toHaveTextContent('"id": "bad"');
    // No Remove without a way to remove.
    expect(within(failed).queryByTestId("dashboard-panel-failed-remove-bad")).not.toBeInTheDocument();
    expect(screen.getByText("drawn")).toBeInTheDocument();
  });

  it("catches a throw while the panel's props are built, not only while it renders", () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const broken = { id: "raw", title: "Raw" } as unknown as Panel;
    render(
      <PanelErrorBoundary panel={broken}>
        {() => {
          // What the grid does for a menu href: read the definition before rendering.
          const n = (broken.series as unknown as unknown[]).length;
          return <p>{n}</p>;
        }}
      </PanelErrorBoundary>
    );
    expect(screen.getByTestId("dashboard-panel-failed-raw")).toBeInTheDocument();
  });

  it("removes and retries", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const user = userEvent.setup();
    const onRemove = vi.fn();
    // The same definition across renders: a new one would earn a fresh try on its own.
    const definition = panel("p");
    function Harness() {
      const [broken, setBroken] = useState(true);
      return (
        <>
          <button type="button" onClick={() => setBroken(false)}>
            fix it
          </button>
          <PanelErrorBoundary panel={definition} onRemove={onRemove}>
            {() => <Boom when={broken} />}
          </PanelErrorBoundary>
        </>
      );
    }
    render(<Harness />);
    await user.click(screen.getByTestId("dashboard-panel-failed-remove-p"));
    expect(onRemove).toHaveBeenCalledTimes(1);
    await user.click(screen.getByText("fix it"));
    await user.click(screen.getByTestId("dashboard-panel-retry-p"));
    expect(screen.getByText("drawn")).toBeInTheDocument();
  });
});

describe("countsErrors", () => {
  it("reads a definition with no series as counting nothing", () => {
    expect(countsErrors({ id: "x", kind: "search", signal: "logs" } as unknown as Panel)).toBe(false);
  });
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

const ref = (key: string): DashboardRef => ({ kind: "release", projectId: FIXTURE_PROJECT, key });

function renderDashboard(dashRef: DashboardRef, mutate?: (panels: Panel[]) => Panel[]) {
  const base = createFixtureApi();
  const api = mutate
    ? {
        ...base,
        getDashboard: async (req: Parameters<typeof base.getDashboard>[0]) => {
          const doc = await base.getDashboard(req);
          return { ...doc, spec: { ...doc.spec, panels: mutate(doc.spec.panels) } };
        },
      }
    : base;
  renderWithApi(
    api,
    <DashboardView project={FIXTURE_PROJECT} dashRef={dashRef} env="prod" environments={FIXTURE_ENVS} search={{}} onSearch={vi.fn()} onEnvChange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />
  );
}

describe("a dashboard with a panel it cannot read", () => {
  it("draws forge's generated workloads dashboard, read raw, without blanking", async () => {
    renderDashboard(ref("workloads"));
    const grid = await screen.findByTestId("dashboard-grid");
    expect(within(grid).getByTestId("dashboard-panel-cpu-usage")).toBeInTheDocument();
    // Its search panels carry no `series`; they still have a cell of their own.
    expect(within(grid).queryByTestId("dashboard-panel-events") ?? within(grid).getByTestId("dashboard-panel-failed-events")).toBeInTheDocument();
  });

  it("keeps every other panel when one has no series at all", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    renderDashboard(ref("workloads"), (panels) => [...panels, { id: "no-series", title: "No series", kind: "line", signal: "metrics" } as unknown as Panel]);
    const grid = await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(within(grid).getByTestId("dashboard-panel-cpu-usage")).toBeInTheDocument());
    expect(within(grid).queryByTestId("dashboard-panel-no-series") ?? within(grid).getByTestId("dashboard-panel-failed-no-series")).toBeInTheDocument();
  });
});
