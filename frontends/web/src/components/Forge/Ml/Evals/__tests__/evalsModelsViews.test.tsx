// Copyright (c) 2026 Reliant Labs

/**
 * The Evals and Models pages mounted on the shop fixture pack through the
 * real gate (MlGatedView) and client: the matrix with its gate failures, an
 * eval run's samples drill with a sample open, the registry, a model with
 * its scorecard and promotion, a version page, and the lineage graph. The
 * router's search and navigate are stubbed (the URL is the page's state).
 */

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { createFixtureTransport } from "@/fixtures/transport";
import { shopAppPack } from "@/fixtures/shop/app";
import { SHOP_READ_ONLY_MESSAGE } from "@/fixtures/shop/index";
import { DEMO_ORG, demoNow, resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { EVAL_RUNS, MODELS, SERVICES, V3_CANDIDATE_CHECKPOINT, VERSIONS } from "@/fixtures/shop/model/ml/world";

const search = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));
vi.mock("@tanstack/react-router", async (orig) => ({ ...(await orig<typeof import("@tanstack/react-router")>()), useSearch: () => search.current, useNavigate: () => () => {} }));
vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => DEMO_ORG }));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart" aria-label={label} /> }));
// The other areas' views are not under test here (and are built in parallel): the route mounts these two alone.
vi.mock("@/components/Forge/Ml/Training/MlTrainingView", () => ({ MlTrainingView: () => null }));
vi.mock("@/components/Forge/Ml/Overview/MlOverviewView", () => ({ MlOverviewView: () => null }));
vi.mock("@/components/Forge/Ml/Inference/MlInferenceView", () => ({ MlInferenceView: () => null }));
vi.mock("@/components/Forge/Ml/Fleet/MlFleetView", () => ({ MlFleetView: () => null }));
vi.mock("@/components/Forge/Ml/Queues/MlQueuesView", () => ({ MlQueuesView: () => null }));
vi.mock("@/components/Forge/Ml/Recommendations/MlRecommendationsView", () => ({ MlRecommendationsView: () => null }));
vi.mock("@/components/Forge/Ml/Setup/MlSetupView", () => ({ MlSetupView: () => null }));

import { MlGatedView } from "@/components/Forge/Ml/MlRoute";

const NOW = Date.UTC(2026, 10, 26, 15, 30);

function show(view: "evals" | "models", sub: string[] = [], q: Record<string, unknown> = {}) {
  search.current = q;
  const transport = createFixtureTransport({ pack: shopAppPack({ now: NOW }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
  const scope = createTransportScope({ id: `ml-${view}-${sub.join("-")}`, transport, queryClient: createAppQueryClient(), readOnly: true });
  scope.queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <TransportScopeProvider scope={scope}>
      <MlGatedView view={view} sub={sub} />
    </TransportScopeProvider>,
  );
}

beforeEach(() => resetDemoNowForTests());
afterEach(() => {
  cleanup();
  resetDemoNowForTests();
});

describe("the Evals pages", () => {
  it("draws the matrix: the candidate's GSM8K regression fails the gate, and cells open their samples", async () => {
    show("evals");
    const matrix = await screen.findByTestId("ml-eval-matrix");
    const cell = within(matrix).getByTestId(`ml-eval-cell-${V3_CANDIDATE_CHECKPOINT}-gsm8k`);
    expect(cell).toHaveTextContent(/−1\.[12]/);
    expect(cell.className).toContain("border-danger-ink");
    expect(cell.getAttribute("href")).toContain(`/evals/runs/${EVAL_RUNS.v3ckpt}?task=gsm8k`);
    expect(within(matrix).getByTestId(`ml-eval-row-${VERSIONS.v21fp8.id}`)).toHaveTextContent("fail");
    expect(screen.getByTestId("ml-evals-kpis")).toHaveTextContent("Gate fails");
    expect(await screen.findByTestId("ml-evals-step-gsm8k")).toBeInTheDocument();
    expect(screen.getByTestId("ml-evals-gates")).toHaveTextContent("safety_refusals ≥ 0.98");
  });

  it("opens an eval run's samples, and a sample side by side", async () => {
    show("evals", ["runs", EVAL_RUNS.v3ckpt], { task: "gsm8k", sel: "gsm8k-0057" });
    const header = await screen.findByTestId("ml-eval-samples-header");
    expect(header).toHaveTextContent("significant");
    expect(await screen.findByTestId("ml-eval-samples-table")).toBeInTheDocument();
    const panel = await screen.findByTestId("ml-eval-sample-panel");
    expect(panel).toHaveTextContent("warehouse");
    expect(panel).toHaveTextContent("regressed");
    expect(screen.getByTestId("ml-eval-run-results")).toHaveTextContent("GSM8K");
    expect(screen.getByTestId("ml-eval-run-kpis")).toHaveTextContent("Fails");
  });
});

describe("the Models pages", () => {
  it("lists the registry with the candidate from the matrix", async () => {
    show("models");
    const table = await screen.findByTestId("ml-models-table");
    const row = within(table).getByTestId(`ml-models-table-row-${MODELS.support.id}`);
    expect(row).toHaveTextContent("v2");
    expect(await within(row).findByText(/gate fail/)).toBeInTheDocument();
  });

  it("shows a model's versions, scorecard and blocked promotion", async () => {
    show("models", [MODELS.support.id]);
    expect(await screen.findByTestId("ml-model-versions-table")).toHaveTextContent("v2.1-fp8");
    expect(await screen.findByTestId("ml-model-scorecard-table")).toHaveTextContent("GSM8K");
    expect(screen.getByTestId("ml-model-promote-prod")).toBeDisabled();
    expect(await screen.findByTestId("ml-lineage")).toBeInTheDocument();
    expect(screen.getByTestId(`ml-lineage-node-${VERSIONS.v2.id}`)).toHaveTextContent("v2 · production");
  });

  it("shows a version's card, serving and gate", async () => {
    show("models", [MODELS.support.id, "v2.1-fp8"]);
    expect(await screen.findByTestId("ml-version-card")).toHaveTextContent("FP8 E4M3");
    expect(screen.getByTestId("ml-version-serving-table")).toHaveTextContent(SERVICES.prod.id);
    expect(screen.getByTestId("ml-version-gate-checks")).toHaveTextContent("safety_refusals ≥ 0.98");
    expect(screen.getByTestId("ml-version-kpis")).toHaveTextContent("10% of prod");
  });
});
