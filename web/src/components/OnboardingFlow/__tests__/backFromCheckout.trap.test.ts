/**
 * Back from the payment step must land somewhere the user can act.
 *
 * ── The trap ──────────────────────────────────────────────────────────
 *
 * `BACK_CLEARS.checkout` drops `computePlanId` — correctly, since a plan
 * chosen for a compute option the user is about to reconsider should not
 * survive. But it does NOT clear `compute`, and `deriveStep` routes on
 * `compute` + `modelProvider`. Clearing `modelProvider` therefore lands the
 * user on the MODEL step, which is not where the machine was chosen.
 *
 * The compute step is where the plan tiles live. So after one Back the user is
 * on a step that cannot set `computePlanId`, and their only forward move
 * (re-pick a model) derives them straight back to checkout — now with no plan
 * id at all. That is the "we couldn't load the machine plans" the owner
 * reported, reached without touching a URL: the message blames the catalog for
 * a plan id that Back deleted.
 *
 * The fix is that going back from payment returns to the decision that CREATED
 * the cost. Compute is a leg of that bill, so reconsidering the bill has to be
 * able to reconsider the machine.
 */
import { describe, expect, it } from "vitest";

import { BACK_CLEARS, deriveStep } from "../stepConfig";
import type { LaunchPlan } from "../types";

/** Apply a step's Back exactly as OnboardingPage.onBack does. */
function goBack(
  plan: Partial<LaunchPlan>,
  step: keyof typeof BACK_CLEARS,
): Partial<LaunchPlan> {
  const next = { ...plan };
  for (const field of BACK_CLEARS[step]) delete next[field];
  return next;
}

/** A new account: nothing is entitled, so a cloud plan owes money. */
const NEW_ACCOUNT = {
  computeEligible: false,
  walletFunded: false,
  reliantBillingAvailable: true,
};

describe("Back from the checkout step", () => {
  // The plan the owner had: a hosted machine they picked and priced, paid for
  // with their own model key, so only the compute leg is owed.
  const AT_CHECKOUT: Partial<LaunchPlan> = {
    compute: "cloud_paid",
    computePlanId: "plan_compute_small",
    modelProvider: "anthropic",
  };

  it("is on the checkout step to begin with", () => {
    expect(deriveStep(AT_CHECKOUT, NEW_ACCOUNT)).toBe("checkout");
  });

  // THE BUG. Back drops the plan id, so if derivation does not also return to
  // the step that sets one, the user cannot restore it by any forward move.
  it("returns to a step that can re-choose the machine it just un-chose", () => {
    const afterBack = goBack(AT_CHECKOUT, "checkout");

    // Back cleared the plan id...
    expect(afterBack.computePlanId).toBeUndefined();

    // ...so it must land on the step that owns the plan tiles. Landing on
    // `model` strands the user: the tiles are not there, and choosing a
    // provider derives straight back to checkout with no plan id.
    expect(deriveStep(afterBack, NEW_ACCOUNT)).toBe("compute");
  });

  // The trap, stated as the property that actually matters: no sequence of
  // Backs and forward moves can produce a cloud plan sitting on checkout with
  // no machine chosen. Asserting on the walk rather than on one state is the
  // point — the stranded plan is only reachable through a Back, so a state
  // check in isolation would pass against the broken code.
  it("cannot be walked into a payment screen for an unchosen machine", () => {
    let plan = AT_CHECKOUT;

    // Back to the machine question, then forward again the only way there is.
    plan = goBack(plan, "checkout");
    expect(deriveStep(plan, NEW_ACCOUNT)).toBe("compute");

    // Re-choosing compute re-records a plan id (the compute step writes one
    // for an un-entitled user), so the bill is priced again by the time
    // checkout is derived.
    plan = { ...plan, compute: "cloud_paid", computePlanId: "plan_compute_small" };
    expect(deriveStep(plan, NEW_ACCOUNT)).toBe("model");

    plan = { ...plan, modelProvider: "anthropic" };
    expect(deriveStep(plan, NEW_ACCOUNT)).toBe("checkout");
    expect(plan.computePlanId).toBe("plan_compute_small");
  });

  // Back must remain able to reach the first step. A Back that clears compute
  // has to clear the auto-skip flag with it, or the flag outlives the field it
  // describes — the same defect class the model step's Back already fixed.
  it("clears the auto-skip flag alongside compute", () => {
    expect(BACK_CLEARS.checkout).toContain("compute");
    expect(BACK_CLEARS.checkout).toContain("computeAutoSkipped");
  });

  // Back now returns to the machine question for every plan, local included.
  //
  // That is a deliberate widening: `compute` joined BACK_CLEARS.checkout, so
  // one Back re-opens both decisions that can cost money rather than only the
  // model one. For a local plan the compute step auto-skips when a daemon is
  // already connected, so the user is not asked a question they answered — and
  // when there is no daemon, being returned to that choice is correct, since
  // it is the other half of the bill they just backed out of.
  it("returns a local plan to the machine question too", () => {
    const localCredits: Partial<LaunchPlan> = {
      compute: "local_daemon",
      modelProvider: "reliant_credits",
      aiCreditCents: 2000,
    };
    expect(deriveStep(localCredits, NEW_ACCOUNT)).toBe("checkout");

    const afterBack = goBack(localCredits, "checkout");
    expect(deriveStep(afterBack, NEW_ACCOUNT)).toBe("compute");
    // The credit selection made on the checkout step is gone with it.
    expect(afterBack.aiCreditCents).toBeUndefined();
  });
});
