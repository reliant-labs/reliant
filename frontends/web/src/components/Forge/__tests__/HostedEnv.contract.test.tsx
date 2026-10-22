// Copyright (c) 2025 Reliant Labs

/**
 * Hosted environments in the forge console: where an env runs, the hosted
 * row, and the hosted deploy confirmation.
 *
 * Three things are pinned, each against the specific way it could regress:
 *
 *   1. UNKNOWN IS NOT CLUSTER. A destination this build does not recognise —
 *      or one an older forge never sent — renders "Unknown", never "Cluster".
 *   2. A HOSTED ROW NAMES NO INFRASTRUCTURE THE CUSTOMER DOES NOT OWN (#366):
 *      no host, no internal id, no kube context. It shows the kind, the
 *      release, where that release came from, and the platform's health
 *      verdict — and withholds the verdict for a cluster we do not observe.
 *   3. A HOSTED DEPLOY CONFIRM HAS NO KUBE-CONTEXT LANGUAGE, and it keeps the
 *      token discipline: the operator types the control plane's host, and the
 *      token carries the endpoint the plan named.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { WhereBadge } from "../EnvBadges";
import { EnvironmentTable } from "../Overview/EnvironmentTable";
import { DeployApproveStep } from "../Deploy/DeployApproveStep";
import { TargetPanel } from "../Deploy/DeployPlanView";
import { prodPlan } from "../Deploy/__tests__/fixtures";
import type { ForgeTopologyEnv } from "@/services/forge/topology";
import type { ForgeDeployReport } from "@/services/forge/deploy";
import { deployTokenFor } from "@/services/forge/deploy";
import type { DeployPlanReport } from "@/services/forge/deployPlan";
import { whereOf } from "@/services/forge/environments";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";

/**
 * The Overview's rows now come from the control plane (R-LIVE), so they are
 * built from LiveEnv rather than from a forge topology report joined with a
 * cloud list. The topology fixtures above still drive the WhereBadge and
 * deploy-confirm cases below, which are forge's own surfaces.
 */
function renderLiveRows(envs: LiveEnv[], statuses: Record<string, CloudEnvStatus> = {}) {
  return render(
    <EnvironmentTable
      rows={envs.map((env) => ({ env, status: statuses[env.id], statusLoading: false }))}
      onOpen={vi.fn()}
    />
  );
}

