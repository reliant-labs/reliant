// Copyright (c) 2025 Reliant Labs

/**
 * Hosted environments in the forge console: the destination badge, the hosted
 * row, and the hosted deploy confirmation.
 *
 * Three things are pinned, each against the specific way it could regress:
 *
 *   1. UNKNOWN IS NOT CLUSTER. A destination this build does not recognise —
 *      or one an older forge never sent — renders "Unknown", never "Cluster".
 *   2. A HOSTED ROW SHOWS ITS CONTROL PLANE, not a kube context: the endpoint
 *      host, each workload's URL, and the verdict per workload.
 *   3. A HOSTED DEPLOY CONFIRM HAS NO KUBE-CONTEXT LANGUAGE, and it keeps the
 *      token discipline: the operator types the control plane's host, and the
 *      token carries the endpoint the plan named.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { TopologyView } from "../TopologyView";
import { EnvironmentCard } from "../Environments/EnvironmentCard";
import { DestinationBadge } from "../DestinationBadge";
import { DeployConfirmStep } from "../Deploy/DeployConfirmStep";
import { TargetPanel } from "../Deploy/DeployPlanView";
import { prodPlan } from "../Deploy/__tests__/fixtures";
import type { ForgeTopologyEnv, ForgeTopologyReport } from "@/services/forge/topology";
import type { ForgeDeployReport } from "@/services/forge/deploy";
import { deployTokenFor } from "@/services/forge/deploy";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

function meta(): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.9.1",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
  } as ForgeReportMeta;
}

const HOSTED_ENV: ForgeTopologyEnv = {
  env: "cloud",
  declared: true,
  bound: true,
  release: "v2.0.0",
  destination: "hosted",
  endpoint: "https://api.reliantlabs.io",
  environment_id: "denv_01HZX",
  images: [{ image: "api", digest: "sha256:aaaa", state: "not_verified" }],
  workloads: [
    { name: "api", tier: "backend", url: "https://api-acme.apps.reliantlabs.io", verdict: "converged" },
    { name: "worker", tier: "backend", verdict: "converging" },
  ],
};

const CLUSTER_ENV: ForgeTopologyEnv = {
  env: "prod",
  declared: true,
  bound: true,
  release: "v2.0.0",
  destination: "cluster",
  kube_context: "gke_prod",
  namespace: "app-prod",
  images: [{ image: "api", digest: "sha256:aaaa", state: "not_verified" }],
};

function report(envs: ForgeTopologyEnv[]): ForgeTopologyReport {
  return { project: "acme", latest_release: "v2.0.0", images: ["api"], environments: envs };
}

function renderTopology(envs: ForgeTopologyEnv[]) {
  return render(
    <TopologyView
      outcome={{ kind: "report", meta: meta(), report: report(envs) }}
      isLoading={false}
      onVerify={vi.fn()}
      projectName="acme"
    />
  );
}

describe("DestinationBadge", () => {
  it.each([
    ["hosted", "Hosted"],
    ["cluster", "Cluster"],
    ["compose", "Compose"],
    ["host", "Host"],
    ["external", "External"],
    ["static", "Static"],
    ["mixed", "Mixed"],
  ])("labels %s as %s", (destination, label) => {
    render(<DestinationBadge env={{ env: "x", destination }} />);
    const badge = screen.getByTestId("destination-x");
    expect(badge.getAttribute("data-destination")).toBe(destination);
    expect(badge.textContent).toBe(label);
  });

  it("renders an unrecognised destination as Unknown — never as Cluster", () => {
    render(<DestinationBadge env={{ env: "x", destination: "moon-base" }} />);
    const badge = screen.getByTestId("destination-x");
    expect(badge.getAttribute("data-destination")).toBe("unknown");
    expect(badge.textContent).toBe("Unknown");
    expect(badge.textContent).not.toMatch(/cluster/i);
  });

  it("renders an ABSENT destination (an older forge) as Unknown too", () => {
    render(<DestinationBadge env={{ env: "x" }} />);
    expect(screen.getByTestId("destination-x").getAttribute("data-destination")).toBe("unknown");
  });

  it("reads ONLY `destination` — forge's redundant `hosted` flag decides nothing", () => {
    // Forge still emits `hosted: true`, derived from the same ControlPlane
    // declaration as destination. It is not a second input: a row with the
    // flag but no destination is unknown, and destination wins over it.
    const view = render(
      <>
        <DestinationBadge env={{ env: "a", hosted: true } as ForgeTopologyEnv} />
        <DestinationBadge env={{ env: "c", hosted: true, destination: "cluster" } as ForgeTopologyEnv} />
      </>
    );
    expect(view.getByTestId("destination-a").getAttribute("data-destination")).toBe("unknown");
    expect(view.getByTestId("destination-c").getAttribute("data-destination")).toBe("cluster");
  });

  it("gives hosted a different treatment from unknown and from cluster", () => {
    const view = render(
      <>
        <DestinationBadge env={{ env: "a", destination: "hosted" }} />
        <DestinationBadge env={{ env: "b", destination: "cluster" }} />
        <DestinationBadge env={{ env: "c", destination: "nope" }} />
      </>
    );
    const cls = (id: string) => view.getByTestId(id).querySelector("span > span")?.className ?? "";
    expect(cls("destination-a")).not.toBe(cls("destination-b"));
    expect(cls("destination-a")).not.toBe(cls("destination-c"));
    expect(cls("destination-b")).not.toBe(cls("destination-c"));
  });
});

describe("a hosted topology row", () => {
  it("shows the endpoint host, the env's health and a workload URL — and no kube context", () => {
    renderTopology([HOSTED_ENV, CLUSTER_ENV]);
    const row = screen.getByTestId("env-row-cloud");

    expect(within(row).getByTestId("destination-cloud").textContent).toBe("Hosted");
    expect(within(row).getByTestId("hosted-endpoint-cloud").textContent).toBe("api.reliantlabs.io");

    const link = within(row).getByTestId("hosted-url-cloud-api");
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href")).toBe("https://api-acme.apps.reliantlabs.io");

    // No env-level verdict: the worst workload (worker, converging) decides —
    // and converging is not converged.
    expect(within(row).getByTestId("hosted-verdict-cloud").getAttribute("data-verdict")).toBe("converging");
    expect(row.textContent).not.toMatch(/gke_|namespace/i);
  });

  it("lists every workload with its own verdict on the Environments card", () => {
    render(<EnvironmentCard env={HOSTED_ENV} active onSelect={vi.fn()} />);
    const card = screen.getByTestId("environment-card-cloud");
    const api = within(card).getByTestId("hosted-workload-cloud-api");
    expect(api.querySelector("[data-verdict]")?.getAttribute("data-verdict")).toBe("converged");
    // The name is shown beside the URL — the URL is never the only label.
    expect(api.textContent).toContain("api");
    const worker = within(card).getByTestId("hosted-workload-cloud-worker");
    expect(worker.querySelector("[data-verdict]")?.getAttribute("data-verdict")).toBe("converging");
    expect(within(card).getByTestId("hosted-env-id-cloud").textContent).toContain("denv_01HZX");
    // A real link, not inert text: the card is no longer one big <button>.
    expect(within(card).getByTestId("hosted-url-cloud-api").tagName).toBe("A");
    expect(within(card).getByTestId("hosted-url-cloud-api").closest("button")).toBeNull();
  });

  it("says an un-ensured hosted env has no id rather than showing an empty one", () => {
    renderTopology([{ ...HOSTED_ENV, environment_id: "" }]);
    expect(screen.getByTestId("hosted-env-id-cloud").textContent).toMatch(/not created/i);
  });

  it("keeps a cluster row's kube context and namespace", () => {
    renderTopology([HOSTED_ENV, CLUSTER_ENV]);
    const row = screen.getByTestId("env-row-prod");
    expect(within(row).getByTestId("destination-prod").textContent).toBe("Cluster");
    expect(row.textContent).toContain("gke_prod · app-prod");
    expect(within(row).queryByTestId("hosted-facts-prod")).toBeNull();
  });
});

function hostedPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return prodPlan({
    env: "cloud",
    guard: { declared_context: "https://api.reliantlabs.io", verdict: "allow", reason: "control_plane_declared" },
    target: {
      destination: "hosted",
      endpoint: "https://api.reliantlabs.io",
      environment_id: "denv_01HZX",
    },
    release: "v2.0.0",
    ...overrides,
  });
}

describe("the hosted deploy confirmation", () => {
  it("names the endpoint and environment id, and has no kube-context language", () => {
    render(<DeployConfirmStep plan={hostedPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    const confirm = screen.getByTestId("deploy-confirm");
    expect(confirm.textContent).toContain("https://api.reliantlabs.io");
    expect(confirm.textContent).toContain("denv_01HZX");
    expect(confirm.textContent).not.toMatch(/cluster|kube|context|manifests/i);
  });

  it("keeps the token discipline: acknowledge AND type the control plane host", async () => {
    const onConfirm = vi.fn();
    render(<DeployConfirmStep plan={hostedPlan()} onConfirm={onConfirm} onCancel={vi.fn()} />);
    const start = screen.getByTestId("deploy-start");
    expect(start).toBeDisabled();

    await userEvent.click(screen.getByTestId("deploy-acknowledge"));
    expect(start).toBeDisabled();

    // The full URL is not the phrase; the host is.
    await userEvent.type(screen.getByTestId("deploy-typed-context"), "https://api.reliantlabs.io");
    expect(start).toBeDisabled();
    await userEvent.clear(screen.getByTestId("deploy-typed-context"));
    await userEvent.type(screen.getByTestId("deploy-typed-context"), "api.reliantlabs.io");
    expect(start).toBeEnabled();
  });

  it("derives a token carrying the endpoint the plan named — what the daemon re-checks", () => {
    const token = deployTokenFor(hostedPlan());
    expect(token?.expectedDeclaredContext).toBe("https://api.reliantlabs.io");
    expect(token?.hosted?.environmentId).toBe("denv_01HZX");
  });

  it("offers no confirm when a hosted plan names no endpoint", () => {
    render(
      <DeployConfirmStep
        plan={hostedPlan({ guard: { verdict: "allow" } })}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByTestId("deploy-no-token").textContent).toMatch(/control plane/);
    expect(screen.getByTestId("deploy-no-token").textContent).not.toMatch(/cluster/);
  });

  it("shows a hosted target panel with the endpoint and no kube context", () => {
    render(<TargetPanel plan={hostedPlan({ target: { destination: "hosted", endpoint: "https://api.reliantlabs.io" } })} />);
    const panel = screen.getByTestId("deploy-target");
    expect(panel.getAttribute("data-destination")).toBe("hosted");
    expect(screen.getByTestId("deploy-target-endpoint").textContent).toBe("https://api.reliantlabs.io");
    // Never ensured: says so, rather than rendering a blank id.
    expect(screen.getByTestId("deploy-target-environment-id").textContent).toMatch(/creates it/);
    expect(panel.textContent).not.toMatch(/cluster|kube|context/i);
  });
});
