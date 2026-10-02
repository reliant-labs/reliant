// Copyright (c) 2025 Reliant Labs

/**
 * ONE CONFIRMATION RULE, FOR BOTH DESTINATIONS.
 *
 * The typed kube-context is gone, and the reason is the architecture rather
 * than a judgement about how careful operators are. The target is DECLARED in
 * KCL — `forge.K8sCluster.cluster` IS the kubectl context, forge deploys to it
 * and never to the ambient current-context — so there is no moment at which a
 * user chooses a cluster and no wrong one they could choose. Asking them to
 * transcribe a name they could not have influenced is friction that carries no
 * information, and it crowded out the plan it was supposed to make them read.
 *
 * What replaces it is a button that names the ENVIRONMENT, which is the thing
 * the user actually decided: "Deploy to prod". Identical to hosted, which is
 * the point — one rule, not a per-destination ceremony nobody can predict.
 *
 * THE BINDING IS UNCHANGED, and these tests say so explicitly. The token still
 * carries the declared context, the request still sends it, and the server
 * still refuses a deploy whose KCL moved between the preview and the click.
 * That check never depended on the typing; the typing was a demonstration of
 * it.
 *
 * The clusters themselves remain ON the plan as plain information — a reader
 * wants to know an env writes to two of them — just not as a headline and
 * never as something to type.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DeployConfirmStep } from "../DeployConfirmStep";
import { DeployFlow } from "../DeployFlow";
import { DeployPlanView } from "../DeployPlanView";
import { deployTokenFor } from "@/services/forge/deploy";
import { devPlan, meta as planMeta, planOutcome, prodPlan } from "./fixtures";

const PROD_CONTEXT = "gke_reliant-labs-475814_us-central1_prod";

function baseFlowProps() {
  return {
    isPlanning: false,
    isStarting: false,
    onConfirm: vi.fn(),
    onReplan: vi.fn(),
    onClose: vi.fn(),
  };
}

/** A clean two-cluster plan: devPlan's own preflight blocks, which hides the confirm. */
function twoClusterPlan() {
  return devPlan({
    preflight: { status: "ran", findings: [], blocking: 0 },
    ok: true,
    exit_code: 0,
  });
}

