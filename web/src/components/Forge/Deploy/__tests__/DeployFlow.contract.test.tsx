// Copyright (c) 2025 Reliant Labs

/**
 * THE FLOW CONTRACT: the guard, the four refusals, and the absence of escape
 * hatches.
 *
 * These are the states that cannot be reached against a live daemon without
 * deploying something to a real cluster — a stale-context refusal needs someone
 * to edit KCL mid-flow, an already-running refusal needs a concurrent apply — so
 * DeployFlow takes pure props and they are driven from fixtures. Nothing in this
 * file touches a transport.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DeployFlow } from "../DeployFlow";
import { DeployConfirmStep } from "../DeployConfirmStep";
import { deployTokenFor } from "@/services/forge/deploy";
import {
  devPlan,
  guardRefusedPlan,
  meta,
  noClusterPlan,
  planOutcome,
  prodPlan,
  refusal,
} from "./fixtures";

function noop() {}

function baseProps() {
  return {
    isPlanning: false,
    isStarting: false,
    onConfirm: noop,
    onReplan: noop,
    onClose: noop,
  };
}

describe("the confirm guard", () => {
  it("is not reachable without a rendered plan", () => {
    // Every non-report outcome, plus loading and a transport failure. None may
    // offer a write: there is no plan on screen, so there is nothing reviewed.
    const outcomes = [
      { kind: "not-forge-project" as const, meta: meta({ isForgeProject: false }) },
      { kind: "unsupported" as const, meta: meta({ supported: false }) },
      { kind: "unreachable" as const, meta: meta() },
      { kind: "malformed" as const, meta: meta(), raw: "{{" },
    ];

    for (const outcome of outcomes) {
      const view = render(<DeployFlow {...baseProps()} planOutcome={outcome} />);
      expect(screen.queryByTestId("deploy-confirm")).toBeNull();
      expect(screen.queryByTestId("deploy-start")).toBeNull();
      view.unmount();
    }

    const loading = render(<DeployFlow {...baseProps()} isPlanning planOutcome={undefined} />);
    expect(screen.getByTestId("deploy-planning")).toBeTruthy();
    expect(screen.queryByTestId("deploy-start")).toBeNull();
    loading.unmount();

    render(
      <DeployFlow
        {...baseProps()}
        planOutcome={undefined}
        planError={new Error("daemon unavailable")}
      />
    );
    expect(screen.getByTestId("deploy-plan-error")).toBeTruthy();
    expect(screen.queryByTestId("deploy-start")).toBeNull();
  });

  it("renders the plan ABOVE the confirm when one exists", () => {
    render(<DeployFlow {...baseProps()} planOutcome={planOutcome(prodPlan())} />);
    const plan = screen.getByTestId("deploy-plan");
    const confirm = screen.getByTestId("deploy-confirm");
    expect(plan.compareDocumentPosition(confirm) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("offers NO confirm at all — not a disabled one — when preflight blocks", () => {
    // dev's two unprovisioned secrets. The apply would fail, so there is no route
    // to it: the confirm step is absent, and the reason is rendered in its place.
    render(<DeployFlow {...baseProps()} planOutcome={planOutcome(devPlan())} />);

    expect(screen.queryByTestId("deploy-confirm")).toBeNull();
    expect(screen.queryByTestId("deploy-start")).toBeNull();
    expect(screen.queryByTestId("deploy-acknowledge")).toBeNull();

    const blocked = screen.getByTestId("deploy-blocked");
    expect(blocked.textContent).toMatch(/cannot be confirmed/i);
    expect(screen.getByTestId("deploy-blocker-preflight-blocking").textContent).toMatch(
      /would\s+fail/i
    );
  });

  it("offers no confirm when forge's own guard refused, and names every blocker", () => {
    render(<DeployFlow {...baseProps()} planOutcome={planOutcome(guardRefusedPlan())} />);
    expect(screen.queryByTestId("deploy-start")).toBeNull();
    expect(screen.getByTestId("deploy-blocker-guard-refused").textContent).toMatch(
      /will not deploy/i
    );
  });

  it("offers no confirm for an env that declares no cluster", () => {
    render(<DeployFlow {...baseProps()} planOutcome={planOutcome(noClusterPlan())} />);
    expect(screen.queryByTestId("deploy-start")).toBeNull();
    expect(screen.getByTestId("deploy-blocker-no-declared-cluster")).toBeTruthy();
  });

  it("offers no confirm from a document that is not a dry-run preview", () => {
    // An apply report describes a deploy that already happened; it cannot
    // authorise another.
    render(<DeployFlow {...baseProps()} planOutcome={planOutcome(prodPlan({ mode: "apply" }))} />);
    expect(screen.queryByTestId("deploy-start")).toBeNull();
    expect(screen.getByTestId("deploy-blocker-not-a-preview").textContent).toContain("apply");
  });

  it("confirms with the button alone, named for the environment", async () => {
    // NO TYPED CONTEXT AND NO CHECKBOX. The cluster is declared in KCL, so the
    // user never chose it and there is no wrong one to catch — see
    // DeployConfirmStep's header. The plan is the review; this is the approval.
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <DeployFlow {...baseProps()} onConfirm={onConfirm} planOutcome={planOutcome(prodPlan())} />
    );

    const start = screen.getByTestId("deploy-start");
    expect(start).toBeEnabled();
    expect(start.textContent).toBe("Deploy to prod");
    expect(screen.queryByTestId("deploy-typed-context")).toBeNull();
    expect(screen.queryByTestId("deploy-acknowledge")).toBeNull();

    await user.click(start);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("still binds the declared cluster in the token the request carries", () => {
    // The guard the typing used to advertise, asserted directly. The server
    // re-checks this, so a KCL that moved still refuses after the click.
    const token = deployTokenFor(prodPlan());
    expect(token?.expectedDeclaredContext).toBe("gke_reliant-labs-475814_us-central1_prod");
    expect(token?.expectedCurrentRelease).toBe("v1.5.15");
  });

  it("uses the one rule for a multi-cluster env too", () => {
    // devPlan blocks on preflight, so use a clean two-cluster plan to isolate this.
    const twoClusters = devPlan({
      preflight: { status: "ran", findings: [], blocking: 0 },
      ok: true,
      exit_code: 0,
    });
    render(<DeployConfirmStep plan={twoClusters} onConfirm={noop} onCancel={noop} />);
    expect(screen.getByTestId("deploy-start").textContent).toBe("Deploy to dev");
    expect(screen.queryByTestId("deploy-typed-context")).toBeNull();
    // The cluster list is information on the PLAN, not a hurdle at the click.
    expect(screen.queryByTestId("deploy-confirm-all-contexts")).toBeNull();
  });

  it("passes the RENDERED plan object to onConfirm", async () => {
    // The token — including the declared context — is derived from this object
    // downstream, so its identity is the guarantee the claim matches the screen.
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const plan = prodPlan();
    render(<DeployFlow {...baseProps()} onConfirm={onConfirm} planOutcome={planOutcome(plan)} />);

    await user.click(screen.getByTestId("deploy-start"));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm.mock.calls[0][0]).toBe(plan);
  });
});

describe("no escape hatches", () => {
  it("renders no skip-preflight, no-digest or force affordance anywhere in the flow", () => {
    // Both of forge's overrides are deliberately unreachable from a UI: they are
    // for a human at a terminal who has weighed the consequence, and a button is
    // a footgun. Asserted over the rendered DOM rather than over the request,
    // because a control that exists is the defect even before it is wired up.
    const { container } = render(
      <DeployFlow {...baseProps()} planOutcome={planOutcome(prodPlan())} />
    );

    const text = (container.textContent ?? "").toLowerCase();
    for (const phrase of ["skip preflight", "skip-preflight", "no-digest", "no digest", "force"]) {
      expect(text).not.toContain(phrase);
    }

    // And NO interactive control at all on a clean plan. The confirm step
    // defines none: the button is the whole confirmation. The only input this
    // flow can ever render is the destructive-findings tick, which requires a
    // finding reporting irreversible loss to exist.
    expect(container.querySelectorAll("input")).toHaveLength(0);
  });
});

describe("the four refusal reasons", () => {
  const cases = [
    {
      reason: "stale-declared-context" as const,
      expect: (text: string) => {
        expect(text).toMatch(/different cluster/i);
        // Leads with the cluster, because a wrong cluster is worse than a wrong
        // release — which is also why the server checks it first.
        expect(text).toMatch(/deploys to the declared context|never saw/i);
      },
    },
    {
      reason: "stale-current-release" as const,
      expect: (text: string) => {
        expect(text).toMatch(/bound release moved/i);
        expect(text).toMatch(/nobody looked at|pinned digests/i);
      },
    },
    {
      reason: "guard-refused" as const,
      expect: (text: string) => {
        expect(text).toMatch(/refused by forge/i);
        // Retrying does not help; the copy says so rather than offering hope.
        expect(text).toMatch(/will not change that until the kubeconfig does/i);
      },
    },
    {
      reason: "already-running" as const,
      expect: (text: string) => {
        expect(text).toMatch(/already in flight/i);
        expect(text).toMatch(/race each other/i);
      },
    },
  ];

  it("gives each reason its own actionable copy", () => {
    const seen = new Set<string>();
    for (const testCase of cases) {
      const view = render(
        <DeployFlow
          {...baseProps()}
          planOutcome={planOutcome(prodPlan())}
          startResult={{
            kind: "refused",
            refusal: refusal({ reason: testCase.reason, runningHandle: "dep-abc123" }),
          }}
        />
      );

      const notice = screen.getByTestId("deploy-refusal");
      expect(notice.getAttribute("data-reason")).toBe(testCase.reason);

      const heading = screen.getByTestId("deploy-refusal-heading").textContent ?? "";
      const explanation = screen.getByTestId("deploy-refusal-explanation").textContent ?? "";
      testCase.expect(`${heading} ${explanation}`);

      // Every refusal states that nothing was applied.
      expect(`${heading} ${explanation}`.toLowerCase()).toMatch(/nothing (new )?was applied/);

      // The stale confirm is gone — the claim is known bad and must not be
      // re-sendable.
      expect(screen.queryByTestId("deploy-start")).toBeNull();

      // The copy is genuinely distinct per reason, not one message with a label.
      expect(seen.has(heading)).toBe(false);
      seen.add(heading);

      view.unmount();
    }
    expect(seen.size).toBe(4);
  });

  it("offers to WATCH the existing handle on already_running, not to re-plan", async () => {
    const user = userEvent.setup();
    const onWatchRunning = vi.fn();
    const onReplan = vi.fn();
    render(
      <DeployFlow
        {...baseProps()}
        onReplan={onReplan}
        onWatchRunning={onWatchRunning}
        planOutcome={planOutcome(prodPlan())}
        startResult={{
          kind: "refused",
          refusal: refusal({ reason: "already-running", runningHandle: "dep-abc123" }),
        }}
      />
    );

    // The action is the running deploy, and the re-plan is NOT offered — a
    // re-plan here invites a second concurrent apply.
    expect(screen.queryByTestId("deploy-refusal-replan")).toBeNull();
    expect(screen.getByTestId("deploy-refusal-handle").textContent).toContain("dep-abc123");

    await user.click(screen.getByTestId("deploy-refusal-watch"));
    expect(onWatchRunning).toHaveBeenCalledWith("dep-abc123");
    expect(onReplan).not.toHaveBeenCalled();
  });

  it("renders the cluster diff on a stale-context refusal", () => {
    render(
      <DeployFlow
        {...baseProps()}
        planOutcome={planOutcome(prodPlan())}
        startResult={{ kind: "refused", refusal: refusal() }}
      />
    );
    expect(screen.getByTestId("deploy-refusal-expected-context").textContent).toContain(
      "gke_reliant-labs-475814_us-central1_prod"
    );
    expect(screen.getByTestId("deploy-refusal-actual-context").textContent).toContain(
      "k3d-control-plane"
    );
  });

  it("offers a re-plan for already_running when no handle was reported", () => {
    render(
      <DeployFlow
        {...baseProps()}
        onWatchRunning={vi.fn()}
        planOutcome={planOutcome(prodPlan())}
        startResult={{
          kind: "refused",
          refusal: refusal({ reason: "already-running", runningHandle: "" }),
        }}
      />
    );
    expect(screen.queryByTestId("deploy-refusal-watch")).toBeNull();
    expect(screen.getByTestId("deploy-refusal-replan")).toBeTruthy();
    expect(screen.getByTestId("deploy-refusal").textContent).toMatch(/cannot be watched/i);
  });
});

describe("a start that did not start anything", () => {
  it("keeps a thrown failure distinct from a refusal, and claims neither outcome", () => {
    render(
      <DeployFlow
        {...baseProps()}
        planOutcome={planOutcome(prodPlan())}
        startError={new Error("deadline exceeded")}
      />
    );
    const panel = screen.getByTestId("deploy-start-error");
    expect(screen.queryByTestId("deploy-refusal")).toBeNull();
    // A timeout does not establish that nothing was applied.
    expect(panel.textContent).toMatch(/not known/i);
    expect(panel.textContent).not.toMatch(/nothing was applied/i);
  });

  it("says a not-started reply applied nothing", () => {
    render(
      <DeployFlow
        {...baseProps()}
        planOutcome={planOutcome(prodPlan())}
        startResult={{
          kind: "not-started",
          outcome: {
            kind: "unsupported",
            meta: meta({ supported: false, unsupportedReason: "forge v0.1.2 has no env deploy --json" }),
          },
        }}
      />
    );
    const panel = screen.getByTestId("deploy-not-started");
    expect(panel.textContent).toMatch(/nothing was applied/i);
    expect(panel.textContent).toContain("forge v0.1.2");
  });
});