function liveEnv(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "denv_01HZX",
    name: "cloud",
    project: "acme",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "v2.0.0",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    provenance: "v2.0.0 · main@abc1234",
    holds: [],
    ...overrides,
  };
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
  it("names NONE of our own infrastructure — no host, no id (#366)", () => {
    // This assertion used to be the reverse: the row was required to SHOW
    // "api.reliantlabs.io". The customer did not choose that hostname, cannot
    // visit it, and every environment we host shows the same one, so it spent
    // a column telling them nothing they could act on.
    renderLiveRows([liveEnv()], {
      "denv_01HZX": { verdict: "converging", workloads: [], currentPromotion: null },
    });
    const row = screen.getByTestId("env-row-cloud");

    expect(row.textContent).toContain("Reliant cloud");
    expect(row.textContent).not.toContain("api.reliantlabs.io");
    expect(row.textContent).not.toContain("denv_01HZX");
    expect(row.textContent).not.toMatch(/control plane|endpoint|environment id/i);
    // The health still comes from the platform's verdict.
    expect(within(row).getByTestId("health-cloud").getAttribute("data-verdict")).toBe("converging");
    expect(row.textContent).not.toMatch(/gke_|namespace/i);
  });

  it("shows where the release came from instead", () => {
    // The column the endpoint used to occupy now carries something the
    // customer CAN act on: which source the running bytes were cut from.
    renderLiveRows([liveEnv()]);
    expect(screen.getByTestId("provenance-cloud").textContent).toBe("v2.0.0 · main@abc1234");
  });

  /**
   * The release column names the QUEUED release — the promotion's — which is
   * not what runs. Without the mark a reader would take v2.0.0 for live.
   */
  it("marks a release that is queued, on the release it is about", () => {
    renderLiveRows([
      liveEnv({
        phase: "held",
        holds: [
          { kind: "billing", promotionId: "p2", reason: "", fix: "", actionUrl: "", callerCanResolve: false },
        ],
      }),
      liveEnv({ id: "denv_other", name: "staging" }),
    ]);
    expect(screen.getByTestId("queued-cloud")).toHaveTextContent("Waiting on billing");
    expect(screen.queryByTestId("queued-staging")).not.toBeInTheDocument();
  });

  it("says a declared-but-unbuilt env is just that, not a blank or a fault", () => {
    renderLiveRows([
      liveEnv({
        release: "",
        provenance: "",
        declaredShape: { kind: "persistent", workloads: [], secrets: [], domains: [], clusters: [] },
      }),
    ]);
    expect(screen.getByTestId("env-row-cloud").textContent).toMatch(/declared, not built/i);
  });

  it("does not claim a health reading for a cluster we do not observe", () => {
    // A self-managed env is deployed by forge to the customer's own cluster,
    // with no observer on our side. A green chip there would assert a
    // convergence nobody measured.
    renderLiveRows([liveEnv({ name: "prod", kind: "self_managed" })]);
    const row = screen.getByTestId("env-row-prod");
    expect(within(row).queryByTestId("health-prod")).toBeNull();
    expect(row.textContent).toContain("Your cluster");
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

/** The hosted twin of the approvable plan a deploy is bound to. */
function hostedApprovablePlan(): DeployPlanReport {
  return {
    env: "cloud",
    ok: true,
    exit_code: 0,
    target: { release: "20261003.114500-abcdef123456" },
    deploy_plan: {
      digest: "sha256:4444444444444444444444444444444444444444444444444444444444444444",
      environment_id: "denv_01HZX",
      bundle_id: "bundle-9",
      release_version: "20261003.114500-abcdef123456",
      findings: [{ code: "image_changed", class: "info", section: "images", subject: "api" }],
    },
  };
}

describe("the hosted deploy approval", () => {
  it("names the environment — and none of our own infrastructure", () => {
    render(
      <DeployApproveStep
        report={hostedApprovablePlan()}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
        env="cloud"
      />
    );
    const approve = screen.getByTestId("deploy-approve");
    // The customer chose neither the control plane's host nor the id we file
    // their environment under, and can act on neither.
    expect(approve.textContent).not.toContain("api.reliantlabs.io");
    expect(approve.textContent).not.toContain("denv_01HZX");
    expect(approve.textContent).not.toMatch(
      /control plane|cluster|kube|context|manifests|environment id|digest/i
    );
    expect(screen.getByTestId("deploy-approve-start").textContent).toContain("cloud");
  });

  it("makes the button the approval: no typed phrase, no checkbox, one click", async () => {
    const onApprove = vi.fn();
    const { container } = render(
      <DeployApproveStep
        report={hostedApprovablePlan()}
        acknowledged={new Set()}
        onApprove={onApprove}
        onCancel={vi.fn()}
      />
    );

    // The friction that was here asked the user to transcribe OUR hostname,
    // which proved only that they could copy a string. The plan is the review.
    expect(container.querySelectorAll("input")).toHaveLength(0);
    const start = screen.getByTestId("deploy-approve-start");
    expect(start).toBeEnabled();

    await userEvent.click(start);
    expect(onApprove).toHaveBeenCalledTimes(1);
  });

  it("derives a token carrying the endpoint the plan named — what the daemon re-checks", () => {
    // UNCHANGED, and this is the test that says so: the user never types the
    // endpoint, and it still binds. It authorises the TARGET; the plan digest
    // authorises what ships.
    const token = deployTokenFor(hostedPlan());
    expect(token?.expectedDeclaredContext).toBe("https://api.reliantlabs.io");
    expect(token?.hosted?.environmentId).toBe("denv_01HZX");
  });

  it("offers no deploy when there is no plan to approve, and says so without jargon", () => {
    render(
      <DeployApproveStep
        report={{ env: "cloud" }}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
    const notice = screen.getByTestId("deploy-not-approvable").textContent ?? "";
    expect(notice).toMatch(/cannot be deployed/i);
    expect(notice).not.toMatch(/control plane|cluster|digest/i);
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
