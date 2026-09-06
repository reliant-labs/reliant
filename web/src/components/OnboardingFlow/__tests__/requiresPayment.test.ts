/**
 * Exhaustive truth table for `requiresPayment`.
 *
 * The function is pure over a small closed domain — 5 compute choices × 8
 * providers × 4 fact combinations = 160 states — so it is enumerated rather
 * than sampled, in the style of `deriveStep.enumeration.test.ts`. A
 * hand-written fixture set only covers the cases someone thought of, and the
 * expensive mistakes here are the ones nobody thought of: a user shown a
 * payment screen they do not owe, or walked past one they do.
 *
 * The independent oracle below is deliberately written from the PRODUCT rule
 * ("hosted compute with no entitlement costs money; Reliant's models against
 * an empty wallet cost money") rather than by restating the implementation, so
 * the two can actually disagree.
 */
import { describe, expect, it } from "vitest";

import { requiresPayment, type PaymentFacts } from "../requiresPayment";
import type { ComputeChoice, LaunchPlan, ModelProvider } from "../types";

const COMPUTE_VALUES: (ComputeChoice | undefined)[] = [
  undefined,
  "cloud_free_trial",
  "cloud_paid",
  "local_daemon",
  "undecided",
];

const MODEL_PROVIDER_VALUES: (ModelProvider | undefined)[] = [
  undefined,
  "reliant_credits",
  "openai",
  "anthropic",
  "openrouter",
  "copilot",
  "other",
  "not_configured",
];

const FACT_VALUES: PaymentFacts[] = [
  { computeEligible: false, walletFunded: false },
  { computeEligible: true, walletFunded: false },
  { computeEligible: false, walletFunded: true },
  { computeEligible: true, walletFunded: true },
];

/** The rule, restated from the product and not from the implementation. */
const HOSTED = new Set(["cloud_free_trial", "cloud_paid"]);

function oracle(
  compute: ComputeChoice | undefined,
  provider: ModelProvider | undefined,
  facts: PaymentFacts,
) {
  const runsOnOurHardware = compute !== undefined && HOSTED.has(compute);
  const usesOurModels = provider === "reliant_credits";
  const needsCompute = runsOnOurHardware && !facts.computeEligible;
  const needsCredit = usesOurModels && !facts.walletFunded;
  return { needsCompute, needsCredit, any: needsCompute || needsCredit };
}

describe("requiresPayment — full enumeration", () => {
  it("agrees with the product rule across all 160 states", () => {
    const offenders: string[] = [];
    let count = 0;
    for (const compute of COMPUTE_VALUES) {
      for (const modelProvider of MODEL_PROVIDER_VALUES) {
        for (const facts of FACT_VALUES) {
          count += 1;
          const plan: Partial<LaunchPlan> = {};
          if (compute) plan.compute = compute;
          if (modelProvider) plan.modelProvider = modelProvider;

          const actual = requiresPayment(plan, facts);
          const expected = oracle(compute, modelProvider, facts);
          if (
            actual.needsCompute !== expected.needsCompute ||
            actual.needsCredit !== expected.needsCredit ||
            actual.any !== expected.any
          ) {
            offenders.push(
              `compute=${compute} provider=${modelProvider} eligible=${facts.computeEligible} funded=${facts.walletFunded}: ` +
                `got ${JSON.stringify(actual)} want ${JSON.stringify(expected)}`,
            );
          }
        }
      }
    }
    expect(offenders).toEqual([]);
    expect(count).toBe(160);
  });

  it("is capable of disagreeing with the oracle", () => {
    // Guards the guard. If the loop above can only ever pass, it tests
    // nothing — so prove the oracle rejects a wrong answer.
    const facts: PaymentFacts = { computeEligible: false, walletFunded: true };
    expect(oracle("local_daemon", "reliant_credits", facts).any).toBe(false);
    expect(oracle("cloud_paid", "anthropic", facts).any).toBe(true);
  });
});

describe("requiresPayment — the cases the product turns on", () => {
  const broke: PaymentFacts = { computeEligible: false, walletFunded: false };
  const entitled: PaymentFacts = { computeEligible: true, walletFunded: true };

  // THE FREE PATH, and per inherited fact 1 it is the ONLY one: a new account
  // gets no trial and no signup credit, so nothing else through this wizard is
  // free. If this case ever starts owing money, onboarding has no free path at
  // all and that is a product decision, not a refactor.
  it("owes nothing for local compute with your own key", () => {
    expect(
      requiresPayment(
        { compute: "local_daemon", modelProvider: "anthropic" },
        broke,
      ),
    ).toEqual({ needsCompute: false, needsCredit: false, any: false });
  });

  // The common cloud case. A brand-new account has no subscription, so this is
  // what essentially every first-time cloud user hits.
  it("owes compute for a new user choosing cloud with their own key", () => {
    const result = requiresPayment(
      { compute: "cloud_paid", modelProvider: "anthropic" },
      broke,
    );
    expect(result.needsCompute).toBe(true);
    expect(result.needsCredit).toBe(false);
  });

  it("owes credit for local compute with Reliant's models and an empty wallet", () => {
    const result = requiresPayment(
      { compute: "local_daemon", modelProvider: "reliant_credits" },
      broke,
    );
    expect(result.needsCompute).toBe(false);
    expect(result.needsCredit).toBe(true);
  });

  it("owes both when the user picked cloud AND Reliant's models", () => {
    expect(
      requiresPayment(
        { compute: "cloud_paid", modelProvider: "reliant_credits" },
        broke,
      ),
    ).toEqual({ needsCompute: true, needsCredit: true, any: true });
  });

  // A redeemed compute coupon makes `computeEligible` true. Nothing is owed
  // and the checkout step must never appear.
  it("owes nothing once a coupon or subscription has made compute eligible", () => {
    expect(
      requiresPayment(
        { compute: "cloud_free_trial", modelProvider: "anthropic" },
        { computeEligible: true, walletFunded: false },
      ).any,
    ).toBe(false);
  });

  it("owes nothing when everything is already entitled", () => {
    expect(
      requiresPayment(
        { compute: "cloud_paid", modelProvider: "reliant_credits" },
        entitled,
      ).any,
    ).toBe(false);
  });

  // Both cloud tiers are the same purchase question. `cloud_paid` reading as
  // local is the defect class the enumeration test was written for.
  it("treats every cloud tier identically", () => {
    for (const compute of ["cloud_free_trial", "cloud_paid"] as const) {
      expect(
        requiresPayment({ compute, modelProvider: "anthropic" }, broke)
          .needsCompute,
        compute,
      ).toBe(true);
    }
  });

  // An unanswered question cannot cost money — and `undecided` is a real
  // ComputeChoice the schema accepts, not a synonym for absent.
  it("owes nothing before the user has chosen", () => {
    expect(requiresPayment({}, broke).any).toBe(false);
    expect(requiresPayment({ compute: "undecided" }, broke).any).toBe(false);
    expect(
      requiresPayment({ modelProvider: "openai" }, broke).any,
    ).toBe(false);
  });
});
