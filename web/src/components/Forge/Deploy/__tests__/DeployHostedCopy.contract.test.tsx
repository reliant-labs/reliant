// Copyright (c) 2025 Reliant Labs

/**
 * WHAT A HOSTED DEPLOY DIALOG MAY SAY, AND WHAT IT MAY ASK FOR.
 *
 * A hosted environment is run FOR the customer. They did not choose the control
 * plane's hostname, they cannot visit it, and they have no use for the internal
 * id it files their environment under — so naming either one spends the most
 * valuable line on the screen telling them something they cannot act on. These
 * tests pin the absence, because absence is the kind of thing a later "let's
 * show a bit more detail" quietly undoes.
 *
 * THE PLAN IS THE REVIEW AND THE BUTTON IS THE APPROVAL. Typing a hostname
 * proves the user can copy a string; it does not prove they read the plan, and
 * when the string is an internal endpoint it actively teaches them to ignore
 * the words above it. The token still binds — it is derived from the plan and
 * the server re-checks it — the user simply does not transcribe it.
 *
 * The cluster path is NOT relaxed, and the last test here is what keeps the two
 * from drifting into one.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ApprovablePlanView } from "../ApprovablePlanView";
import { DeployApproveStep } from "../DeployApproveStep";
import type { DeployPlanReport } from "@/services/forge/deployPlan";
import { DeployFlow } from "../DeployFlow";
import { DeployPlanView } from "../DeployPlanView";
import { deployTokenFor, type ForgeDeployReport } from "@/services/forge/deploy";
import { meta as planMeta, prodPlan, refusal } from "./fixtures";

const ENDPOINT = "https://admin.reliantapi.com";
const ENVIRONMENT_ID = "denv_01HZXABCDEF";

function hostedPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return prodPlan({
    env: "prod",
    guard: { declared_context: ENDPOINT, verdict: "allow", reason: "control_plane_declared" },
    target: { destination: "hosted", endpoint: ENDPOINT, environment_id: ENVIRONMENT_ID },
    release: "v1.5.15",
    images: { images: [], digest_count: 0, tag_count: 0 },
    resources: [],
    ...overrides,
  });
}

describe("a hosted plan names no infrastructure the customer does not own", () => {
  it("renders no control-plane endpoint row and no environment id", () => {
    render(<DeployPlanView plan={hostedPlan()} />);

    expect(screen.queryByTestId("deploy-target-endpoint")).toBeNull();
    expect(screen.queryByTestId("deploy-target-environment-id")).toBeNull();

    const text = screen.getByTestId("deploy-plan").textContent ?? "";
    expect(text).not.toContain(ENDPOINT);
    expect(text).not.toContain("admin.reliantapi.com");
    expect(text).not.toContain(ENVIRONMENT_ID);
    expect(text).not.toMatch(/control plane|environment id|endpoint/i);
  });

  it("says what the deploy does in one plain sentence", () => {
    const { unmount } = render(<DeployPlanView plan={hostedPlan()} />);
    expect(screen.getByTestId("deploy-target-heading").textContent).toBe(
      "Deploys release v1.5.15 to prod."
    );
    unmount();

    // Never ensured: the environment does not exist yet, and the sentence says
    // so without reaching for an id that is still empty.
    render(
      <DeployPlanView
        plan={hostedPlan({
          target: { destination: "hosted", endpoint: ENDPOINT, environment_id: "" },
        })}
      />
    );
    expect(screen.getByTestId("deploy-target-heading").textContent).toBe(
      "Creates prod and deploys release v1.5.15."
    );
  });

  it("explains a missing release as a thing to choose, not as a promote forge will refuse", () => {
    render(<DeployPlanView plan={hostedPlan({ release: "" })} />);
    const release = screen.getByTestId("deploy-release").textContent ?? "";
    expect(release).toMatch(/no release/i);
    expect(release).toMatch(/choose a release/i);
    expect(release).not.toMatch(/promote|digest|forge/i);
  });

  it("keeps the refusal's reason and fix, without naming forge's internals", () => {
    render(
      <DeployPlanView
        plan={hostedPlan({
          guard: {
            declared_context: ENDPOINT,
            verdict: "refuse",
            reason: "the environment is locked",
            fix: "Unlock the environment and try again.",
          },
        })}
      />
    );
    const refused = screen.getByTestId("deploy-guard-refused").textContent ?? "";
    expect(refused).toMatch(/the environment is locked/);
    expect(screen.getByTestId("deploy-guard-fix").textContent).toBe(
      "Unlock the environment and try again."
    );
    expect(refused).not.toMatch(/control plane|endpoint/i);
  });
});

const APPROVED_DIGEST =
  "sha256:5555555555555555555555555555555555555555555555555555555555555555";
const APPROVED_RELEASE = "20261003.114500-abcdef123456";

/** The plan a hosted deploy is approved from. */
function hostedApprovable(overrides: Partial<DeployPlanReport> = {}): DeployPlanReport {
  return {
    env: "prod",
    ok: true,
    exit_code: 0,
    target: { release: APPROVED_RELEASE },
    deploy_plan: {
      digest: APPROVED_DIGEST,
      environment_id: ENVIRONMENT_ID,
      bundle_id: "bundle-7",
      release_version: APPROVED_RELEASE,
      findings: [{ code: "image_changed", class: "info", section: "images", subject: "api" }],
    },
    ...overrides,
  };
}

