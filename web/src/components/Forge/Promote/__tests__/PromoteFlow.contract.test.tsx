// Copyright (c) 2025 Reliant Labs

/**
 * THE FLOW CONTRACT: the guard, the refusal, and the applied panel.
 *
 * These are the states that are painful to reach against a live daemon — a
 * refusal needs a concurrent promote, and an apply writes to a real ledger — so
 * PromoteFlow takes pure props and they are driven from fixtures here. Nothing in
 * this file touches a transport.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { PromoteFlow } from "../PromoteFlow";
import type { PromoteRefusal } from "@/services/forge/promote";
import { forwardPlan, meta, planOutcome, rollbackPlan } from "./fixtures";

function noop() {}

function baseProps() {
  return {
    isPlanning: false,
    isApplying: false,
    onConfirm: noop,
    onReplan: noop,
    onClose: noop,
  };
}

function refusal(overrides: Partial<PromoteRefusal> = {}): PromoteRefusal {
  return {
    reason: "stale-current-release",
    expectedCurrentRelease: "v1.3.0",
    expectedUnbound: false,
    actualBound: true,
    actualCurrentRelease: "v1.4.2",
    actualPromotedAt: "2026-09-10T13:59:00Z",
    detail: "staging is bound to v1.4.2, not v1.3.0 — someone promoted in between",
    ...overrides,
  };
}

describe("the confirm guard", () => {
  it("is not reachable without a rendered plan", () => {
    // Every non-report outcome, plus loading. None may offer a write: there is no
    // diff on screen, so there is nothing to have reviewed.
    const outcomes = [
      { kind: "not-forge-project" as const, meta: meta({ isForgeProject: false }) },
      { kind: "unsupported" as const, meta: meta({ supported: false }) },
      { kind: "unreachable" as const, meta: meta() },
      { kind: "malformed" as const, meta: meta(), raw: "{{" },
    ];

    for (const outcome of outcomes) {
      const view = render(<PromoteFlow {...baseProps()} planOutcome={outcome} />);
      expect(screen.queryByTestId("promote-confirm")).toBeNull();
      expect(screen.queryByTestId("promote-apply")).toBeNull();
      view.unmount();
    }

    // Still planning: no plan, no confirm.
    const loading = render(<PromoteFlow {...baseProps()} isPlanning planOutcome={undefined} />);
    expect(screen.getByTestId("promote-planning")).toBeTruthy();
    expect(screen.queryByTestId("promote-apply")).toBeNull();
    loading.unmount();

    // A transport failure: no confirm either.
    render(
      <PromoteFlow
        {...baseProps()}
        planOutcome={undefined}
        planError={new Error("daemon unavailable")}
      />
    );
    expect(screen.getByTestId("promote-plan-error")).toBeTruthy();
    expect(screen.queryByTestId("promote-apply")).toBeNull();
  });

  it("renders the diff ABOVE the confirm when a plan exists", () => {
    render(<PromoteFlow {...baseProps()} planOutcome={planOutcome(forwardPlan())} />);
    const plan = screen.getByTestId("promote-plan");
    const confirm = screen.getByTestId("promote-confirm");
    // The diff precedes the confirm in document order — the user cannot arrive at
    // the button without the diff having been rendered.
    expect(plan.compareDocumentPosition(confirm) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps the write disabled until the claim is acknowledged", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <PromoteFlow
        {...baseProps()}
        onConfirm={onConfirm}
        planOutcome={planOutcome(forwardPlan())}
      />
    );

    const apply = screen.getByTestId("promote-apply");
    expect(apply).toBeDisabled();
    await user.click(apply);
    expect(onConfirm).not.toHaveBeenCalled();

    await user.click(screen.getByTestId("promote-acknowledge"));
    expect(apply).toBeEnabled();
  });

  it("additionally requires typing the env name for a rollback", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <PromoteFlow
        {...baseProps()}
        onConfirm={onConfirm}
        planOutcome={planOutcome(rollbackPlan())}
      />
    );

    await user.click(screen.getByTestId("promote-acknowledge"));
    // Acknowledged, but the env name is still untyped.
    expect(screen.getByTestId("promote-apply")).toBeDisabled();

    await user.type(screen.getByTestId("promote-typed-env"), "prod");
    expect(screen.getByTestId("promote-apply")).toBeEnabled();
  });

  it("offers no write at all when the plan cannot produce a token", async () => {
    // `bound` absent: forge did not say what the env runs, so no claim can be
    // made and the write must be unavailable rather than defaulted.
    render(
      <PromoteFlow
        {...baseProps()}
        planOutcome={planOutcome(forwardPlan({ current: { release_known: false } }))}
      />
    );
    expect(screen.getByTestId("promote-no-token")).toBeTruthy();
    expect(screen.getByTestId("promote-apply")).toBeDisabled();
    expect(screen.queryByTestId("promote-acknowledge")).toBeNull();
  });

  it("passes the RENDERED plan object to onConfirm", async () => {
    // The token is derived from this object downstream, so the identity of what
    // is handed over is the guarantee that the claim matches the diff.
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const plan = forwardPlan();
    render(
      <PromoteFlow {...baseProps()} onConfirm={onConfirm} planOutcome={planOutcome(plan)} />
    );

    await user.click(screen.getByTestId("promote-acknowledge"));
    await user.click(screen.getByTestId("promote-apply"));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm.mock.calls[0][0]).toBe(plan);
  });
});

describe("the refusal path", () => {
  it("renders actual-vs-expected and offers a re-plan, not a generic error", async () => {
    const user = userEvent.setup();
    const onReplan = vi.fn();
    render(
      <PromoteFlow
        {...baseProps()}
        onReplan={onReplan}
        planOutcome={planOutcome(forwardPlan())}
        applyResult={{ kind: "refused", refusal: refusal() }}
      />
    );

    const notice = screen.getByTestId("promote-refusal");
    expect(notice.getAttribute("data-reason")).toBe("stale-current-release");

    // NOTHING WAS WRITTEN, stated in the heading.
    expect(screen.getByTestId("promote-refusal-heading").textContent).toMatch(/nothing was written/i);

    // The diff between what we claimed and what it found — the content of a
    // refusal.
    expect(screen.getByTestId("promote-refusal-expected").textContent).toContain("v1.3.0");
    expect(screen.getByTestId("promote-refusal-actual").textContent).toContain("v1.4.2");
    // When the binding it found was written: beaten by seconds, or a stale page.
    expect(screen.getByTestId("promote-refusal-when")).toBeTruthy();
    // Forge's own sentence.
    expect(screen.getByTestId("promote-refusal-detail").textContent).toContain("promoted in between");

    // It is NOT the generic apply-error panel.
    expect(screen.queryByTestId("promote-apply-error")).toBeNull();

    // A re-plan is offered, and the stale confirm is gone — the claim is known
    // bad, so it must not be re-sendable.
    expect(screen.queryByTestId("promote-apply")).toBeNull();
    await user.click(screen.getByTestId("promote-refusal-replan"));
    expect(onReplan).toHaveBeenCalledTimes(1);
  });

  it("renders a refusal that found NO binding as its own finding", () => {
    render(
      <PromoteFlow
        {...baseProps()}
        planOutcome={planOutcome(forwardPlan())}
        applyResult={{
          kind: "refused",
          refusal: refusal({ actualBound: false, actualCurrentRelease: "" }),
        }}
      />
    );
    // Not an empty release: "never promoted" is a different finding.
    expect(screen.getByTestId("promote-refusal-actual").textContent).toContain("not promoted");
  });

  it("keeps a thrown apply failure distinct from a refusal", () => {
    render(
      <PromoteFlow
        {...baseProps()}
        planOutcome={planOutcome(forwardPlan())}
        applyError={new Error("deadline exceeded")}
      />
    );
    const panel = screen.getByTestId("promote-apply-error");
    expect(screen.queryByTestId("promote-refusal")).toBeNull();
    // A timeout does not establish that nothing was written, and this must not
    // claim otherwise.
    expect(panel.textContent).toContain("not known");
    expect(panel.textContent).not.toMatch(/nothing was written/i);
  });
});

describe("after a successful apply", () => {
  it("shows the next-step deploy hint and does not claim a deploy happened", () => {
    const applied = forwardPlan({ dry_run: false, applied: true });
    render(
      <PromoteFlow
        {...baseProps()}
        planOutcome={planOutcome(forwardPlan())}
        applyResult={{ kind: "applied", outcome: planOutcome(applied) }}
      />
    );

    const panel = screen.getByTestId("promote-applied");

    // The next step is named explicitly, from the APPLIED document.
    expect(screen.getByTestId("promote-next-step").textContent).toBe("forge env deploy staging");
    expect(screen.getByTestId("promote-ships-nothing").textContent).toMatch(/nothing is deployed/i);

    // And nothing anywhere on this panel claims a deploy or a ship.
    const text = panel.textContent ?? "";
    expect(text).not.toMatch(/\bdeployed to\b/i);
    expect(text).not.toMatch(/\bshipped\b/i);
    expect(text).not.toMatch(/\bis now live\b/i);
    expect(text).not.toMatch(/\bdeploy succeeded\b/i);
    // It says what actually happened: a binding was updated.
    expect(screen.getByTestId("promote-applied-heading").textContent).toMatch(/binding updated/i);

    // The confirm is gone — the write is done and must not be re-issuable.
    expect(screen.queryByTestId("promote-apply")).toBeNull();
  });

  it("reports a written binding as written even when its report is unreadable", () => {
    render(
      <PromoteFlow
        {...baseProps()}
        planOutcome={planOutcome(forwardPlan())}
        applyResult={{
          kind: "applied",
          outcome: { kind: "malformed", meta: meta(), raw: "{{" },
        }}
      />
    );
    const panel = screen.getByTestId("promote-applied");
    // The binding IS written; this must not read as a failure.
    expect(panel.textContent).toMatch(/binding was written/i);
    expect(panel.textContent).toMatch(/nothing has been deployed/i);
  });
});
