// Copyright (c) 2025 Reliant Labs

/**
 * Hosted environments in the forge console: where an env runs, the hosted
 * row, and the hosted deploy confirmation.
 *
 * Three things are pinned, each against the specific way it could regress:
 *
 *   1. UNKNOWN IS NOT CLUSTER. A destination this build does not recognise —
 *      or one an older forge never sent — renders "Unknown", never "Cluster".
 *   2. A HOSTED ROW SHOWS ITS CONTROL PLANE, not a kube context: the endpoint
 *      host, and the env's health from the control plane's verdict.
 *   3. A HOSTED DEPLOY CONFIRM HAS NO KUBE-CONTEXT LANGUAGE, and it keeps the
 *      token discipline: the operator types the control plane's host, and the
 *      token carries the endpoint the plan named.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { WhereBadge } from "../EnvBadges";
import { EnvironmentTable } from "../Overview/EnvironmentTable";
import { DeployConfirmStep } from "../Deploy/DeployConfirmStep";
import { TargetPanel } from "../Deploy/DeployPlanView";
import { prodPlan } from "../Deploy/__tests__/fixtures";
import type { ForgeTopologyEnv } from "@/services/forge/topology";
import type { ForgeDeployReport } from "@/services/forge/deploy";
import { deployTokenFor } from "@/services/forge/deploy";
import { envFacts, joinEnvironments, whereOf } from "@/services/forge/environments";

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

function renderRows(envs: ForgeTopologyEnv[]) {
  const rows = joinEnvironments(envs, []).map((summary) => ({ summary, facts: envFacts(summary, undefined) }));
  return render(
    <EnvironmentTable
      rows={rows}
      promoteRelease="v2.0.0"
      canShip
      onOpen={vi.fn()}
      onPromote={vi.fn()}
      onDeploy={vi.fn()}
    />
  );
}

describe("where an environment runs", () => {
  it.each([
    ["hosted", "cloud", "Reliant cloud"],
    ["cluster", "cluster", "Cluster"],
    ["compose", "local", "Local"],
    ["host", "local", "Local"],
    ["external", "external", "External"],
    ["static", "static", "Static hosting"],
    ["mixed", "mixed", "Mixed"],
  ])("labels destination %s as %s", (destination, where, label) => {
    const resolved = whereOf({ destination }, null);
    expect(resolved).toBe(where);
    render(<WhereBadge env="x" where={resolved} />);
    expect(screen.getByTestId("where-x").textContent).toBe(label);
  });

  it("renders an unrecognised destination as Unknown — never as Cluster", () => {
    const where = whereOf({ destination: "moon-base" }, null);
    render(<WhereBadge env="x" where={where} />);
    const badge = screen.getByTestId("where-x");
    expect(badge.getAttribute("data-where")).toBe("unknown");
    expect(badge.textContent).toBe("Unknown");
    expect(badge.textContent).not.toMatch(/cluster/i);
  });

  it("renders an ABSENT destination (an older forge) as Unknown too", () => {
    expect(whereOf({}, null)).toBe("unknown");
  });

  it("reads ONLY `destination` — forge's redundant `hosted` flag decides nothing", () => {
    expect(whereOf({ hosted: true } as ForgeTopologyEnv, null)).toBe("unknown");
    expect(whereOf({ hosted: true, destination: "cluster" } as ForgeTopologyEnv, null)).toBe("cluster");
  });

  it("gives Reliant cloud a different treatment from unknown and from cluster", () => {
    const view = render(
      <>
        <WhereBadge env="a" where="cloud" />
        <WhereBadge env="b" where="cluster" />
        <WhereBadge env="c" where="unknown" />
      </>
    );
    const cls = (id: string) => view.getByTestId(id).querySelector("span > span")?.className ?? "";
    expect(cls("where-a")).not.toBe(cls("where-b"));
    expect(cls("where-a")).not.toBe(cls("where-c"));
    expect(cls("where-b")).not.toBe(cls("where-c"));
  });
});

describe("a hosted row on the Overview", () => {
  it("shows the endpoint host and the env's health — and no kube context", () => {
    renderRows([HOSTED_ENV, CLUSTER_ENV]);
    const row = screen.getByTestId("env-row-cloud");

    expect(within(row).getByTestId("where-cloud").textContent).toBe("Reliant cloud");
    expect(row.textContent).toContain("api.reliantlabs.io");
    // No env-level verdict: the worst workload (worker, converging) decides —
    // and converging is not converged.
    expect(within(row).getByTestId("health-cloud").getAttribute("data-verdict")).toBe("converging");
    expect(row.textContent).not.toMatch(/gke_|namespace/i);
  });

  it("says an un-ensured hosted env is not deployed rather than showing a blank health", () => {
    renderRows([{ ...HOSTED_ENV, environment_id: "" }]);
    expect(screen.getByTestId("health-cloud").textContent).toMatch(/not deployed/i);
  });

  it("keeps a cluster row's kube context and namespace", () => {
    renderRows([HOSTED_ENV, CLUSTER_ENV]);
    const row = screen.getByTestId("env-row-prod");
    expect(within(row).getByTestId("where-prod").textContent).toBe("Cluster");
    expect(row.textContent).toContain("gke_prod · app-prod");
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
  it("names the environment and the release — and none of our own infrastructure", () => {
    render(<DeployConfirmStep plan={hostedPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    const confirm = screen.getByTestId("deploy-confirm");
    // The customer chose neither the control plane's host nor the id we file
    // their environment under, and can act on neither.
    expect(confirm.textContent).not.toContain("api.reliantlabs.io");
    expect(confirm.textContent).not.toContain("denv_01HZX");
    expect(confirm.textContent).not.toMatch(
      /control plane|cluster|kube|context|manifests|environment id/i
    );
    expect(screen.getByTestId("deploy-start").textContent).toBe("Deploy to cloud");
  });

  it("makes the button the approval: no typed phrase, no checkbox, one click", async () => {
    const onConfirm = vi.fn();
    const { container } = render(
      <DeployConfirmStep plan={hostedPlan()} onConfirm={onConfirm} onCancel={vi.fn()} />
    );

    // The friction that was here asked the user to transcribe OUR hostname,
    // which proved only that they could copy a string. The plan is the review.
    expect(container.querySelectorAll("input")).toHaveLength(0);
    const start = screen.getByTestId("deploy-start");
    expect(start).toBeEnabled();

    await userEvent.click(start);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("derives a token carrying the endpoint the plan named — what the daemon re-checks", () => {
    // UNCHANGED BY THE COPY PASS, and this is the test that says so: the user
    // no longer types the endpoint, and it still binds.
    const token = deployTokenFor(hostedPlan());
    expect(token?.expectedDeclaredContext).toBe("https://api.reliantlabs.io");
    expect(token?.hosted?.environmentId).toBe("denv_01HZX");
  });

  it("cannot start when a hosted plan names no environment to deploy to", () => {
    render(
      <DeployConfirmStep
        plan={hostedPlan({ guard: { verdict: "allow" } })}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByTestId("deploy-start")).toBeDisabled();
    const notice = screen.getByTestId("deploy-no-token").textContent ?? "";
    expect(notice).toMatch(/cannot be deployed/i);
    expect(notice).not.toMatch(/control plane|cluster/i);
  });

  it("shows a hosted target panel that is one plain sentence", () => {
    render(<TargetPanel plan={hostedPlan({ target: { destination: "hosted", endpoint: "https://api.reliantlabs.io" } })} />);
    const panel = screen.getByTestId("deploy-target");
    expect(panel.getAttribute("data-destination")).toBe("hosted");
    // Never ensured: the sentence says it is created, with no blank id field
    // for the reader to mistake for a fault.
    expect(screen.getByTestId("deploy-target-heading").textContent).toBe(
      "Creates cloud and deploys release v2.0.0."
    );
    expect(screen.queryByTestId("deploy-target-endpoint")).toBeNull();
    expect(screen.queryByTestId("deploy-target-environment-id")).toBeNull();
    expect(panel.textContent).not.toMatch(/cluster|kube|context|control plane|api\.reliantlabs\.io/i);
  });
});
