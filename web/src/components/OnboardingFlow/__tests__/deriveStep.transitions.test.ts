/**
 * The onboarding step machine, tested over TRANSITIONS rather than states.
 *
 * ── Why this file exists next to the enumeration ──────────────────────
 *
 * `deriveStep.enumeration.test.ts` enumerates 1920 STATES and asks whether each
 * one derives the right step. That is a strong test and it missed a live bug
 * with money attached, because the defect was not in any state — it was in what
 * a state MEANT after a particular path was walked to reach it:
 *
 *   1. Local compute + Reliant's models, empty wallet → checkout. The user buys
 *      AI credit. The plan records that the bill was settled.
 *   2. Back from project-picker clears `modelProvider`.
 *   3. Back from model clears `compute`.
 *   4. The user now picks CLOUD compute — a monthly subscription they have
 *      never paid for.
 *
 * The old `plan.paid` recorded a verdict ("the bills were settled") without
 * recording WHAT was bought, and nothing invalidated it when the bill changed
 * underneath. `checkoutIsOwed` short-circuited on it, so derivation walked
 * straight past the only screen that can charge, and the user reached a
 * provisioned cloud plan without paying for it.
 *
 * A state-only enumeration cannot see this. `{compute: cloud, provider: …,
 * settled}` is a legitimate state — it is what a user who genuinely paid for
 * cloud looks like. The bug is that a DIFFERENT purchase produced the same
 * flag. Only a walk over moves distinguishes them, which is what this file
 * does.
 *
 * ── The moves ────────────────────────────────────────────────────────
 *
 * Everything here is expressed in terms the rendered UI can actually issue:
 * the plan writes each step makes, `BACK_CLEARS` for Back, and paying, which is
 * modelled the honest way — a purchase confirmed by the server moves the FACT
 * it bought, not just a flag on the plan. Modelling payment as "set a flag" is
 * how the enumeration's own BFS ended up proving only reachability and never
 * that the money was owed for what the user actually ended up with.
 */
import { describe, expect, it } from "vitest";

import {
  BACK_CLEARS,
  deriveStep,
  visibleStepsForPlan,
  type OnboardingFactsInput,
} from "../stepConfig";
import { requiresPayment } from "../requiresPayment";
import { isCloudCompute } from "../types";
import type { ComputeChoice, LaunchPlan, ModelProvider } from "../types";

/** A brand-new account: no subscription, no wallet balance. */
const NEW_USER: OnboardingFactsInput = {
  computeEligible: false,
  walletFunded: false,
};

const TERMINAL_STEPS = ["project-choice", "project-picker", "github-connect"];

/** The whole world: the plan the user is building plus the server's facts. */
interface World {
  plan: Partial<LaunchPlan>;
  facts: OnboardingFactsInput;
}

/** A step the user takes. Named so a failing path reads as a bug report. */
interface Move {
  label: string;
  apply: (world: World) => World;
}

const choose = (updates: Partial<LaunchPlan>, label: string): Move => ({
  label,
  apply: ({ plan, facts }) => ({ plan: { ...plan, ...updates }, facts }),
});

/** Back, exactly as `OnboardingPage.onBack` does it: clear this step's fields. */
const back: Move = {
  label: "Back",
  apply: ({ plan, facts }) => {
    const fields = BACK_CLEARS[deriveStep(plan, facts)];
    const next: Partial<LaunchPlan> = { ...plan };
    for (const field of fields) delete next[field];
    return { plan: next, facts };
  },
};

/**
 * Pay for whatever the checkout step is currently asking for.
 *
 * This is the move the enumeration's BFS got wrong. A confirmed purchase moves
 * a SERVER FACT — a compute subscription makes the account eligible, a wallet
 * top-up funds the wallet — and the plan then records that the leg it owed is
 * settled. Writing only the plan flag is what let a search "pay" for one thing
 * and have it count against another.
 *
 * `CheckoutStep.finish` is the real code this mirrors: it re-reads the facts
 * and only records settlement once the server agrees the debt is cleared.
 */
const pay: Move = {
  label: "Pay",
  apply: ({ plan, facts }) => {
    const owed = requiresPayment(plan, facts);
    if (owed.needsCompute) {
      return {
        plan: { ...plan, computeSettled: true },
        facts: { ...facts, computeEligible: true },
      };
    }
    if (owed.needsCredit) {
      return {
        plan: { ...plan, creditSettled: true },
        facts: { ...facts, walletFunded: true },
      };
    }
    return { plan, facts };
  },
};

