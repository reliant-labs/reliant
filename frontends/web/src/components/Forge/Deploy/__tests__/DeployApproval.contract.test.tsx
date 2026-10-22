// Copyright (c) 2025 Reliant Labs

/**
 * THE CONTRACT: an approval is bound to the plan that was READ, and the surface
 * that asks for it never shows anything from the environment's existing
 * binding.
 *
 * The second half is the one that needs a test rather than a comment. The
 * instant preview carries the digests of the images the environment is running
 * NOW, and they are a plausible, precise, WRONG answer to "what does this
 * deploy ship" — a deploy builds from the chosen branch and cuts a new release.
 * Those digests next to a deploy button is the defect this work removed, and
 * nothing stops it coming back except a test that fails when it does.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApprovablePlanView } from "../ApprovablePlanView";
import { DeployApproveStep } from "../DeployApproveStep";
import { PlanStaleNotice } from "../PlanStaleNotice";
import {
  approvableDigest,
  approvableRelease,
  diffPlans,
  findingClassOf,
  planIsApprovable,
  requiredAcknowledgements,
  type DeployPlan,
  type DeployPlanReport,
} from "@/services/forge/deployPlan";

// ── Fixtures ────────────────────────────────────────────────────────────────

const PLAN_DIGEST = "sha256:1111111111111111111111111111111111111111111111111111111111111111";
const PLAN_RELEASE = "20261003.114500-abcdef123456";

/**
 * A digest belonging to the environment's EXISTING binding — the kind the
 * instant preview reports. It must never appear on an approval surface, and
 * several tests assert exactly that.
 */
const EXISTING_BINDING_DIGEST = "sha256:9999999999999999999999999999999999999999999999999999999999999999";

function planReport(overrides: Partial<DeployPlanReport> = {}): DeployPlanReport {
  return {
    env: "prod",
    ok: true,
    exit_code: 0,
    confirmed: false,
    applied: false,
    target: { release: PLAN_RELEASE },
    next_step: `forge env deploy prod ${PLAN_RELEASE} --approve ${PLAN_DIGEST}`,
    deploy_plan: {
      digest: PLAN_DIGEST,
      environment_id: "prod",
      bundle_id: "bundle-1",
      release_version: PLAN_RELEASE,
      live_basis: { drift_observed: false },
      config_identical: false,
      findings: [
        { code: "image_changed", class: "info", section: "images", subject: "api" },
      ],
    },
    ...overrides,
  };
}

function withFindings(findings: DeployPlan["findings"]): DeployPlanReport {
  const report = planReport();
  return { ...report, deploy_plan: { ...report.deploy_plan, findings } };
}

const noop = () => {};

// ── Plan-only success ───────────────────────────────────────────────────────

describe("the approvable plan", () => {
  it("shows what this deploy ships, from the plan and not from the binding", () => {
    render(
      <ApprovablePlanView report={planReport()} acknowledged={new Set()} onAcknowledge={noop} />
    );

    expect(screen.getByTestId("approvable-plan")).toBeInTheDocument();
    expect(screen.getByText(new RegExp(PLAN_RELEASE))).toBeInTheDocument();
  });

  it("offers the deploy when the plan carries nothing irreversible", () => {
    const onApprove = vi.fn();
    render(
      <DeployApproveStep
        report={planReport()}
        acknowledged={new Set()}
        onApprove={onApprove}
        onCancel={noop}
      />
    );

    expect(screen.getByTestId("deploy-approve-start")).toBeInTheDocument();
  });

  it("sends the digest and release from the document it rendered", async () => {
    const onApprove = vi.fn();
    render(
      <DeployApproveStep
        report={planReport()}
        acknowledged={new Set()}
        onApprove={onApprove}
        onCancel={noop}
      />
    );

    await userEvent.click(screen.getByTestId("deploy-approve-start"));

    expect(onApprove).toHaveBeenCalledWith({
      approveDigest: PLAN_DIGEST,
      releaseVersion: PLAN_RELEASE,
      acknowledgedFindings: [],
    });
  });

  it("says so when the changes could not be worked out, rather than showing none", () => {
    render(
      <ApprovablePlanView
        report={{ env: "prod", deploy_plan: undefined }}
        acknowledged={new Set()}
        onAcknowledge={noop}
      />
    );

    expect(screen.getByTestId("approvable-plan-none")).toBeInTheDocument();
    // The distinction that matters: unknown is not "nothing changes".
    expect(screen.getByText(/not the same as nothing changing/i)).toBeInTheDocument();
  });

  it("offers no deploy when there is no plan to approve", () => {
    render(
      <DeployApproveStep
        report={{ env: "prod" }}
        acknowledged={new Set()}
        onApprove={noop}
        onCancel={noop}
      />
    );

    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
    expect(screen.getByTestId("deploy-not-approvable")).toBeInTheDocument();
  });

  it("offers no deploy when the plan names no release to ship", () => {
    // A digest with nothing to deploy it as: the deploy would be versionless,
    // which builds and cuts a SECOND release the digest cannot match.
    render(
      <DeployApproveStep
        report={{ ...planReport(), target: undefined, deploy_plan: { digest: PLAN_DIGEST } }}
        acknowledged={new Set()}
        onApprove={noop}
        onCancel={noop}
      />
    );

    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
  });
});