/** The same plan, carrying an irreversible change that must be accepted by name. */
function hostedDestructive(): DeployPlanReport {
  const base = hostedApprovable();
  return {
    ...base,
    deploy_plan: {
      ...base.deploy_plan,
      findings: [
        {
          code: "stateful_deletion",
          class: "stop",
          section: "stateful_deletions",
          subject: "StatefulSet/postgres",
          detail: "This would delete the database's storage.",
        },
      ],
    },
  };
}

describe("the hosted approval is the button", () => {
  it("asks for no typed phrase and no checkbox", () => {
    const { container } = render(
      <DeployApproveStep
        report={hostedApprovable()}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
      />
    );

    expect(container.querySelectorAll("input")).toHaveLength(0);
    expect(screen.queryByTestId("deploy-typed-context")).toBeNull();
    expect(screen.queryByTestId("deploy-acknowledge")).toBeNull();
    expect(screen.queryByTestId("deploy-claim")).toBeNull();
  });

  it("labels the button with the environment and enables it immediately", () => {
    render(
      <DeployApproveStep
        report={hostedApprovable()}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
        env="prod"
      />
    );
    const start = screen.getByTestId("deploy-approve-start");
    expect(start).toBeEnabled();
    expect(start.textContent).toContain("prod");
  });

  it("starts the deploy on the first click, bound to the plan on screen", async () => {
    const onApprove = vi.fn();
    const plan = hostedPlan();
    render(
      <DeployApproveStep
        report={hostedApprovable()}
        acknowledged={new Set()}
        onApprove={onApprove}
        onCancel={vi.fn()}
      />
    );

    await userEvent.click(screen.getByTestId("deploy-approve-start"));
    expect(onApprove).toHaveBeenCalledTimes(1);
    expect(onApprove.mock.calls[0][0].approveDigest).toBe(APPROVED_DIGEST);

    // THE BINDING IS STILL THERE — the user did not type it, so this is the
    // assertion that it is still carried. The endpoint the plan named is what
    // travels, and what the server re-checks.
    const token = deployTokenFor(plan);
    expect(token?.expectedDeclaredContext).toBe(ENDPOINT);
    expect(token?.hosted?.environmentId).toBe(ENVIRONMENT_ID);
    expect(token?.expectedCurrentRelease).toBe("v1.5.15");
  });

  it("names no endpoint, id or kube vocabulary at the point of the click", () => {
    render(
      <DeployApproveStep
        report={hostedApprovable()}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    const text = screen.getByTestId("deploy-approve").textContent ?? "";
    expect(text).not.toContain(ENDPOINT);
    expect(text).not.toContain(ENVIRONMENT_ID);
    expect(text).not.toMatch(/control plane|cluster|kube|context|manifests|undone from git/i);
  });

  it("cannot start when the plan names no environment to deploy to", () => {
    // DeployFlow drops the whole step for an unconfirmable plan; rendered
    // directly, the step still refuses — the button is inert and the reason
    // avoids the internal nouns the old copy reached for.
    render(
      <DeployApproveStep
        report={{ env: "prod" }}
        acknowledged={new Set()}
        onApprove={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.queryByTestId("deploy-approve-start")).toBeNull();
    const notice = screen.getByTestId("deploy-not-approvable").textContent ?? "";
    expect(notice).toMatch(/cannot be deployed/i);
    expect(notice).not.toMatch(/control plane|cluster/i);
  });
});

describe("a destructive change requires an explicit, per-code acceptance", () => {
  // The ONLY thing that brings a tick back, and it now comes from the PLAN's
  // stop-class findings rather than from a guess at which preflight check names
  // mean destruction. Forge classifies them, so an unknown one is treated as
  // needing acceptance instead of being quietly waved through.

  it("withholds the button until accepted, and says what is destroyed", async () => {
    // ABSENT, NOT DISABLED. A disabled button invites hunting for the way to
    // enable it; an absent one makes the acceptance the thing to do.
    const onApprove = vi.fn();
    const { rerender } = render(
      <>
        <ApprovablePlanView
          report={hostedDestructive()}
          acknowledged={new Set()}
          onAcknowledge={vi.fn()}
        />
        <DeployApproveStep
          report={hostedDestructive()}
          acknowledged={new Set()}
          onApprove={onApprove}
          onCancel={vi.fn()}
        />
      </>
    );

    expect(screen.queryByTestId("deploy-approve-start")).toBeNull();

    const notice = screen.getByTestId("approvable-plan-stop").textContent ?? "";
    expect(notice).toContain("StatefulSet/postgres");
    expect(notice).toContain("This would delete the database's storage.");

    // Accepted BY CODE, and only then does the control exist.
    rerender(
      <>
        <ApprovablePlanView
          report={hostedDestructive()}
          acknowledged={new Set(["stateful_deletion"])}
          onAcknowledge={vi.fn()}
        />
        <DeployApproveStep
          report={hostedDestructive()}
          acknowledged={new Set(["stateful_deletion"])}
          onApprove={onApprove}
          onCancel={vi.fn()}
        />
      </>
    );

    await userEvent.click(screen.getByTestId("deploy-approve-start"));
    expect(onApprove).toHaveBeenCalledTimes(1);
    expect(onApprove.mock.calls[0][0].acknowledgedFindings).toEqual(["stateful_deletion"]);
  });

  it("is absent — acceptance and all — for an ordinary hosted plan", () => {
    render(
      <ApprovablePlanView
        report={hostedApprovable()}
        acknowledged={new Set()}
        onAcknowledge={vi.fn()}
      />
    );
    expect(screen.queryByTestId("approvable-plan-stop")).toBeNull();
    expect(screen.queryByTestId("acknowledge-stateful_deletion")).toBeNull();
  });
});

describe("a hosted refusal does not relabel our endpoint as their cluster", () => {
  it("states the staleness without the endpoint diff", () => {
    render(
      <DeployFlow
        isPlanning={false}
        isStarting={false}
        onReplan={vi.fn()}
        onClose={vi.fn()}
        onBuildAndPlan={vi.fn()}
        acknowledged={new Set()}
        onAcknowledge={vi.fn()}
        onApprove={vi.fn()}
        onReplanAfterStale={vi.fn()}
        planOutcome={{ kind: "report", meta: planMeta(), report: hostedPlan() }}
        startResult={{
          kind: "refused",
          refusal: refusal({
            reason: "stale-declared-context",
            expectedDeclaredContext: ENDPOINT,
            actualDeclaredContext: "https://other.reliantapi.com",
          }),
        }}
      />
    );

    const notice = screen.getByTestId("deploy-refusal");
    // The diff is two of OUR hostnames under the label "You approved the
    // cluster" — meaningless to a customer and wrong twice over.
    expect(screen.queryByTestId("deploy-refusal-context-diff")).toBeNull();
    expect(notice.textContent).not.toContain(ENDPOINT);
    expect(notice.textContent).not.toMatch(/cluster|kubeconfig|declared context/i);
    expect(screen.getByTestId("deploy-refusal-heading").textContent).toMatch(/changed/i);
    expect(notice.textContent).toMatch(/nothing was deployed/i);
  });

  it("keeps the cluster diff for a cluster env", () => {
    render(
      <DeployFlow
        isPlanning={false}
        isStarting={false}
        onReplan={vi.fn()}
        onClose={vi.fn()}
        onBuildAndPlan={vi.fn()}
        acknowledged={new Set()}
        onAcknowledge={vi.fn()}
        onApprove={vi.fn()}
        onReplanAfterStale={vi.fn()}
        planOutcome={{ kind: "report", meta: planMeta(), report: prodPlan() }}
        startResult={{
          kind: "refused",
          refusal: refusal({ reason: "stale-declared-context" }),
        }}
      />
    );
    expect(screen.getByTestId("deploy-refusal-context-diff")).toBeTruthy();
    expect(screen.getByTestId("deploy-refusal-heading").textContent).toMatch(/different cluster/i);
  });
});

describe("the cluster path now follows the SAME rule", () => {
  // This block previously asserted the cluster ceremony as deliberately
  // divergent. The owner's follow-up collapsed the two: KCL declares the
  // target, so there was never a wrong cluster for the typing to catch. The
  // hosted-specific assertions above are what remain destination-specific, and
  // they are about COPY (which nouns a customer can act on), not ceremony.
  it("approves with the button alone, named for the env, exactly like hosted", async () => {
    const onApprove = vi.fn();
    render(
      <DeployApproveStep
        report={hostedApprovable()}
        acknowledged={new Set()}
        onApprove={onApprove}
        onCancel={vi.fn()}
        env="prod"
      />
    );

    const start = screen.getByTestId("deploy-approve-start");
    expect(start).toBeEnabled();
    expect(start.textContent).toContain("prod");

    expect(screen.queryByTestId("deploy-typed-context")).toBeNull();
    expect(screen.queryByTestId("deploy-acknowledge")).toBeNull();

    await userEvent.click(start);
    expect(onApprove).toHaveBeenCalledTimes(1);
  });
});