const COMPUTE_CHOICES: ComputeChoice[] = [
  "cloud_free_trial",
  "cloud_paid",
  "local_daemon",
];

const PROVIDERS: ModelProvider[] = ["reliant_credits", "anthropic"];

/**
 * Has this user been walked into the app owing money?
 *
 * Stated from the raw facts, NOT from `requiresPayment` — a check written in
 * terms of the function under test cannot fail when that function is the thing
 * that is wrong.
 */
function owesMoneyAtTerminal({ plan, facts }: World): string | null {
  const step = deriveStep(plan, facts);
  if (!TERMINAL_STEPS.includes(step)) return null;
  if (isCloudCompute(plan.compute) && !facts.computeEligible) {
    return `${step} reached on cloud compute with no entitlement`;
  }
  if (plan.modelProvider === "reliant_credits" && !facts.walletFunded) {
    return `${step} reached on Reliant's models with an empty wallet`;
  }
  return null;
}

describe("F2 — a Back that changes the bill must re-ask for payment", () => {
  // The exact reachable sequence from the review, walked one UI action at a
  // time. Every intermediate assertion is about where the USER is, so a failure
  // names the click that went wrong.
  it("charges for cloud compute after AI credit was bought on a local plan", () => {
    let world: World = { plan: {}, facts: NEW_USER };

    world = choose({ compute: "local_daemon" }, "local").apply(world);
    expect(deriveStep(world.plan, world.facts)).toBe("model");

    world = choose({ modelProvider: "reliant_credits" }, "Reliant's models").apply(
      world,
    );
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");

    // The user buys AI credit. Wallet funded, credit leg settled.
    world = pay.apply(world);
    expect(world.facts.walletFunded).toBe(true);
    expect(deriveStep(world.plan, world.facts)).toBe("project-picker");

    // Back twice: to the model step, then to the compute step.
    world = back.apply(world);
    expect(deriveStep(world.plan, world.facts)).toBe("model");
    world = back.apply(world);
    expect(deriveStep(world.plan, world.facts)).toBe("compute");

    // A cloud machine is a monthly subscription this account has never bought.
    world = choose({ compute: "cloud_paid" }, "cloud").apply(world);
    world = choose({ modelProvider: "anthropic" }, "own key").apply(world);

    expect(
      requiresPayment(world.plan, world.facts).needsCompute,
      "a cloud plan with no entitlement owes a compute subscription",
    ).toBe(true);
    expect(
      deriveStep(world.plan, world.facts),
      "derivation must not walk past the only screen that can charge",
    ).toBe("checkout");
  });

  // The mirror image, which the same defect allows: a compute subscription
  // bought first must not pay for AI credit chosen afterwards.
  it("charges for AI credit after a compute subscription was bought", () => {
    let world: World = { plan: {}, facts: NEW_USER };

    world = choose({ compute: "cloud_paid" }, "cloud").apply(world);
    world = choose({ modelProvider: "anthropic" }, "own key").apply(world);
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");

    world = pay.apply(world);
    expect(world.facts.computeEligible).toBe(true);
    expect(deriveStep(world.plan, world.facts)).toBe("project-choice");

    // Back to the model step and switch to Reliant's models on an empty wallet.
    world = back.apply(world);
    expect(deriveStep(world.plan, world.facts)).toBe("model");
    world = choose({ modelProvider: "reliant_credits" }, "Reliant's models").apply(
      world,
    );

    expect(requiresPayment(world.plan, world.facts).needsCredit).toBe(true);
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");
  });

  // Settlement records WHAT was confirmed, so a purchase must still count when
  // the user returns to the same choice. Without this, the fix for the above
  // could be "clear everything on Back", which re-asks a paid user for money.
  it("does not re-charge a user who backs out of a choice and returns to it", () => {
    let world: World = { plan: {}, facts: NEW_USER };

    world = choose({ compute: "cloud_paid" }, "cloud").apply(world);
    world = choose({ modelProvider: "anthropic" }, "own key").apply(world);
    world = pay.apply(world);

    world = back.apply(world); // → model
    world = back.apply(world); // → compute
    world = choose({ compute: "cloud_free_trial" }, "cloud again").apply(world);
    world = choose({ modelProvider: "anthropic" }, "own key again").apply(world);

    expect(deriveStep(world.plan, world.facts)).toBe("project-choice");
  });
});