describe("a cluster confirm asks for nothing but the button", () => {
  it("renders no typed-context input and no acknowledgement checkbox", () => {
    const { container } = render(
      <DeployConfirmStep plan={prodPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />
    );

    expect(container.querySelectorAll("input")).toHaveLength(0);
    expect(screen.queryByTestId("deploy-typed-context")).toBeNull();
    expect(screen.queryByTestId("deploy-acknowledge")).toBeNull();
    expect(screen.queryByTestId("deploy-claim")).toBeNull();

    // And it no longer instructs anyone to type anything.
    expect(screen.getByTestId("deploy-confirm").textContent).not.toMatch(/type .* to confirm/i);
  });

  it("names the environment on the button, not the cluster", () => {
    render(<DeployConfirmStep plan={prodPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    const start = screen.getByTestId("deploy-start");
    expect(start).toBeEnabled();
    expect(start.textContent).toBe("Deploy to prod");
    expect(start.textContent).not.toContain(PROD_CONTEXT);
  });

  it("starts the deploy on the first click and hands over the rendered plan", async () => {
    const onConfirm = vi.fn();
    const plan = prodPlan();
    render(<DeployConfirmStep plan={plan} onConfirm={onConfirm} onCancel={vi.fn()} />);

    await userEvent.click(screen.getByTestId("deploy-start"));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("STILL BINDS the declared context in the token — the user just never typed it", () => {
    // The safety property, asserted independently of the ceremony that used to
    // advertise it. This is what the server re-checks.
    const token = deployTokenFor(prodPlan());
    expect(token?.expectedDeclaredContext).toBe(PROD_CONTEXT);
    expect(token?.expectedCurrentRelease).toBe("v1.5.15");
    expect(token?.hosted).toBeUndefined();
  });

  it("passes the RENDERED plan object through the flow, unchanged", async () => {
    const onConfirm = vi.fn();
    const plan = prodPlan();
    render(
      <DeployFlow {...baseFlowProps()} onConfirm={onConfirm} planOutcome={planOutcome(plan)} />
    );

    await userEvent.click(screen.getByTestId("deploy-start"));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm.mock.calls[0][0]).toBe(plan);
  });

  it("uses the same rule for a multi-cluster env — one button, named for the env", async () => {
    const onConfirm = vi.fn();
    render(
      <DeployConfirmStep plan={twoClusterPlan()} onConfirm={onConfirm} onCancel={vi.fn()} />
    );

    const start = screen.getByTestId("deploy-start");
    expect(start.textContent).toBe("Deploy to dev");
    expect(screen.queryByTestId("deploy-typed-context")).toBeNull();

    await userEvent.click(start);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

describe("the plan still says where it writes — as information", () => {
  it("lists the clusters without making one the headline or asking for it", () => {
    render(<DeployPlanView plan={twoClusterPlan()} />);

    // Still on the page: a reader wants to know this env writes to two.
    const target = screen.getByTestId("deploy-target");
    expect(target.textContent).toContain("k3d-control-plane");
    expect(target.textContent).toContain("k3d-cp-daemon");
    expect(target.getAttribute("data-context-count")).toBe("2");

    // But the heading is about the environment and the write, not a context.
    const heading = screen.getByTestId("deploy-target-heading").textContent ?? "";
    expect(heading).toMatch(/writes to 2 clusters/i);
    expect(heading).not.toContain("k3d-control-plane");
  });

  it("keeps a single-cluster env's context and namespace visible", () => {
    render(<DeployPlanView plan={prodPlan()} />);
    const target = screen.getByTestId("deploy-target");
    expect(target.textContent).toContain(PROD_CONTEXT);
    expect(target.textContent).toContain("control-plane-prod");
  });
});

describe("everything that was load-bearing is still load-bearing", () => {
  it("offers no confirm when the plan cannot produce a token", () => {
    render(
      <DeployConfirmStep
        plan={prodPlan({ guard: { verdict: "allow" } })}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByTestId("deploy-start")).toBeDisabled();
    expect(screen.getByTestId("deploy-no-token")).toBeTruthy();
  });

  it("keeps the destructive tick, which is now the ONLY checkbox in the flow", async () => {
    const onConfirm = vi.fn();
    const plan = prodPlan({
      preflight: {
        status: "ran",
        blocking: 0,
        findings: [
          {
            check: "persistent_volume_deletion",
            subject: "PVC/postgres-data",
            detail: "This would delete the database's storage.",
            blocking: false,
          },
        ],
      },
    });
    render(<DeployConfirmStep plan={plan} onConfirm={onConfirm} onCancel={vi.fn()} />);

    const start = screen.getByTestId("deploy-start");
    expect(start).toBeDisabled();
    expect(screen.getByTestId("deploy-destructive-findings").textContent).toContain(
      "PVC/postgres-data"
    );

    await userEvent.click(screen.getByTestId("deploy-acknowledge-destructive"));
    expect(start).toBeEnabled();
    await userEvent.click(start);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("still renders no skip-preflight, no-digest or force affordance", () => {
    const { container } = render(
      <DeployFlow {...baseFlowProps()} planOutcome={planOutcome(prodPlan())} />
    );
    const text = (container.textContent ?? "").toLowerCase();
    for (const phrase of ["skip preflight", "skip-preflight", "no-digest", "no digest", "force"]) {
      expect(text).not.toContain(phrase);
    }
    // No inputs at all on a clean plan now.
    expect(container.querySelectorAll("input")).toHaveLength(0);
  });

  it("still offers no confirm when preflight blocks", () => {
    render(<DeployFlow {...baseFlowProps()} planOutcome={planOutcome(devPlan())} />);
    expect(screen.queryByTestId("deploy-confirm")).toBeNull();
    expect(screen.queryByTestId("deploy-start")).toBeNull();
    expect(screen.getByTestId("deploy-blocked")).toBeTruthy();
  });

  it("renders the plan ABOVE the confirm, as before", () => {
    render(<DeployFlow {...baseFlowProps()} planOutcome={planOutcome(prodPlan())} />);
    const plan = screen.getByTestId("deploy-plan");
    const confirm = screen.getByTestId("deploy-confirm");
    expect(plan.compareDocumentPosition(confirm) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps the hosted path on the identical rule", () => {
    const hosted = prodPlan({
      env: "cloud",
      guard: { declared_context: "https://admin.reliantapi.com", verdict: "allow" },
      target: { destination: "hosted", endpoint: "https://admin.reliantapi.com", environment_id: "e1" },
    });
    render(<DeployConfirmStep plan={hosted} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(screen.getByTestId("deploy-start").textContent).toBe("Deploy to cloud");
  });
});

describe("a flow with no plan still cannot reach a write", () => {
  it("offers no button for any non-report outcome", () => {
    const outcomes = [
      { kind: "not-forge-project" as const, meta: planMeta({ isForgeProject: false }) },
      { kind: "unsupported" as const, meta: planMeta({ supported: false }) },
      { kind: "unreachable" as const, meta: planMeta() },
      { kind: "malformed" as const, meta: planMeta(), raw: "{{" },
    ];
    for (const outcome of outcomes) {
      const view = render(<DeployFlow {...baseFlowProps()} planOutcome={outcome} />);
      expect(screen.queryByTestId("deploy-start")).toBeNull();
      view.unmount();
    }
  });
});
