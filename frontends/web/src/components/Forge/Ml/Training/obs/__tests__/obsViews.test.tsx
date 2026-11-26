// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";

/**
 * The run's observability tabs and the run comparison, mounted over the shop
 * fixture pack with the scope a run page builds (runScopeOf over GetMlRun):
 * each reads ObservabilityService, which the pack answers from
 * trainingTelemetry.ts (model/ml/telemetry.ts).
 */

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { createFixtureTransport } from "@/fixtures/transport";
import { shopAppPack } from "@/fixtures/shop/app";
import { SHOP_READ_ONLY_MESSAGE } from "@/fixtures/shop/index";
import { DEMO_ORG, demoNow, resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { RUNS, TRACES } from "@/fixtures/shop/model/ml/world";

vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => DEMO_ORG }));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart" aria-label={label} /> }));

import { MlRead, MlGateContext } from "@/components/Forge/Ml/MlParts";
import { useMl } from "@/components/Forge/Ml/kit";
import { runScopeOf, type RunScope } from "../../runScope";
import { LogsTab } from "../../tabs/LogsTab";
import { MetricsTab } from "../../tabs/MetricsTab";
import { ProfilesTab } from "../../tabs/ProfilesTab";
import { SystemTab } from "../../tabs/SystemTab";
import { TracesTab } from "../../tabs/TracesTab";
import { RunCompare } from "../../compare/RunCompare";

type Tab = (p: { scope: RunScope }) => ReactNode;

function Scoped({ tab: TabView, phase, attempt, rank }: { tab: Tab; phase?: string; attempt?: number; rank?: number }) {
  const run = useMl("getMlRun", { run: RUNS.v3.name });
  return <MlRead query={run} what="the run">{(d) => <TabView scope={runScopeOf(d, phase, attempt, rank)} />}</MlRead>;
}

function mount(body: () => ReactNode, search = "") {
  const transport = createFixtureTransport({ pack: shopAppPack({ now: demoNow() }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
  const scope = createTransportScope({ id: "ml-training-obs", transport, queryClient: createAppQueryClient(), readOnly: true });
  scope.queryClient.setDefaultOptions({ queries: { retry: false } });
  const root = createRootRoute();
  const route = createRoute({
    getParentRoute: () => root,
    path: "$",
    validateSearch: (s: Record<string, unknown>) => s,
    component: () => <MlGateContext.Provider value={{ orgId: DEMO_ORG, reportNotEntitled: () => {} }}>{body()}</MlGateContext.Provider>,
  });
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: [`/x${search}`] }) });
  render(
    <TransportScopeProvider scope={scope}>
      <RouterProvider router={router} />
    </TransportScopeProvider>,
  );
}

beforeAll(() => {
  // speedscope's frame hands its profile over as a blob URL.
  URL.createObjectURL ??= () => "blob:test";
  URL.revokeObjectURL ??= () => {};
});
beforeEach(() => resetDemoNowForTests());
afterEach(() => {
  cleanup();
  resetDemoNowForTests();
});

describe("training observability tabs", () => {
  it("metrics: sections of tiles, not-applicable families named, search filters", async () => {
    mount(() => <Scoped tab={MetricsTab} phase="sft" />);
    expect(await screen.findByTestId("ml-metric-train/loss")).toBeInTheDocument();
    expect(screen.getByTestId("ml-metrics-legend")).toHaveTextContent("attempt 3");
    expect(screen.getByTestId("ml-metrics-section-moe-absent")).toHaveTextContent("Dense model");
    expect(screen.getByTestId("ml-metric-eval/gsm8k")).toBeInTheDocument();
  });

  it("metrics: on the step axis by default, the redone steps shaded; ?axis=time plots wall time", async () => {
    mount(() => <Scoped tab={MetricsTab} phase="sft" />);
    expect(await screen.findByTestId("ml-metric-train/loss")).toBeInTheDocument();
    expect(screen.getByTestId("ml-metrics-axis-step")).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId("ml-metrics-legend")).toHaveTextContent("steps redone after a failure");
    cleanup();
    mount(() => <Scoped tab={MetricsTab} phase="sft" />, "?axis=time");
    expect(await screen.findByTestId("ml-metric-train/loss")).toBeInTheDocument();
    expect(screen.getByTestId("ml-metrics-axis-time")).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId("ml-metrics-legend")).not.toHaveTextContent("steps redone");
  });

  it("metrics: ?q keeps only matching tiles", async () => {
    mount(() => <Scoped tab={MetricsTab} phase="sft" />, "?q=mfu");
    expect(await screen.findByTestId("ml-metric-throughput/mfu")).toBeInTheDocument();
    expect(screen.queryByTestId("ml-metric-train/loss")).toBeNull();
  });

  it("system: straggler, heatmap of 64 ranks, ranks table", async () => {
    mount(() => <Scoped tab={SystemTab} phase="sft" />);
    expect(await screen.findByTestId("ml-system-kpis")).toHaveTextContent("rank 37");
    const grid = await screen.findByTestId("ml-system-heatmap-grid");
    expect(grid).toHaveTextContent("r37");
    expect(grid.querySelectorAll("[data-testid^=ml-system-heatmap-grid-r]")).toHaveLength(64);
    expect(await screen.findByTestId("ml-system-straggler")).toHaveTextContent("HW_SLOWDOWN");
    expect(await screen.findByTestId("ml-system-histogram")).toBeInTheDocument();
    expect(screen.getByTestId("ml-system-ranks")).toHaveTextContent("64 ranks");
  });

  it("logs: Observability's logs explorer over the phase's pods", async () => {
    mount(() => <Scoped tab={LogsTab} phase="sft" />);
    expect(await screen.findByTestId("obs-logs-table")).toBeInTheDocument();
    expect(screen.getByTestId("obs-logs")).toBeInTheDocument();
  });

  it("traces: Observability's trace explorer lists the attempt's slow steps and opens one", async () => {
    mount(() => <Scoped tab={TracesTab} phase="sft" attempt={3} />);
    // The list is virtualized (no rows in jsdom); its summary counts the attempt's kept traces.
    expect(await screen.findByText(/Plotting all \d+ matching traces/)).toBeInTheDocument();
    cleanup();
    mount(() => <Scoped tab={TracesTab} phase="sft" attempt={3} />, `?sel=${TRACES.slowStep}`);
    expect(await screen.findByTestId("trace-detail")).toBeInTheDocument();
  });

  it("profiles: the trainer's flamegraph from the attempt's captures", async () => {
    mount(() => <Scoped tab={ProfilesTab} phase="sft" attempt={3} />);
    expect(await screen.findByTestId("obs-speedscope")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-profiles-empty")).toBeNull();
  });
});

describe("run comparison", () => {
  it("summary, config diff, system diff and curves for three runs", async () => {
    mount(() => <RunCompare ids={[RUNS.v3.id, RUNS.v2.id, RUNS.v1.id]} />);
    const summary = await screen.findByTestId("ml-compare-summary-table");
    expect(summary).toHaveTextContent(RUNS.v2.name);
    expect(summary).toHaveTextContent("baseline");
    expect(screen.getByTestId("ml-compare-config-table")).toHaveTextContent("lr");
    expect(screen.getByTestId("ml-compare-system")).toHaveTextContent(RUNS.v1.name);
    expect(await screen.findByTestId("ml-compare-train/loss")).toBeInTheDocument();
    // Every run reports its step, so runs from different weeks line up by step.
    await waitFor(() => expect(screen.getByTestId("ml-compare-axis")).toHaveTextContent("step axis"));
  });
});
