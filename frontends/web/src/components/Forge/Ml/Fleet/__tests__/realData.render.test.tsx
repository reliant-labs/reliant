// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";

/**
 * Fleet, Node, GPU and Setup over REAL answers: the protojson in ./recorded
 * (README.md there), recorded by control-plane's e2e/ml_fleet through its B1
 * handlers from a k3d cluster on fake-gpu-operator plus KWOK. Only utilisation and
 * framebuffer memory are collected, there is no price sheet, and node,
 * workload and NVLink detail is empty, so every unset number must read "not
 * collected" (never 0, NaN or "$0"). Also: no cluster reporting yet
 * (observed_at unset) is the connect-a-cluster state, and FEATURE_NOT_ENTITLED
 * is the access page.
 */

import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { cleanup, render, screen, within } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GetMlDeviceResponseSchema,
  GetMlFleetResponseSchema,
  GetMlNodeResponseSchema,
  GetMlStackResponseSchema,
  MlEarlyAccessService,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import type { MlView } from "@/components/Forge/Console/paths";

import fleetJson from "./recorded/get_ml_fleet.json";
import nodeJson from "./recorded/get_ml_node.json";
import deviceJson from "./recorded/get_ml_device.json";
import stackJson from "./recorded/get_ml_stack.json";

vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => "org-ml" }));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: () => <figure data-testid="chart" /> }));

import { MlGatedView } from "@/components/Forge/Ml/MlRoute";

const CLUSTER = "ml-fleet-e2e";
const BAD = "k3d-ml-fleet-e2e-agent-0";
const NODE = (nodeJson as { node: { name: string } }).node.name;
const BURN_GPU = (deviceJson as { device: { uuid: string } }).device.uuid;

type Answer = "recorded" | "empty" | "not-entitled" | "stale" | "scalars-disagree";

/** Synthetic variant of the real recording: the recorded fleet plus a second cluster that stopped reporting an hour before, with no GPU nodes. */
function staleFleet(): JsonValue {
  const f = structuredClone(fleetJson) as { clusters: Record<string, unknown>[] };
  f.clusters.push({ id: "edge-cpu", name: "edge-cpu", lastSeenAt: "2026-11-26T11:00:00Z", nodeCount: 3, gpuCount: 0, health: "ML_HEALTH_UNKNOWN" });
  return f as unknown as JsonValue;
}

/**
 * Synthetic variant of the real node: every GPU's headline scalars claim
 * 300 W of a 400 W limit and 80 of 80 GB, while its fields still say power
 * is NOT_COLLECTED and now framebuffer free is NOT_COLLECTED too. The page
 * must follow the fields.
 */
function scalarsDisagreeNode(): JsonValue {
  const n = structuredClone(nodeJson) as { node: { devices: Record<string, unknown>[] } };
  for (const d of n.node.devices) {
    Object.assign(d, { powerW: 300, powerLimitW: 400, memoryUsedGb: 80, memoryTotalGb: 80 });
    for (const f of d.fields as Record<string, unknown>[]) {
      if (f.key === "gpu.memory.free") {
        delete f.value;
        f.collection = "ML_COLLECTION_NOT_COLLECTED";
      }
    }
  }
  return n as unknown as JsonValue;
}

function transportFor(answer: Answer): Transport {
  const refuse = () => {
    throw new ConnectError("ml is not enabled", Code.PermissionDenied, new Headers({ "x-forge-error-reason": "FEATURE_NOT_ENTITLED" }));
  };
  const or =
    <T,>(recorded: () => T, empty: () => T) => () => (answer === "not-entitled" ? refuse() : answer === "empty" ? empty() : recorded());
  return createRouterTransport(({ service }) => {
    service(OrganizationService, {
      getMyOrgEntitlements: () => ({ features: [{ feature: "ml", enabled: true }] }),
    });
    service(MlEarlyAccessService, {
      getMlFleet: or(() => fromJson(GetMlFleetResponseSchema, answer === "stale" ? staleFleet() : (fleetJson as unknown as JsonValue)), () => ({})),
      getMlNode: or(() => fromJson(GetMlNodeResponseSchema, answer === "scalars-disagree" ? scalarsDisagreeNode() : (nodeJson as unknown as JsonValue)), () => ({})),
      getMlDevice: or(() => fromJson(GetMlDeviceResponseSchema, deviceJson as unknown as JsonValue), () => ({})),
      getMlStack: or(() => fromJson(GetMlStackResponseSchema, stackJson as unknown as JsonValue), () => ({})),
    });
  });
}

