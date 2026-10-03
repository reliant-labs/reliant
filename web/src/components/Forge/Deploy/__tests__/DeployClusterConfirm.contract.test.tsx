// Copyright (c) 2025 Reliant Labs

/**
 * ONE APPROVAL RULE, FOR BOTH DESTINATIONS.
 *
 * The typed kube-context is gone, and the reason is the architecture rather
 * than a judgement about how careful operators are. The target is DECLARED in
 * KCL — `forge.K8sCluster.cluster` IS the kubectl context, forge deploys to it
 * and never to the ambient current-context — so there is no moment at which a
 * user chooses a cluster and no wrong one they could choose. Asking them to
 * transcribe a name they could not have influenced is friction that carries no
 * information, and it crowded out the plan it was supposed to make them read.
 *
 * WHAT THE APPROVAL IS NOW. The button names the ENVIRONMENT, which is the
 * thing the user decided, and it approves A SPECIFIC PLAN — the digest travels
 * with the deploy and forge refuses anything else. The target token still binds
 * the declared cluster, unchanged; it just never covered the content, which is
 * what the plan digest adds.
 *
 * THERE IS NO PATH FROM THE INSTANT PREVIEW TO A WRITE, and several tests here
 * pin that. The preview describes the environment as it is; a deploy ships what
 * a build produces. Until that build has run there is no deploy control at all.
 *
 * The clusters themselves remain ON the plan as plain information — a reader
 * wants to know an env writes to two of them — just not as a headline and
 * never as something to type.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DeployApproveStep } from "../DeployApproveStep";
import { DeployFlow } from "../DeployFlow";
import { DeployPlanView } from "../DeployPlanView";
import { deployTokenFor } from "@/services/forge/deploy";
import type { DeployPlanReport } from "@/services/forge/deployPlan";
import { devPlan, meta as planMeta, planOutcome, prodPlan } from "./fixtures";

const PROD_CONTEXT = "gke_reliant-labs-475814_us-central1_prod";

const APPROVED_DIGEST =
  "sha256:1111111111111111111111111111111111111111111111111111111111111111";
const APPROVED_RELEASE = "20261003.114500-abcdef123456";

/** A clean plan-only document: a release cut, nothing irreversible. */
function approvablePlan(env = "prod"): DeployPlanReport {
  return {
    env,
    ok: true,
    exit_code: 0,
    confirmed: false,
    applied: false,
    target: { release: APPROVED_RELEASE },
    deploy_plan: {
      digest: APPROVED_DIGEST,
      environment_id: env,
      bundle_id: "bundle-1",
      release_version: APPROVED_RELEASE,
      findings: [{ code: "image_changed", class: "info", section: "images", subject: "api" }],
    },
  };
}

