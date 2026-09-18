import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

/**
 * The owner's complaint: "for AI i thought we added ability to manage budgets,
 * limits, recurring spend?"
 *
 * It WAS added. `SetCurrentUserWalletAutoRecharge` has existed the whole time,
 * with hooks, a mandatory monthly ceiling and a documented failure ladder — and
 * exactly one caller, `OutOfCreditModal`, which fires only once the balance is
 * already empty. A budget you can only set after you have run out is not a
 * budget, which is why the feature read as missing.
 *
 * These tests pin the two things that make this control correct rather than
 * merely present:
 *
 *  - The monthly ceiling is MANDATORY. Compute's cap of 0 means uncapped; this
 *    one is rejected, and the difference must not be copied across.
 *  - A degraded rule renders FROM `failure_state`, not from `enabled`. The
 *    server keeps a declined rule enabled on every rung of the ladder, so a
 *    client that inferred failure from `enabled === false` would render a state
 *    that never occurs and miss every state that does.
 */

import {
  WalletAutoRechargeControl,
  describeFailure,
  type AutoRechargeRule,
} from "../WalletAutoRechargeControl";

const ARMED: AutoRechargeRule = {
  enabled: true,
  thresholdCents: 500,
  amountCents: 2500,
  maxPerMonthCents: 10000,
};

const CARD = { brand: "Visa", last4: "4242" };

function renderControl(
  overrides: Partial<
    React.ComponentProps<typeof WalletAutoRechargeControl>
  > = {},
) {
  const onSave = overrides.onSave ?? vi.fn();
  render(
    <WalletAutoRechargeControl
      rule={overrides.rule !== undefined ? overrides.rule : null}
      failureState={overrides.failureState ?? "ok"}
      lastError={overrides.lastError}
      spentThisMonthCents={overrides.spentThisMonthCents ?? null}
      card={overrides.card !== undefined ? overrides.card : CARD}
      dailySpendUsd={
        overrides.dailySpendUsd !== undefined ? overrides.dailySpendUsd : null
      }
      saving={overrides.saving}
      error={overrides.error}
      onSave={onSave}
      onAddCard={overrides.onAddCard}
    />,
  );
  return { onSave };
}

// ── The control exists, on the page, before anything goes wrong ───────

describe("the budget controls are reachable without running out of credit", () => {
  it("offers the three numbers the rule is made of", () => {
    renderControl();

    expect(
      screen.getByRole("radio", { name: /top up automatically/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText(/when my balance drops below/i),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/add this much credit/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/never spend more than/i)).toBeInTheDocument();
  });

  it("seeds the fields from the user's own burn rate rather than leaving them blank", () => {
    // $4/day. suggestRecharge targets a fortnight, rounded up to $5: $56 → $60,
    // ceiling 4x = $240, threshold ~3 days = $12.
    renderControl({ dailySpendUsd: 4 });

    expect(screen.getByLabelText(/add this much credit/i)).toHaveValue("60.00");
    expect(screen.getByLabelText(/never spend more than/i)).toHaveValue(
      "240.00",
    );
    expect(screen.getByLabelText(/when my balance drops below/i)).toHaveValue(
      "12.00",
    );
  });
});

// ── The submit: one call, every field, only on a click ────────────────

describe("arming the rule", () => {
  it("sends permission, threshold, amount and ceiling in ONE save", async () => {
    const user = userEvent.setup();
    const { onSave } = renderControl({ dailySpendUsd: 4 });

    await user.click(screen.getByRole("radio", { name: /top up automatically/i }));
    await user.click(screen.getByRole("button", { name: /save top-up/i }));

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({
      enabled: true,
      thresholdCents: 1200,
      amountCents: 6000,
      maxPerMonthCents: 24000,
    });
  });

  /**
   * This authorizes a RECURRING charge against a saved card, with nobody
   * present when it fires. Rendering the control must move no money and arm no
   * rule; only the button may.
   */
  it("saves nothing on mount, or on merely selecting the option", async () => {
    const user = userEvent.setup();
    const { onSave } = renderControl();
    expect(onSave).not.toHaveBeenCalled();

    await user.click(screen.getByRole("radio", { name: /top up automatically/i }));
    expect(onSave).not.toHaveBeenCalled();
  });

  it("will not arm a rule with no card to charge", () => {
    renderControl({ card: null });
    expect(screen.getByRole("button", { name: /save top-up/i })).toBeDisabled();
    expect(screen.getByText(/card on file before this can be turned on/i))
      .toBeInTheDocument();
  });
});

// ── The ceiling is mandatory. This is where compute's rule does NOT apply ──

describe("the monthly ceiling is mandatory, unlike compute's cap", () => {
  it("rejects a ceiling of zero rather than sending it as 'uncapped'", async () => {
    const user = userEvent.setup();
    const { onSave } = renderControl({ rule: ARMED });

    const ceiling = screen.getByLabelText(/never spend more than/i);
    await user.clear(ceiling);
    await user.type(ceiling, "0");

    expect(ceiling).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText(/an automatic charger must have a wall/i))
      .toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /save top-up/i }));
    expect(onSave).not.toHaveBeenCalled();
  });

  /**
   * A ceiling below one top-up is a rule that can never fire. The server would
   * store it; the user would arm it, see nothing happen, and have no way to
   * learn why.
   */
  it("rejects a ceiling smaller than a single top-up", async () => {
    const user = userEvent.setup();
    const { onSave } = renderControl({ rule: ARMED });

    const ceiling = screen.getByLabelText(/never spend more than/i);
    await user.clear(ceiling);
    await user.type(ceiling, "10");

    await user.click(screen.getByRole("button", { name: /save top-up/i }));
    expect(onSave).not.toHaveBeenCalled();
  });

  it("offers no 'no limit' option at all", () => {
    renderControl();
    // Compute has three options and one of them is uncapped. This has two,
    // because the server does not accept the third.
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    expect(screen.queryByRole("radio", { name: /no limit/i })).toBeNull();
  });
});

