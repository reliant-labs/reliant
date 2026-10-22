// The deploy budget-cap control's contract, at the component boundary.
//
// Ported from control-plane internal-console's
// `src/components/usage/budget-cap-control.test.tsx`, assertions unchanged.
//
// usageModel.test.ts proves buildOverageRequest is total. This file proves the
// COMPONENT actually routes every submit through it — which is the half that
// regresses, because the tempting refactor ("only send what changed") is
// invisible in the model and fatal in the form.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { BudgetCapControl } from "@/components/Settings/cloud/usage/BudgetCapControl";

import type { OverageChoice } from "@/components/Settings/cloud/usage/usageModel";

afterEach(cleanup);

function renderControl(value: OverageChoice, onSubmit = vi.fn()) {
  render(<BudgetCapControl value={value} onSubmit={onSubmit} />);
  return onSubmit;
}

function submit() {
  fireEvent.click(screen.getByRole("button", { name: /save spending cap/i }));
}

// Anchored so the two option labels stay distinguishable, and so the cap INPUT
// is not confused with the radio description that mentions it.
const cappedRadio = () => screen.getByRole("radio", { name: /^set a ceiling$/i });
const uncappedRadio = () => screen.getByRole("radio", { name: /^no ceiling$/i });
const capInput = () => screen.getByRole("spinbutton", { name: /^monthly ceiling$/i });
const queryCapInput = () =>
  screen.queryByRole("spinbutton", { name: /^monthly ceiling$/i });

describe("BudgetCapControl — the partial-send hazard", () => {
  it("sends the user's current cap on every submit, even when untouched", () => {
    // THE test that prevents silently uncapping someone. The user opens the
    // page with a $25 cap and presses Save without editing the amount. If the
    // form sent only `enabled`, the server would replace the cap with nothing
    // and the user would be uncapped with no indication anywhere.
    const onSubmit = renderControl({ kind: "capped", budgetCents: 2500 });

    submit();

    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onSubmit).toHaveBeenCalledWith({ budgetCents: 2500n });
  });

  it("includes budget_cents when the user removes their ceiling", () => {
    const onSubmit = renderControl({ kind: "capped", budgetCents: 2500 });

    fireEvent.click(uncappedRadio());
    submit();

    // Explicit 0, not an omitted field: both read as "no cap" to the server,
    // but only one is distinguishable from a dropped field.
    expect(onSubmit).toHaveBeenCalledWith({ budgetCents: 0n });
  });

  it("sends the newly typed cap, converted from dollars to cents", () => {
    const onSubmit = renderControl({ kind: "uncapped" });

    fireEvent.click(cappedRadio());
    fireEvent.change(capInput(), { target: { value: "42.50" } });
    submit();

    expect(onSubmit).toHaveBeenCalledWith({ budgetCents: 4250n });
  });

  it("never submits a request object missing budgetCents", () => {
    const onSubmit = renderControl({ kind: "capped", budgetCents: 100 });
    submit();
    const fields = onSubmit.mock.calls[0]?.[0];
    expect(fields).toBeDefined();
    expect(Object.hasOwn(fields as object, "budgetCents")).toBe(true);
  });
});

describe("BudgetCapControl — 'no cap' is distinguishable from 'cap of 0'", () => {
  it("names the uncapped state in words rather than showing an empty field", () => {
    renderControl({ kind: "uncapped" });

    expect(screen.getByText(/no spending ceiling/i)).toBeDefined();
    // The cap input is not even rendered in this mode, so there is no blank
    // box to misread as "a cap I have not filled in yet".
    expect(queryCapInput()).toBeNull();
  });

  it("does not render an uncapped account as $0.00", () => {
    renderControl({ kind: "uncapped" });
    expect(screen.queryByText(/\$0\.00/)).toBeNull();
  });

  it("renders a real cap as a dollar amount", () => {
    renderControl({ kind: "capped", budgetCents: 2500 });
    expect(screen.getByText(/ceiling of \$25\.00 per month/i)).toBeDefined();
  });

  it("refuses to save a cap of zero, since zero would mean uncapped", () => {
    // Two defences cover this, and the one that matters is that NOTHING is
    // sent: a saved 0 would be stored as "no cap", silently uncapping a user
    // who believed they had just capped themselves at zero spend. The input's
    // own min constraint stops this one before the handler runs.
    const onSubmit = renderControl({ kind: "uncapped" });

    fireEvent.click(cappedRadio());
    fireEvent.change(capInput(), { target: { value: "0" } });
    submit();

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("explains why an empty cap cannot be saved rather than silently doing nothing", () => {
    // An empty field passes the browser's own constraint check (it is not
    // `required`), so this is the path the component's guard has to catch.
    const onSubmit = renderControl({ kind: "uncapped" });

    fireEvent.click(cappedRadio());
    fireEvent.change(capInput(), { target: { value: "" } });
    submit();

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/above \$0\.00/i);
  });

  it("offers no 'turn overage off' option, because the platform has none", () => {
    // A running deployment accrues usage whether or not anyone opted in, so an
    // off switch would promise something that does not exist. This is the one
    // real difference from ComputeOverageControl, which has three options
    // because a machine genuinely can refuse to start.
    renderControl({ kind: "uncapped" });
    expect(screen.queryByRole("radio", { name: /off|disable/i })).toBeNull();
    expect(screen.getAllByRole("radio")).toHaveLength(2);
  });
});

describe("BudgetCapControl — no subscription", () => {
  it("explains why a cap cannot be set instead of offering a dead button", () => {
    render(
      <BudgetCapControl
        value={{ kind: "uncapped" }}
        onSubmit={vi.fn()}
        disabledReason="No compute plan on this account."
      />,
    );

    expect(screen.getByText(/no compute plan on this account/i)).toBeDefined();
    expect(screen.queryByRole("button", { name: /save spending cap/i })).toBeNull();
  });
});
