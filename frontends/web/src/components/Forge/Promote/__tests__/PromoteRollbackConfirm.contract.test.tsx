// Copyright (c) 2025 Reliant Labs

/**
 * THE ROLLBACK'S TYPED ENV NAME IS GONE, and the button carries the decision.
 *
 * The typed name was the one piece of friction scoped to rollbacks, on the
 * reasoning that a backwards move is not what a skimming reviewer expects. The
 * expectation was right; the remedy was not. The env name is already in the
 * dialog's own heading, so transcribing it proved nothing about having read the
 * diff — and it sat immediately below a checkbox asserting the same thing,
 * which is two reflexes stacked rather than one decision.
 *
 * What survives is what actually distinguishes a rollback: the destructive
 * styling, the sentence saying it moves the environment backwards, and a button
 * that names BOTH the environment and the version it is going to — "Roll back
 * prod to v1.4.0". Reading that button is reading the decision.
 *
 * A FORWARD PROMOTE IS UNCHANGED BY THIS FILE. It keeps its acknowledgement
 * checkbox, because a promote overwrites a binding forge keeps no history of
 * and the claim being acknowledged ("prod is currently bound to v1.5.0") is a
 * fact about state the user cannot otherwise be shown to have seen.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { PromoteConfirmStep } from "../PromoteConfirmStep";
import { forwardPlan, rollbackPlan } from "./fixtures";

describe("a rollback confirm", () => {
  it("renders no typed-env input", () => {
    render(<PromoteConfirmStep plan={rollbackPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />);

    expect(screen.queryByTestId("promote-typed-env")).toBeNull();
    const confirm = screen.getByTestId("promote-confirm");
    expect(confirm.textContent).not.toMatch(/type .* to confirm/i);
  });

  it("names the environment AND the version it rolls back to, on the button", () => {
    render(<PromoteConfirmStep plan={rollbackPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(screen.getByTestId("promote-apply").textContent).toMatch(
      /^Roll back prod to v[\d.]+$/
    );
  });

  it("still says, in words, that this moves the environment backwards", () => {
    render(<PromoteConfirmStep plan={rollbackPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(screen.getByTestId("promote-confirm").textContent).toMatch(/moves prod backwards/i);
  });

  it("needs only the acknowledgement, then the button", async () => {
    const onConfirm = vi.fn();
    render(<PromoteConfirmStep plan={rollbackPlan()} onConfirm={onConfirm} onCancel={vi.fn()} />);

    const apply = screen.getByTestId("promote-apply");
    expect(apply).toBeDisabled();

    await userEvent.click(screen.getByTestId("promote-acknowledge"));
    expect(apply).toBeEnabled();
    await userEvent.click(apply);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("leaves exactly one input in the dialog: the acknowledgement", () => {
    const { container } = render(
      <PromoteConfirmStep plan={rollbackPlan()} onConfirm={vi.fn()} onCancel={vi.fn()} />
    );
    const inputs = [...container.querySelectorAll("input")];
    expect(inputs).toHaveLength(1);
    expect(inputs[0].getAttribute("data-testid")).toBe("promote-acknowledge");
    expect(inputs[0].getAttribute("type")).toBe("checkbox");
  });
});

describe("a forward promote is untouched", () => {
  it("keeps its acknowledgement and its own button label", async () => {
    const onConfirm = vi.fn();
    render(<PromoteConfirmStep plan={forwardPlan()} onConfirm={onConfirm} onCancel={vi.fn()} />);

    const apply = screen.getByTestId("promote-apply");
    expect(apply).toBeDisabled();
    // forwardPlan promotes staging; the label names whichever env the plan did.
    expect(apply.textContent).toMatch(/^Promote staging to v/);
    expect(screen.queryByTestId("promote-typed-env")).toBeNull();

    await userEvent.click(screen.getByTestId("promote-acknowledge"));
    expect(apply).toBeEnabled();
  });

  it("still offers no write when the plan cannot produce a token", () => {
    render(
      <PromoteConfirmStep
        plan={forwardPlan({ current: { release_known: false } })}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByTestId("promote-no-token")).toBeTruthy();
    expect(screen.getByTestId("promote-apply")).toBeDisabled();
    expect(screen.queryByTestId("promote-acknowledge")).toBeNull();
  });
});
