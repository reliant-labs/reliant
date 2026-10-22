/**
 * Payment is owed only when RELIANT is the one being billed.
 *
 * The owner's rule: "only conditionally do billing if either reliant AI, or
 * reliant compute is chosen." The (compute, provider) half of that was already
 * right — hosted compute owes, `reliant_credits` owes, and a user on their own
 * machine with their own key owes nothing. What was missing is the DEPLOYMENT
 * half of the same question.
 *
 * A build with no control plane has no hosted compute to sell and no Reliant
 * models to draw on: `capabilities.cloudDaemons` and `capabilities.managedCredits`
 * are both false, and every billing RPC throws rather than answering. Nothing
 * in `requiresPayment` knew that, so its facts read pessimistic (the deliberate
 * unknown ⇒ "you owe money" rule) and derivation routed to a checkout step
 * that cannot mint a Stripe session and cannot list a plan. That dead end is
 * what the "No plans are available in this setup" copy was describing.
 *
 * So: Reliant cannot bill for what Reliant does not sell. When billing is
 * unavailable the requirement is false — not pessimistic — because this is a
 * BUILD CONSTANT rather than an in-flight query. There is no race to lose and
 * therefore no reason to err toward a screen that cannot take money.
 *
 * This does NOT relax the pessimism that matters. Where Reliant genuinely does
 * bill, an unknown eligibility or wallet balance still reads as "owes" exactly
 * as before — see the enumeration in requiresPayment.test.ts.
 */
import { describe, expect, it } from "vitest";

import { requiresPayment, type PaymentFacts } from "../requiresPayment";

/** Reliant sells here, and the user has bought nothing yet. */
const BILLABLE_BROKE: PaymentFacts = {
  computeEligible: false,
  walletFunded: false,
  reliantBillingAvailable: true,
};

/** A build with no control plane: nothing of Reliant's is for sale. */
const NOT_BILLABLE: PaymentFacts = {
  computeEligible: false,
  walletFunded: false,
  reliantBillingAvailable: false,
};

describe("requiresPayment — Reliant only bills for what Reliant sells", () => {
  it("owes nothing for hosted compute when this build cannot sell it", () => {
    // The control is the same plan against a billable deployment: it DOES owe.
    expect(
      requiresPayment(
        { compute: "cloud_paid", modelProvider: "anthropic" },
        BILLABLE_BROKE,
      ).needsCompute,
    ).toBe(true);

    expect(
      requiresPayment(
        { compute: "cloud_paid", modelProvider: "anthropic" },
        NOT_BILLABLE,
      ),
    ).toEqual({ needsCompute: false, needsCredit: false, any: false });
  });

  it("owes nothing for Reliant's models when this build cannot sell them", () => {
    expect(
      requiresPayment(
        { compute: "local_daemon", modelProvider: "reliant_credits" },
        BILLABLE_BROKE,
      ).needsCredit,
    ).toBe(true);

    expect(
      requiresPayment(
        { compute: "local_daemon", modelProvider: "reliant_credits" },
        NOT_BILLABLE,
      ),
    ).toEqual({ needsCompute: false, needsCredit: false, any: false });
  });

  it("owes nothing for a plan that would otherwise owe BOTH legs", () => {
    expect(
      requiresPayment(
        { compute: "cloud_paid", modelProvider: "reliant_credits" },
        BILLABLE_BROKE,
      ).any,
    ).toBe(true);

    expect(
      requiresPayment(
        { compute: "cloud_paid", modelProvider: "reliant_credits" },
        NOT_BILLABLE,
      ).any,
    ).toBe(false);
  });

  // The guarantee this change must not cost us. Availability suppresses the
  // bill ONLY where Reliant is not the seller; where it is, an unknown fact is
  // still a debt, which is what stops a race walking an unpaid user past the
  // one screen that charges.
  it("still reads pessimistically when Reliant IS the seller", () => {
    expect(requiresPayment({ compute: "cloud_paid" }, BILLABLE_BROKE).any).toBe(
      true,
    );
    expect(
      requiresPayment({ modelProvider: "reliant_credits" }, BILLABLE_BROKE).any,
    ).toBe(true);
  });

  // The free path is free on every deployment, which is the one case that must
  // be identical either way.
  it("owes nothing for local compute and your own key, on any deployment", () => {
    for (const facts of [BILLABLE_BROKE, NOT_BILLABLE]) {
      expect(
        requiresPayment(
          { compute: "local_daemon", modelProvider: "anthropic" },
          facts,
        ).any,
      ).toBe(false);
    }
  });
});