function mount(view: MlView, sub: string[] = [], answer: Answer = "recorded") {
  const scope = createTransportScope({ id: `ml-real-${view}`, transport: transportFor(answer), queryClient: createAppQueryClient(), readOnly: true });
  scope.queryClient.setDefaultOptions({ queries: { retry: false } });
  const root = createRootRoute();
  const route = createRoute({ getParentRoute: () => root, path: "$", validateSearch: (s: Record<string, unknown>) => s, component: () => <MlGatedView view={view} sub={sub} /> });
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: ["/x"] }) });
  render(
    <TransportScopeProvider scope={scope}>
      <RouterProvider router={router} />
    </TransportScopeProvider>,
  );
}

afterEach(() => cleanup());

const noNumbersInvented = (el: HTMLElement) => {
  expect(el.textContent).not.toMatch(/NaN|\$0\b|\$—/);
};

describe("ML Fleet and Setup on real cluster data", () => {
  it("fleet: the cluster, 32 GPUs, both pools, the unhealthy node and its reason; unpriced cost explained", async () => {
    mount("fleet");
    const kpis = await screen.findByTestId("ml-fleet-kpis");
    expect(kpis).toHaveTextContent("32");
    expect(kpis).toHaveTextContent("1 cluster · 1 pool");
    expect(kpis).toHaveTextContent("no price sheet");
    const pools = screen.getByTestId("ml-fleet-pools-table");
    expect(pools).toHaveTextContent("a100");
    expect(pools).toHaveTextContent("A100-SXM4-80GB");
    expect(pools).toHaveTextContent(CLUSTER);
    expect(within(pools).getAllByText("not collected").length).toBeGreaterThan(0);
    const unhealthy = screen.getByTestId("ml-fleet-unhealthy");
    expect(unhealthy).toHaveTextContent(BAD);
    expect(unhealthy).toHaveTextContent("GpuXidError: Xid79");
    noNumbersInvented(screen.getByTestId("ml-page-fleet"));
  });

  it("node: 8 devices, power, temperature and Xid not collected, its conditions listed", async () => {
    mount("fleet", [CLUSTER, NODE]);
    const table = await screen.findByTestId("ml-node-devices-table");
    expect(within(table).getAllByRole("row")).toHaveLength(9);
    expect(within(table).getAllByText("not collected").length).toBeGreaterThanOrEqual(8 * 3);
    expect(screen.getByTestId("ml-node-conditions")).toHaveTextContent("KubeletReady");
    expect(within(table).getByText("gpu-train")).toBeInTheDocument();
    expect(screen.getByTestId("ml-node-kpis")).toHaveTextContent("not collected");
    noNumbersInvented(screen.getByTestId("ml-node-kpis"));
  });

  it("node headlines follow the fields: power not collected, memory from framebuffer used + free", async () => {
    // The recording: GPU 0 has 81920 MiB used and 0 free; GPUs 1-7 the reverse. powerW is NaN and gpu.power NOT_COLLECTED.
    mount("fleet", [CLUSTER, NODE]);
    noNumbersInvented(await screen.findByTestId("ml-node-kpis"));
    const power = screen.getByTestId("ml-node-kpi-power");
    expect(power).toHaveTextContent("not collected");
    expect(power).not.toHaveTextContent(/\d+ W/);
    const memory = screen.getByTestId("ml-node-kpi-memory");
    expect(memory).toHaveTextContent("80.0 / 640 GB");
    const rows = within(screen.getByTestId("ml-node-devices-table")).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("80.0 / 80.0 GB");
    expect(rows[2]).toHaveTextContent("0.0 / 80.0 GB");
  });

  it("node: scalars that claim power and memory never stand in for uncollected fields", async () => {
    mount("fleet", [CLUSTER, NODE], "scalars-disagree");
    const kpis = await screen.findByTestId("ml-node-kpis");
    const power = screen.getByTestId("ml-node-kpi-power");
    expect(power).toHaveTextContent("not collected");
    expect(power).not.toHaveTextContent("2,400 W");
    // Used is collected, free is not: used alone, and which half is missing.
    const memory = screen.getByTestId("ml-node-kpi-memory");
    expect(memory).toHaveTextContent("80.0 GB used");
    expect(memory).toHaveTextContent("framebuffer free not collected on 8 of 8 GPUs");
    expect(memory).not.toHaveTextContent("640");
    const table = screen.getByTestId("ml-node-devices-table");
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("80.0 GB used · free not collected");
    expect(table).not.toHaveTextContent("300 / 400 W");
    expect(table).not.toHaveTextContent("80.0 / 80");
    noNumbersInvented(kpis);
  });

  it("GPU: utilisation and memory collected; power, temperature and Xid not collected", async () => {
    // The fake exporter draws utilisation from the pod's 60-80 annotation, so
    // each re-recording carries a different value: read it from the recording.
    const util = `${Math.round((deviceJson as { device: { busyRatio: number } }).device.busyRatio * 100)}%`;
    mount("fleet", ["gpus", BURN_GPU]);
    await screen.findByTestId("ml-gpu-fields");
    expect(screen.getByTestId("ml-field-gpu.util")).toHaveTextContent(util);
    expect(screen.getByTestId("ml-field-gpu.memory.used")).toHaveTextContent("80.0 GiB");
    for (const key of ["gpu.power", "gpu.temperature", "gpu.xid.last"]) expect(screen.getByTestId(`ml-field-${key}`)).toHaveTextContent("not collected");
    const kpis = screen.getByTestId("ml-gpu-kpis");
    expect(kpis).toHaveTextContent(util);
    expect(within(kpis).getAllByText(/not collected/).length).toBeGreaterThanOrEqual(3);
    expect(screen.getByTestId("ml-gpu-identity")).toHaveTextContent("gpu-train");
    noNumbersInvented(kpis);
  });

  it("setup: one coverage row per field group; a running exporter's missing fields read not collected", async () => {
    mount("setup");
    const coverage = await screen.findByTestId(`ml-setup-coverage-${CLUSTER}`);
    expect(within(coverage).getAllByRole("row")).toHaveLength(12);
    expect(coverage).toHaveTextContent("Profiling counters");
    expect(coverage).toHaveTextContent("not collected: no device reports DCGM_FI_DEV_XID_ERRORS");
    expect(within(coverage).getAllByText("Not collected")).toHaveLength(6);
    expect(within(coverage).getAllByText("Adopted")).toHaveLength(5);
    expect(screen.getByTestId(`ml-setup-discover-${CLUSTER}`)).toHaveTextContent("forge ml discover");
    noNumbersInvented(screen.getByTestId("ml-page-setup"));
  });

  it("a stale cluster is badged with when it last reported; a cluster with no GPUs says so", async () => {
    mount("fleet", [], "stale");
    const edge = await screen.findByTestId("ml-fleet-cluster-edge-cpu");
    expect(edge).toHaveTextContent("stale · last seen 1h ago");
    expect(edge).toHaveTextContent("3 nodes · no GPUs found");
    expect(screen.queryByTestId(`ml-fleet-cluster-${CLUSTER}`)).toBeNull();
    expect(screen.getByTestId("ml-fleet-kpis")).toHaveTextContent("2 clusters · 1 pool");
  });

  it("no cluster reporting yet (observed_at unset): connect a cluster, never zeros", async () => {
    mount("fleet", [], "empty");
    expect(await screen.findByTestId("ml-no-cluster")).toHaveTextContent("No cluster is reporting yet");
    expect(screen.queryByTestId("ml-fleet-kpis")).toBeNull();
  });

  it("setup with no cluster: connect a cluster", async () => {
    mount("setup", [], "empty");
    expect(await screen.findByTestId("ml-no-cluster")).toBeInTheDocument();
  });

  it("FEATURE_NOT_ENTITLED: the access page", async () => {
    mount("fleet", [], "not-entitled");
    expect(await screen.findByTestId("ml-access-page")).toBeInTheDocument();
  });
});