// ── The failure ladder ────────────────────────────────────────────────

describe("a degraded rule renders from failure_state, never from enabled", () => {
  /**
   * THE test. `enabled` is `true` on every rung — the server deliberately does
   * not disarm a failing rule, because a rule that silently switched itself off
   * would leave the user believing they were protected. So a client reading
   * `enabled` alone shows a healthy, armed rule to someone whose card is dead.
   */
  it("shows a declined rule as on AND broken, not as off", () => {
    renderControl({
      rule: ARMED, // enabled: true — as the server sends it while declining
      failureState: "declined",
      lastError: "Your card was declined.",
    });

    const alert = screen.getByTestId("auto-recharge-failure");
    expect(alert).toHaveAttribute("data-failure-state", "declined");
    expect(
      within(alert).getByText(/last automatic top-up was declined/i),
    ).toBeInTheDocument();
    // The server's own words, passed through rather than paraphrased.
    expect(
      within(alert).getByText("Your card was declined."),
    ).toBeInTheDocument();

    // And the rule still reads as ON, because it is.
    expect(
      screen.getByRole("radio", { name: /top up automatically/i }),
    ).toBeChecked();
  });

  it("distinguishes a bank verification request from a decline", () => {
    renderControl({ rule: ARMED, failureState: "requires_action" });
    expect(
      screen.getByText(/bank needs to verify/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/was declined/i)).toBeNull();
  });

  it("names the ceiling when the ceiling is what stopped it", () => {
    renderControl({ rule: ARMED, failureState: "ceiling_reached" });
    expect(screen.getByText(/\$100\.00 monthly limit/i)).toBeInTheDocument();
    expect(screen.getByText(/resumes next month/i)).toBeInTheDocument();
  });

  it("says nothing alarming about a healthy rule", () => {
    renderControl({ rule: ARMED, failureState: "ok" });
    expect(screen.queryByTestId("auto-recharge-failure")).toBeNull();
  });

  /**
   * An unrecognised rung is reported as healthy. A newer server inventing a
   * state we cannot explain should not produce a scary banner naming it — the
   * rule is still doing its job.
   */
  it("treats an unknown failure state as healthy rather than as an unknown alarm", () => {
    expect(describeFailure("some_future_rung", 10000)).toBeNull();
    expect(describeFailure("", 10000)).toBeNull();
    expect(describeFailure("ok", 10000)).toBeNull();
  });

  it("does not report a failure for a rule that is switched off", () => {
    renderControl({
      rule: { ...ARMED, enabled: false },
      failureState: "declined",
    });
    expect(screen.queryByTestId("auto-recharge-failure")).toBeNull();
  });
});

// ── Spend against the ceiling ─────────────────────────────────────────

describe("spend against the ceiling", () => {
  it("draws what auto-recharge has already committed this month", () => {
    renderControl({ rule: ARMED, spentThisMonthCents: 5000 });
    expect(screen.getByText(/\$50\.00 of \$100\.00/)).toBeInTheDocument();
  });

  it("draws nothing for a rule that is off", () => {
    renderControl({
      rule: { ...ARMED, enabled: false },
      spentThisMonthCents: 5000,
    });
    expect(screen.queryByText(/topped up automatically this month/i)).toBeNull();
  });
});