// ── THE REGRESSION GUARD: no digests from the existing binding ──────────────

describe("the approval surface never shows the existing binding's digests", () => {
  it("renders no digest that did not come from the plan-only document", () => {
    // A document shaped like the instant preview's: it carries images and
    // digests for what is CURRENTLY deployed. None of it may surface here.
    const contaminated = {
      ...planReport(),
      images: {
        digest_count: 3,
        tag_count: 0,
        images: [
          { reference: `api@${EXISTING_BINDING_DIGEST}`, pinning: "digest" },
          { reference: `web@${EXISTING_BINDING_DIGEST}`, pinning: "digest" },
        ],
      },
      release: "v1.4.0", // the binding's release, not this plan's
    } as DeployPlanReport;

    const { container } = render(
      <>
        <ApprovablePlanView
          report={contaminated}
          acknowledged={new Set()}
          onAcknowledge={noop}
        />
        <DeployApproveStep
          report={contaminated}
          acknowledged={new Set()}
          onApprove={noop}
          onCancel={noop}
        />
      </>
    );

    const text = container.textContent ?? "";
    expect(text).not.toContain(EXISTING_BINDING_DIGEST);
    // Nor the binding's release, which is equally not what ships.
    expect(text).not.toContain("v1.4.0");
    // And the plan's own release IS shown, so this is not passing by
    // rendering nothing at all.
    expect(text).toContain(PLAN_RELEASE);
  });

  it("shows no digest-pinning summary, which only ever described the old binding", () => {
    const { container } = render(
      <ApprovablePlanView report={planReport()} acknowledged={new Set()} onAcknowledge={noop} />
    );

    expect(container.querySelector("[data-testid='deploy-images']")).toBeNull();
    expect(container.querySelector("[data-testid='deploy-digest-count']")).toBeNull();
  });
});

// ── Stop-class findings: per-code acknowledgement, button absent ────────────

describe("irreversible changes", () => {
  const destructive = withFindings([
    {
      code: "stateful_deletion",
      class: "stop",
      section: "stateful_deletions",
      subject: "postgres-data",
      detail: "the volume would be deleted",
    },
    {
      code: "lb_identity_change",
      class: "stop",
      section: "lb_identity",
      subject: "api-lb",
      detail: "the address would be reissued",
    },
  ]);

  it("asks for each one separately", () => {
    render(
      <ApprovablePlanView
        report={destructive}
        acknowledged={new Set()}
        onAcknowledge={noop}
      />
    );

    expect(screen.getByTestId("acknowledge-stateful_deletion")).toBeInTheDocument();
    expect(screen.getByTestId("acknowledge-lb_identity_change")).toBeInTheDocument();
  });

  it("offers NO deploy button until every one is accepted — absent, not disabled", () => {
    const { rerender } = render(
      <DeployApproveStep
        report={destructive}
        acknowledged={new Set()}
        onApprove={noop}
        onCancel={noop}
      />
    );
    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();

    // One of two accepted: still no button.
    rerender(
      <DeployApproveStep
        report={destructive}
        acknowledged={new Set(["stateful_deletion"])}
        onApprove={noop}
        onCancel={noop}
      />
    );
    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();

    // Both accepted: now it exists.
    rerender(
      <DeployApproveStep
        report={destructive}
        acknowledged={new Set(["stateful_deletion", "lb_identity_change"])}
        onApprove={noop}
        onCancel={noop}
      />
    );
    expect(screen.getByTestId("deploy-approve-start")).toBeInTheDocument();
  });

  it("carries every accepted code on the approval", async () => {
    const onApprove = vi.fn();
    render(
      <DeployApproveStep
        report={destructive}
        acknowledged={new Set(["stateful_deletion", "lb_identity_change"])}
        onApprove={onApprove}
        onCancel={noop}
      />
    );

    await userEvent.click(screen.getByTestId("deploy-approve-start"));

    expect(onApprove).toHaveBeenCalledWith({
      approveDigest: PLAN_DIGEST,
      releaseVersion: PLAN_RELEASE,
      acknowledgedFindings: ["lb_identity_change", "stateful_deletion"],
    });
  });

  it("cannot be approved at all when an irreversible change has no name to accept", () => {
    const nameless = withFindings([
      { class: "stop", section: "stateful_deletions", subject: "mystery" },
    ]);

    render(
      <>
        <ApprovablePlanView
          report={nameless}
          acknowledged={new Set()}
          onAcknowledge={noop}
        />
        <DeployApproveStep
          report={nameless}
          acknowledged={new Set()}
          onApprove={noop}
          onCancel={noop}
        />
      </>
    );

    expect(screen.getByTestId("approvable-plan-unacknowledgeable")).toBeInTheDocument();
    expect(screen.queryByTestId("deploy-approve-start")).not.toBeInTheDocument();
  });

  it("treats an unrecognised class as needing acceptance, never as information", () => {
    // A newer forge's class we do not know. Covered by an ordinary approval it
    // would be exactly the "approved something nobody classified" failure.
    expect(findingClassOf("catastrophic")).toBe("stop");

    const unknownClass = withFindings([
      { code: "something_new", class: "catastrophic", subject: "x" },
    ]);
    expect(requiredAcknowledgements(unknownClass.deploy_plan)).toEqual(["something_new"]);
    expect(planIsApprovable(unknownClass, new Set())).toBe(false);
    expect(planIsApprovable(unknownClass, new Set(["something_new"]))).toBe(true);
  });
});

