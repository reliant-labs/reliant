// Copyright (c) 2026 Reliant Labs

/**
 * The six ML pages on the shop fixture pack (fixtures/shop, the demo shell's
 * own data), through the REAL client: MlRoute's gate reads the pack's
 * entitlement (ml enabled), and each page reads MlEarlyAccessService over the
 * pack's transport (a TransportScope), mapped by wire.ts. So the demo is the
 * pages' regression baseline.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import { createShopTransport } from "@/fixtures/shop";
import {
  ML_BAD_DEVICE,
  ML_BAD_NODE,
  ML_H100_POOL,
  ML_INFERENCE_SERVICE,
  ML_MODEL_NAME,
  ML_RELEASE_SUMMARY,
  ML_RUN_IDS,
  ML_TRAIN_CLUSTER,
  ML_VLLM_RUNTIME,
} from "@/fixtures/shop/model/ml";
import { DEMO_ORG } from "@/fixtures/shop/shopStory";
import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";

import type { MlView } from "../../Console/paths";

vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => DEMO_ORG }));
vi.mock("../../Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart" aria-label={label} /> }));

import { fixLabel } from "../MlRecommendationsPage";
import { MlGatedView } from "../MlRoute";
import { PLANNED_KCL_LABEL } from "../MlStackPage";

function renderView(view: MlView) {
  const scope = createTransportScope({ id: `ml-${view}`, transport: createShopTransport(), queryClient: createAppQueryClient(), readOnly: true });
  scope.queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <TransportScopeProvider scope={scope}>
      <MlGatedView view={view} app="" onApp={() => {}} />
    </TransportScopeProvider>,
  );
}

describe("the ML pages on the shop pack", () => {
  it("Machines: each pool, its chart, and the faulty node's evidence", async () => {
    renderView("machines");
    const pool = await screen.findByTestId(`ml-pool-${ML_TRAIN_CLUSTER}-${ML_H100_POOL}`);
    expect(within(pool).getByTestId("chart")).toHaveAttribute("aria-label", expect.stringContaining(ML_H100_POOL));
    const bad = within(pool).getByTestId(`ml-node-${ML_BAD_NODE}`);
    expect(bad).toHaveAttribute("data-health", "bad");
    expect(bad).toHaveTextContent(`Xid 79 on ${ML_BAD_DEVICE}`);
    expect(screen.queryByTestId("ml-access-page")).toBeNull();
  });

  it("Stack: components by cluster, with state and the KCL labelled planned", async () => {
    renderView("stack");
    const cluster = await screen.findByTestId(`ml-stack-${ML_TRAIN_CLUSTER}`);
    expect(within(cluster).getByTestId("ml-component-dcgm-exporter")).toHaveAttribute("data-state", "adopted");
    expect(within(cluster).getByTestId("ml-component-jobset")).toHaveAttribute("data-state", "installed");
    const kcl = within(cluster).getByTestId("ml-component-kcl-dcgm-exporter");
    expect(kcl).toHaveTextContent("forge.Adopt");
    expect(kcl.querySelector("figcaption")).toHaveTextContent(PLANNED_KCL_LABEL);
    expect(PLANNED_KCL_LABEL).toMatch(/^Planned: .*not released yet/);
  });

  it("Recommendations: worst first, with the fix and a pod-start breakdown", async () => {
    renderView("recommendations");
    const list = await screen.findByTestId("ml-findings");
    const severities = [...list.querySelectorAll("[data-severity]")].map((el) => el.getAttribute("data-severity"));
    expect(severities[0]).toBe("critical");
    expect(severities).toEqual([...severities].sort((a, b) => ["critical", "warning", "info", "unknown"].indexOf(a!) - ["critical", "warning", "info", "unknown"].indexOf(b!)));
    expect(list.querySelector('[data-testid^="ml-finding-podstart-"]')).not.toBeNull();
    expect(list.querySelector('[data-testid^="ml-finding-command-"]')).not.toBeNull();
    // Each written fix is labelled by what it is: none is forge KCL (forge declares no ML resources yet).
    const caption = (id: string) => screen.getByTestId(`ml-finding-kcl-${id}`).querySelector("figcaption")?.textContent;
    expect(caption("mlf-pod-start-slow-support-assistant")).toBe("The fix, as a Kubernetes manifest");
    expect(caption("mlf-shm-too-small-support-assistant-sft")).toBe("The fix, as a Kubernetes patch");
    expect(caption("mlf-kubelet-tracing-off-serve")).toBe("The fix, in the kubelet config");
    expect(list).not.toHaveTextContent("The fix, in KCL");
    expect(list.textContent).not.toMatch(/forge\.[A-Z]/);
  });

  it("labels a fix by its content", () => {
    expect(fixLabel("apiVersion: apps/v1\nkind: DaemonSet\nspec: {}")).toBe("The fix, as a Kubernetes manifest");
    expect(fixLabel("apiVersion: kubelet.config.k8s.io/v1beta1\nkind: KubeletConfiguration")).toBe("The fix, in the kubelet config");
    expect(fixLabel("# a comment\nspec:\n  volumes: []")).toBe("The fix, as a Kubernetes patch");
    expect(fixLabel('forge.Something { name = "x" }')).toBe("The fix, in KCL");
  });

  it("Runs: each run with its state, labelled Early access", async () => {
    renderView("runs");
    expect(await screen.findByTestId(`ml-run-${ML_RUN_IDS.running}`)).toHaveAttribute("data-state", "running");
    expect(screen.getByTestId(`ml-run-${ML_RUN_IDS.failed}`)).toHaveAttribute("data-state", "failed");
    expect(screen.getByTestId("ml-page-runs")).toHaveTextContent("Early access");
  });

  it("Models: versions by short digest", async () => {
    renderView("models");
    const model = await screen.findByTestId(`ml-model-${ML_MODEL_NAME}`);
    expect(within(model).getAllByText(/^sha256:[0-9a-f]{12}…$/).length).toBeGreaterThan(0);
  });

  it("Inference: the runtime, the verdict and the cold start", async () => {
    renderView("inference");
    const service = await screen.findByTestId(`ml-service-${ML_INFERENCE_SERVICE}`);
    expect(service).toHaveTextContent(ML_VLLM_RUNTIME);
    expect(within(service).getByTestId(`ml-service-release-${ML_INFERENCE_SERVICE}`)).toHaveTextContent(ML_RELEASE_SUMMARY);
    expect(within(service).getByTestId(`ml-service-coldstart-${ML_INFERENCE_SERVICE}`)).toBeInTheDocument();
  });
});