describe("F3 — Back clears the flag that describes the answer it cleared", () => {
  // `computeAutoSkipped` records that compute resolved itself without asking.
  // Back from the model step un-asks that question, so the flag must go with
  // the answer — otherwise a field outlives what it describes, which is F2's
  // defect class without the money.
  it("drops computeAutoSkipped when Back clears compute", () => {
    const world: World = {
      plan: { compute: "local_daemon", computeAutoSkipped: true },
      facts: NEW_USER,
    };
    expect(deriveStep(world.plan, world.facts)).toBe("model");

    const after = back.apply(world);

    expect(after.plan.compute).toBeUndefined();
    expect(after.plan.computeAutoSkipped).toBeUndefined();
    expect(deriveStep(after.plan, after.facts)).toBe("compute");
  });

  // The guard that made the stale flag survivable stays load-bearing: a
  // hand-edited URL can carry a combination no Back produces, and the progress
  // bar must never hide the step the user is standing on.
  it("still never hides the current step, for a flag no Back produced", () => {
    const handEdited: Partial<LaunchPlan> = { computeAutoSkipped: true };
    expect(visibleStepsForPlan(handEdited, NEW_USER)).toContain(
      deriveStep(handEdited, NEW_USER),
    );
  });
});

describe("F2 — no walk of the UI reaches the app owing money", () => {
  /**
   * Exhaustive breadth-first search over MOVE SEQUENCES.
   *
   * The enumeration test's BFS pushes `{...plan, paid: true}` as an
   * always-available move, so it can only ever prove that a terminal step is
   * reachable — never that the money was owed for what the user ended up with.
   * This search moves the server facts with the purchase, and checks the
   * property at every node it visits rather than only at the goal.
   */
  it("holds across every sequence of up to six actions", () => {
    const moves: Move[] = [
      ...COMPUTE_CHOICES.map((compute) => choose({ compute }, `compute=${compute}`)),
      ...PROVIDERS.map((modelProvider) =>
        choose({ modelProvider }, `provider=${modelProvider}`),
      ),
      pay,
      back,
    ];

    const MAX_DEPTH = 6;
    const seen = new Set<string>();
    const offenders: string[] = [];
    const queue: { world: World; path: string[] }[] = [
      { world: { plan: {}, facts: NEW_USER }, path: [] },
    ];

    while (queue.length > 0) {
      const { world, path } = queue.shift()!;
      const key = JSON.stringify([
        world.plan.compute,
        world.plan.modelProvider,
        world.plan.intent,
        world.plan.computeSettled,
        world.plan.creditSettled,
        world.facts.computeEligible,
        world.facts.walletFunded,
      ]);
      if (seen.has(key)) continue;
      seen.add(key);

      const violation = owesMoneyAtTerminal(world);
      if (violation) offenders.push(`${path.join(" → ") || "(start)"}: ${violation}`);

      if (path.length >= MAX_DEPTH) continue;
      for (const move of moves) {
        queue.push({ world: move.apply(world), path: [...path, move.label] });
      }
    }

    expect(offenders).toEqual([]);
  });

  // Guards the guard. The search above proves nothing unless it actually
  // reaches terminal steps and actually exercises the paying move — a search
  // that visits three nodes and finds no offender is not evidence.
  it("visits terminal steps, and reaches them only by paying", () => {
    let world: World = { plan: {}, facts: NEW_USER };
    world = choose({ compute: "cloud_paid" }, "cloud").apply(world);
    world = choose({ modelProvider: "reliant_credits" }, "credits").apply(world);

    // Both legs owed. Neither payment alone is enough to leave checkout.
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");
    world = pay.apply(world);
    expect(deriveStep(world.plan, world.facts)).toBe("checkout");
    world = pay.apply(world);
    expect(TERMINAL_STEPS).toContain(deriveStep(world.plan, world.facts));
    expect(owesMoneyAtTerminal(world)).toBeNull();
  });

  // And the detector itself must be capable of firing, or the search is a
  // 2000-node no-op.
  //
  // The fixture is F2's end state written down directly: a settlement flag set
  // while the server fact it stands in for is still false. That is the ONLY
  // way to stand at a terminal step owing money — which is why the flag having
  // to name its leg is the fix.
  it("detects a user standing at a terminal step with an unpaid bill", () => {
    const world: World = {
      plan: {
        compute: "cloud_paid",
        modelProvider: "anthropic",
        intent: "build_app",
        computeSettled: true,
      },
      facts: NEW_USER,
    };
    // Precondition: the plan really is past checkout, or the detector is being
    // asked about a state the user never reaches.
    expect(TERMINAL_STEPS).toContain(deriveStep(world.plan, world.facts));
    expect(owesMoneyAtTerminal(world)).toBeTruthy();
  });
});