function baseFlowProps() {
  return {
    isPlanning: false,
    isStarting: false,
    onReplan: vi.fn(),
    onClose: vi.fn(),
    onBuildAndPlan: vi.fn(),
    acknowledged: new Set<string>(),
    onAcknowledge: vi.fn(),
    onApprove: vi.fn(),
    onReplanAfterStale: vi.fn(),
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

describe("an approval asks for nothing but the button", () => {
  it("renders no typed-context input and no blanket acknowledgement", () => {
    const { container } = render(
      <DeployApproveStep
        report={approvablePlan()}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
      />
    );

    // Nothing to type, and no catch-all tick: an acknowledgement exists only
    // per irreversible change, and this plan has none.
    expect(container.querySelectorAll("input")).toHaveLength(0);
  });

  it("names the environment on the button, not the cluster", () => {
    render(
      <DeployApproveStep
        report={approvablePlan()}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
        env="prod"
      />
    );

    const label = screen.getByTestId("deploy-approve-start").textContent ?? "";
    expect(label).toContain("prod");
    expect(label).not.toContain(PROD_CONTEXT);
  });

  it("deploys on the first click, carrying the digest of the plan on screen", async () => {
    const onApprove = vi.fn();
    render(
      <DeployApproveStep
        report={approvablePlan()}
        acknowledged={new Set()}
        onApprove={onApprove}
        onCancel={vi.fn()}
      />
    );

    await userEvent.click(screen.getByTestId("deploy-approve-start"));

    expect(onApprove).toHaveBeenCalledTimes(1);
    expect(onApprove).toHaveBeenCalledWith({
      approveDigest: APPROVED_DIGEST,
      releaseVersion: APPROVED_RELEASE,
      acknowledgedFindings: [],
    });
  });

  it("STILL BINDS the declared context in the token — the user just never typed it", () => {
    const token = deployTokenFor(prodPlan());
    expect(token?.expectedDeclaredContext).toBe(PROD_CONTEXT);
  });

  it("uses the same rule for a multi-cluster env — one button, named for the env", () => {
    render(
      <DeployApproveStep
        report={approvablePlan("dev")}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
        env="dev"
      />
    );

    expect(screen.getByTestId("deploy-approve-start").textContent).toContain("dev");
  });
});

describe("the plan still says where it writes — as information", () => {
  it("lists the clusters without making one the headline or asking for it", () => {
    render(<DeployPlanView plan={twoClusterPlan()} />);
    const target = screen.getByTestId("deploy-target");
    expect(target).toBeInTheDocument();
  });

  it("keeps a single-cluster env's context and namespace visible", () => {
    render(<DeployPlanView plan={prodPlan()} />);
    expect(screen.getByTestId("deploy-target").textContent).toContain(PROD_CONTEXT);
  });
});

describe("everything that was load-bearing is still load-bearing", () => {
  it("offers no deploy when the plan cannot produce a token", () => {
    render(
      <DeployFlow
        {...baseFlowProps()}
        planOutcome={planOutcome(
          prodPlan({ guard: { declared_context: "", current_context: "", verdict: "allow" } })
        )}
      />
    );

    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
    expect(screen.queryByTestId("deploy-build-and-plan")).not.toBeInTheDocument();
    expect(screen.getByTestId("deploy-blocked")).toBeInTheDocument();
  });

  it("offers no deploy when the preflight blocks", () => {
    render(<DeployFlow {...baseFlowProps()} planOutcome={planOutcome(devPlan())} />);

    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
    expect(screen.getByTestId("deploy-blocker-preflight-blocking")).toBeInTheDocument();
  });

  it("renders no skip-preflight, no-digest or force affordance anywhere", () => {
    const { container } = render(
      <DeployFlow
        {...baseFlowProps()}
        planOutcome={planOutcome(prodPlan())}
        approvablePlan={approvablePlan()}
      />
    );

    const text = container.textContent ?? "";
    for (const forbidden of ["skip-preflight", "no-digest", "force"]) {
      expect(text.toLowerCase()).not.toContain(forbidden);
    }
  });

  it("renders the plan ABOVE the approval, as before", () => {
    const { container } = render(
      <DeployFlow
        {...baseFlowProps()}
        planOutcome={planOutcome(prodPlan())}
        approvablePlan={approvablePlan()}
      />
    );

    const html = container.innerHTML;
    expect(html.indexOf('data-testid="approvable-plan"')).toBeLessThan(
      html.indexOf('data-testid="deploy-approve"')
    );
  });

  it("offers no deploy until the changes have been worked out", () => {
    // THE CORE OF THE CHANGE: a clean, confirmable preview on its own is not
    // enough. The preview cannot say what a deploy would ship, so the only
    // control offered is the one that works it out.
    render(<DeployFlow {...baseFlowProps()} planOutcome={planOutcome(prodPlan())} />);

    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
    expect(screen.getByTestId("deploy-build-and-plan")).toBeInTheDocument();
  });
});

describe("a flow with no plan still cannot reach a write", () => {
  it("offers no button for any non-report outcome", () => {
    for (const outcome of [
      { kind: "not-forge-project" as const, meta: planMeta() },
      { kind: "unsupported" as const, meta: planMeta() },
      { kind: "unreachable" as const, meta: planMeta() },
      { kind: "malformed" as const, meta: planMeta() },
    ]) {
      const { unmount } = render(
        <DeployFlow {...baseFlowProps()} planOutcome={outcome} />
      );
      expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
      expect(screen.queryByTestId("deploy-build-and-plan")).not.toBeInTheDocument();
      unmount();
    }
  });
});