// ── plan_stale: re-render with what changed ─────────────────────────────────

describe("when the plan goes stale", () => {
  const approved: DeployPlan = {
    digest: PLAN_DIGEST,
    bundle_id: "bundle-1",
    release_version: PLAN_RELEASE,
    live_basis: { drift_observed: false },
    findings: [{ code: "image_changed", class: "info", section: "images", subject: "api" }],
  };

  const recomputed: DeployPlan = {
    digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
    bundle_id: "bundle-2",
    release_version: "20261003.120000-feedface0001",
    live_basis: { drift_observed: true },
    findings: [
      { code: "image_changed", class: "info", section: "images", subject: "api" },
      {
        code: "stateful_deletion",
        class: "stop",
        section: "stateful_deletions",
        subject: "postgres-data",
      },
    ],
  };

  it("says the environment was not touched", () => {
    render(<PlanStaleNotice approved={approved} current={recomputed} />);

    expect(screen.getByTestId("plan-stale")).toBeInTheDocument();
    expect(screen.getByText(/exactly as it was/i)).toBeInTheDocument();
  });

  it("shows what is different, including a newly-appeared irreversible change", () => {
    render(<PlanStaleNotice approved={approved} current={recomputed} />);

    expect(screen.getByTestId("plan-stale-diff")).toBeInTheDocument();
    expect(screen.getByTestId("plan-stale-added")).toBeInTheDocument();
    // The most important line on the screen: it now destroys something.
    expect(screen.getByTestId("plan-stale-new-stop")).toBeInTheDocument();
  });

  it("computes the difference over what the digest covers", () => {
    const change = diffPlans(approved, recomputed);

    expect(change.newStopCodes).toEqual(["stateful_deletion"]);
    expect(change.added).toHaveLength(1);
    expect(change.added[0]?.code).toBe("stateful_deletion");
    expect(change.releaseChanged).toEqual({
      from: PLAN_RELEASE,
      to: "20261003.120000-feedface0001",
    });
    expect(change.driftChanged).toEqual({ from: false, to: true });
  });

  it("reports no change for a detail-only difference, which cannot move the digest", () => {
    const sameButWordier: DeployPlan = {
      ...approved,
      findings: [
        {
          code: "image_changed",
          class: "info",
          section: "images",
          subject: "api",
          detail: "a longer explanation",
        },
      ],
    };

    const change = diffPlans(approved, sameButWordier);
    expect(change.added).toHaveLength(0);
    expect(change.removed).toHaveLength(0);
  });

  it("admits it cannot show the difference rather than implying there is none", () => {
    render(<PlanStaleNotice approved={approved} current={approved} />);

    expect(screen.getByTestId("plan-stale-opaque")).toBeInTheDocument();
  });

  it("requires a fresh acceptance of a stop finding that appeared after approval", () => {
    const staleReport = { ...planReport(), deploy_plan: recomputed };

    // The previous approval covered nothing destructive, so it cannot carry
    // over: the recomputed plan is unapprovable until the new code is accepted.
    expect(planIsApprovable(staleReport, new Set())).toBe(false);
    expect(planIsApprovable(staleReport, new Set(["stateful_deletion"]))).toBe(true);
  });
});

// ── Reading the document ────────────────────────────────────────────────────

describe("reading a plan-only document", () => {
  it("takes the release from what the plan stage actually cut", () => {
    expect(approvableRelease(planReport())).toBe(PLAN_RELEASE);
  });

  it("treats a missing digest as nothing to approve, not as an empty approval", () => {
    expect(approvableDigest({ env: "prod", deploy_plan: { digest: "  " } })).toBeNull();
    expect(approvableDigest({ env: "prod" })).toBeNull();
  });
});
