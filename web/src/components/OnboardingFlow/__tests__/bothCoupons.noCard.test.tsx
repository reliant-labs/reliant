/**
 * THE ENGAGEMENT REQUIREMENT: a user holding both coupons never sees a card.
 *
 * ── Why this is its own file, and why it is a WALK ────────────────────
 *
 * Every individual piece of this is covered elsewhere — `requiresPayment` has
 * its own truth table, `deriveStep` is enumerated over 1920 states, and each
 * step has component tests. None of them can answer the question actually
 * being asked, which is about a JOURNEY: does a person who redeems a compute
 * code on step 1 and a credit code on step 2 ever get shown a card form?
 *
 * That could break in ways no single-unit test would notice. The most likely
 * one, and the reason this file exists: before this change the model step had
 * NO coupon field, so a user holding both codes had to walk onto the checkout
 * step — a screen whose purpose is collecting a card — to redeem the second.
 * Every unit was behaving correctly and the requirement was still violated.
 *
 * So this walks the flow the way the user does, moving the SERVER FACTS a
 * redemption would move, and asserts the card never appears at any point.
 */
import { describe, expect, it } from "vitest";

import { deriveStep, visibleStepsForPlan } from "../stepConfig";
import type { OnboardingFactsInput } from "../stepConfig";
import { requiresPayment } from "../requiresPayment";
import type { LaunchPlan } from "../types";

/** A brand-new account on a deployment that sells: nothing granted. */
const NEW_USER: OnboardingFactsInput = {
  computeEligible: false,
  walletFunded: false,
  reliantBillingAvailable: true,
};

interface World {
  plan: Partial<LaunchPlan>;
  facts: OnboardingFactsInput;
}

/**
 * Redeeming a compute coupon, as the server sees it.
 *
 * A grant makes the account eligible. It writes NO plan flag — the compute
 * step does not settle anything, it refetches — which is exactly why this is
 * safe: derivation reads the server fact, not a client claim.
 */
const redeemComputeCoupon = (world: World): World => ({
  ...world,
  facts: { ...world.facts, computeEligible: true },
});

/** Redeeming a credit coupon: the wallet is funded. Again, no plan flag. */
const redeemCreditCoupon = (world: World): World => ({
  ...world,
  facts: { ...world.facts, walletFunded: true },
});

describe("both coupons — no card is ever shown", () => {
  /**
   * The full walk, one action at a time.
   *
   * The assertion that matters is the one INSIDE the loop: at no point does
   * derivation route to `checkout`, which is the only step in onboarding that
   * can mount a card form.
   */
  it("never derives the checkout step for a user who redeems both codes", () => {
    let world: World = { plan: {}, facts: NEW_USER };
    const stepsSeen: string[] = [];
    const record = () => stepsSeen.push(deriveStep(world.plan, world.facts));

    // Step 1: the compute step. The user sees the prices, and redeems a
    // compute code right there — the field is on this step.
    record();
    expect(deriveStep(world.plan, world.facts)).toBe("compute");
    world = redeemComputeCoupon(world);

    // Choosing the hosted machine. No plan id is recorded: an entitled user is
    // not shown the tiles, because there is nothing left to buy.
    world = {
      ...world,
      plan: { ...world.plan, compute: "cloud_paid" },
    };

    // Step 2: the model step. Reliant's models, and the credit coupon redeemed
    // HERE — the field this change added, and the whole reason the flow can
    // stay card-free.
    record();
    expect(deriveStep(world.plan, world.facts)).toBe("model");
    world = redeemCreditCoupon(world);

    world = {
      ...world,
      plan: { ...world.plan, modelProvider: "reliant_credits" },
    };

    // And on. The next step must NOT be checkout.
    record();

    expect(
      requiresPayment(world.plan, world.facts).any,
      "both legs were granted by coupon, so nothing is owed",
    ).toBe(false);
    expect(stepsSeen).not.toContain("checkout");
    expect(deriveStep(world.plan, world.facts)).not.toBe("checkout");
  });

  // The progress bar must not ADVERTISE a payment step either. Listing one the
  // user will never reach promises a card that is never coming, which is its
  // own kind of dishonesty on an onboarding flow selling "no card needed".
  it("never lists a payment step in the progress bar", () => {
    const world: World = {
      plan: { compute: "cloud_paid", modelProvider: "reliant_credits" },
      facts: { ...NEW_USER, computeEligible: true, walletFunded: true },
    };
    expect(visibleStepsForPlan(world.plan, world.facts)).not.toContain(
      "checkout",
    );
  });

  /**
   * The counter-case, which is what stops the test above from passing
   * vacuously: redeem only ONE code and the checkout step DOES appear.
   *
   * Without this, a `deriveStep` that never returned "checkout" under any
   * circumstances would satisfy every assertion above while breaking payment
   * entirely.
   */
  it("still asks for payment when only the compute code was redeemed", () => {
    const world: World = {
      plan: { compute: "cloud_paid", modelProvider: "reliant_credits" },
      facts: { ...NEW_USER, computeEligible: true },
    };
    expect(requiresPayment(world.plan, world.facts).needsCredit).toBe(true);
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");
  });

  it("still asks for payment when only the credit code was redeemed", () => {
    const world: World = {
      plan: { compute: "cloud_paid", modelProvider: "reliant_credits" },
      facts: { ...NEW_USER, walletFunded: true },
    };
    expect(requiresPayment(world.plan, world.facts).needsCompute).toBe(true);
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");
  });

  /**
   * The other card-free route, which must keep working: own machine + own key.
   * No coupons involved, and nothing is ever owed.
   */
  it("shows no payment step for own-machine plus own-key", () => {
    const world: World = {
      plan: { compute: "local_daemon", modelProvider: "anthropic" },
      facts: NEW_USER,
    };
    expect(requiresPayment(world.plan, world.facts).any).toBe(false);
    expect(deriveStep(world.plan, world.facts)).not.toBe("checkout");
  });

  /**
   * BACK STILL WORKS from a coupon-settled state — the complaint that started
   * this whole piece of work ("maybe it's because i had added a coupon code
   * that i can't go back? but what if i want to change my compute size?").
   *
   * A coupon-entitled user who goes back to the compute step must actually
   * arrive there, and must not be bounced forward by a stale settlement.
   */
  it("lets a coupon-entitled user go back and change their machine", () => {
    const world: World = {
      plan: { compute: "cloud_paid" },
      facts: { ...NEW_USER, computeEligible: true, walletFunded: true },
    };
    // Back from the model step clears `compute`.
    const afterBack: World = { ...world, plan: {} };
    expect(deriveStep(afterBack.plan, afterBack.facts)).toBe("compute");
  });
});
