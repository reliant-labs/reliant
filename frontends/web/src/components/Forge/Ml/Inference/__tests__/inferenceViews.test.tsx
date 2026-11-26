// Copyright (c) 2026 Reliant Labs

/**
 * The Inference and Queues pages mounted on the shop fixture pack through the
 * real gate (MlGatedView) and client: the list, a service's tabs, a request
 * trace, and the queues page with its pending reasons. The router's search
 * and navigate are stubbed (the URL is the page's state).
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
import { REVISIONS, SERVICES, TRACES } from "@/fixtures/shop/model/ml/world";

const search = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));
vi.mock("@tanstack/react-router", async (orig) => ({ ...(await orig<typeof import("@tanstack/react-router")>()), useSearch: () => search.current, useNavigate: () => () => {} }));
vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => DEMO_ORG }));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart" aria-label={label} /> }));
vi.mock("@/components/Forge/Observability/waterfall/TraceWaterfall", () => ({ TraceWaterfall: ({ spans }: { spans: { name: string }[] }) => <ol data-testid="waterfall">{spans.map((s, i) => <li key={i}>{s.name}</li>)}</ol> }));

import { MlGatedView } from "@/components/Forge/Ml/MlRoute";

const NOW = Date.UTC(2026, 10, 26, 15, 30);

function show(view: "inference" | "queues", sub: string[] = [], q: Record<string, unknown> = {}) {
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

describe("the Inference pages", () => {
  it("lists the services with links to each", async () => {
    show("inference");
    const table = await screen.findByTestId("ml-inference-services-table");
    expect(within(table).getByTestId(`ml-inference-services-table-row-${SERVICES.prod.id}`)).toHaveTextContent("A/B");
    expect(within(table).getByTestId(`ml-inference-services-table-row-${SERVICES.reranker.id}`)).toHaveTextContent("n/a");
    expect(screen.getByTestId("ml-kpis")).toHaveTextContent("GPUs serving");
  });

  it("opens a service on its overview with findings and the latency charts", async () => {
    show("inference", [SERVICES.prod.id]);
    expect(await screen.findByTestId("ml-inference-tabs")).toBeInTheDocument();
    expect(screen.getByTestId("ml-kpis")).toHaveTextContent("TTFT p95 · 24h");
    expect(screen.getByTestId("ml-kpis")).toHaveTextContent("+4.1%");
    expect(screen.getByTestId("ml-inference-findings")).toHaveTextContent("f-kv-cache-saturation-prod");
    expect(await screen.findByRole("figure", { name: "Time to first token" })).toBeInTheDocument();
  });

  it("shows the Changes comparison on the revisions tab", async () => {
    show("inference", [SERVICES.prod.id], { tab: "revisions" });
    const changes = await screen.findByTestId("ml-inference-changes");
    expect(changes).toHaveTextContent("support-assistant-fp8");
    expect(within(changes).getByTestId("ml-inference-arm-metrics")).toHaveTextContent("Guardrail trigger rate");
    expect(screen.getByTestId(`ml-inference-revisions-row-${REVISIONS.prodCurrent}`)).toHaveAttribute("aria-selected", "true");
  });

  it("lists a service's request traces in Observability's explorer and opens one", async () => {
    show("inference", [SERVICES.prod.id], { tab: "traces" });
    expect(await screen.findByTestId("obs-trace-table")).toBeInTheDocument();
    cleanup();
    show("inference", [SERVICES.prod.id], { tab: "traces", sel: TRACES.slowRequest });
    expect(await screen.findByTestId("trace-detail")).toBeInTheDocument();
    expect(await screen.findByTestId("waterfall")).toHaveTextContent("epp.schedule");
  });

  it("draws a request trace on its own page", async () => {
    show("inference", ["traces", TRACES.slowRequest]);
    expect(await screen.findByTestId("waterfall")).toHaveTextContent("llm_request");
  });
});

describe("the Queues page", () => {
  it("shows the pending workloads, and the reason filter keeps one reason", async () => {
    show("queues", [], { filter: "untolerated-taint" });
    const pending = await screen.findByTestId("ml-queues-pending");
    expect(within(pending).getAllByRole("row")).toHaveLength(2);
    expect(pending).toHaveTextContent("jupyter-jon-a100-0");
    expect(screen.getByTestId("ml-kpis")).toHaveTextContent("50 / 7");
    expect(screen.getByTestId("ml-queues-preemptions")).toHaveTextContent("pretrain attempt 1");
  });

  it("explains why the DPO ablation waits", async () => {
    show("queues", [], { sel: "run_01JDDPOABLATE01/dpo/1" });
    expect(await screen.findByTestId("ml-queues-why")).toHaveTextContent("insufficient unused quota for nvidia.com/gpu in flavor h100, 32 more needed");
  });
});
