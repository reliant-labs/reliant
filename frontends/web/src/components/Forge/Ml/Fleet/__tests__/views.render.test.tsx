// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";

/**
 * Fleet, Setup and Recommendations mounted through the real gate
 * (MlRoute MlGatedView) over the shop fixture pack: each page draws its
 * header numbers and its main evidence from the fixture world, and the
 * inbox's side panel opens on ?sel.
 */

import { cleanup, render, screen, within } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { createFixtureTransport } from "@/fixtures/transport";
import { shopAppPack } from "@/fixtures/shop/app";
import { SHOP_READ_ONLY_MESSAGE } from "@/fixtures/shop/index";
import { DEMO_ORG, demoNow, resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { BAD_GPU_INDEX, BAD_NODE, CLUSTERS, FINDINGS, gpuUuid, HOT_NODE } from "@/fixtures/shop/model/ml/world";
import type { MlView } from "@/components/Forge/Console/paths";

vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => DEMO_ORG }));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: () => <figure data-testid="chart" /> }));
// The other ML views are other areas' (and may be mid-build): only the gate and these three pages are under test.
vi.mock("@/components/Forge/Ml/Training/MlTrainingView", () => ({ MlTrainingView: () => null }));
vi.mock("@/components/Forge/Ml/Evals/MlEvalsView", () => ({ MlEvalsView: () => null }));
vi.mock("@/components/Forge/Ml/Models/MlModelsView", () => ({ MlModelsView: () => null }));
vi.mock("@/components/Forge/Ml/Inference/MlInferenceView", () => ({ MlInferenceView: () => null }));
vi.mock("@/components/Forge/Ml/Overview/MlOverviewView", () => ({ MlOverviewView: () => null }));
vi.mock("@/components/Forge/Ml/Queues/MlQueuesView", () => ({ MlQueuesView: () => null }));

import { MlGatedView } from "@/components/Forge/Ml/MlRoute";

function mount(view: MlView, sub: string[] = [], search = "") {
  const transport = createFixtureTransport({ pack: shopAppPack({ now: demoNow() }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
  const scope = createTransportScope({ id: `ml-${view}`, transport, queryClient: createAppQueryClient(), readOnly: true });
  scope.queryClient.setDefaultOptions({ queries: { retry: false } });
  const root = createRootRoute();
  const route = createRoute({ getParentRoute: () => root, path: "$", validateSearch: (s: Record<string, unknown>) => s, component: () => <MlGatedView view={view} sub={sub} /> });
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: [`/x${search}`] }) });
  render(
    <TransportScopeProvider scope={scope}>
      <RouterProvider router={router} />
    </TransportScopeProvider>,
  );
}

beforeEach(() => resetDemoNowForTests());
afterEach(() => {
  cleanup();
  resetDemoNowForTests();
});

describe("ML Fleet, Setup and Recommendations pages", () => {
  it("fleet overview: KPIs, pools and the unhealthy list", async () => {
    mount("fleet");
    const kpis = await screen.findByTestId("ml-fleet-kpis");
    expect(kpis).toHaveTextContent("104");
    expect(await screen.findByTestId("ml-fleet-pools-table")).toHaveTextContent("h100-train");
    expect(await screen.findByTestId("ml-fleet-unhealthy")).toHaveTextContent(`${BAD_NODE} GPU ${BAD_GPU_INDEX}`);
  });

  it("node page: 8 devices, NVLink matrix and conditions", async () => {
    mount("fleet", [CLUSTERS.train.id, HOT_NODE]);
    const table = await screen.findByTestId("ml-node-devices-table");
    expect(within(table).getAllByRole("row")).toHaveLength(9);
    expect(screen.getByTestId("ml-node-nvlink-matrix")).toBeInTheDocument();
    expect(screen.getByTestId("ml-node-conditions")).toHaveTextContent("GPUThermalThrottle");
  });

  it("GPU page: every field group with DCGM sources; the faulty GPU reads not collected", async () => {
    mount("fleet", ["gpus", gpuUuid(BAD_NODE, BAD_GPU_INDEX)]);
    const fields = await screen.findByTestId("ml-gpu-fields");
    expect(fields).toHaveTextContent("DCGM_FI_DEV_XID_ERRORS");
    expect(screen.getByTestId("ml-field-gpu.util")).toHaveTextContent("not collected");
  });

  it("setup: a checklist per cluster", async () => {
    mount("setup");
    expect(await screen.findByTestId(`ml-setup-coverage-${CLUSTERS.serve.id}`)).toHaveTextContent("Profiling counters");
    expect(screen.getByTestId(`ml-setup-discover-${CLUSTERS.train.id}`)).toHaveTextContent(`reliant forge ml discover --cluster ${CLUSTERS.train.id}`);
  });

  it("recommendations: grouped inbox, coverage folded, side panel on ?sel", async () => {
    mount("recommendations", [], `?sel=${FINDINGS.idleNotebook}`);
    expect(await screen.findByTestId("ml-recs-table")).toBeInTheDocument();
    expect(screen.getByTestId("ml-recs-coverage-row")).toHaveTextContent("Telemetry coverage");
    expect(screen.queryByTestId(`ml-recs-row-${FINDINGS.dcgmProfilingOff}`)).toBeNull();
    const panel = await screen.findByTestId("ml-finding-panel");
    expect(panel).toHaveTextContent("Why it matters");
    expect(within(panel).getByTestId("ml-finding-diff")).toBeInTheDocument();
  });
});
