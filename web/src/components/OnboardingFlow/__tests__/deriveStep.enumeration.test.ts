/**
 * Exhaustive enumeration of the onboarding step machine.
 *
 * `deriveStep` reads three plan fields plus the `paid` flag, each with a small
 * closed domain, and two server facts. The whole reachable space is 240 plan
 * states × 2 `paid` values × 4 fact combinations = 1920. That is still small
 * enough to enumerate in a loop, which is strictly better than hand-picking
 * fixtures: a hand-written test only covers the states someone thought of, and
 * every onboarding regression so far lived in one nobody did.
 *
 * The state space grew when the consolidated checkout step landed — the price
 * of `deriveStep` widening from `(plan)` to `(plan, facts)`. It stays PURE,
 * which is the whole reason that widening was acceptable: the alternative,
 * writing eligibility into the URL plan, is not enumerable at all because the
 * user can edit it.
 *
 * Written in the shape of `launchPlanSchema.drift.test.ts` — two declarations
 * that must agree, a fixture enumerating one side, a loop asserting the other,
 * and a guard-the-guard case proving the loop is capable of failing.
 *
 * Two defects this was written to catch, both live when it was added:
 *
 *  A. `compute: "cloud_paid"` is a first-class `ComputeChoice` that
 *     `launchPlanSchema` accepts, but every cloud test in the flow spelled
 *     cloud-ness as `=== "cloud_free_trial"`. A paid plan therefore routed to
 *     `project-picker` and never rendered `DaemonConnectingGate` — precisely
 *     the failure `458a830c` fixed for the free-trial path, still live on the
 *     paid one.
 *
 *  B. `computeAutoSkipped` outlives the field it describes. Back from `model`
 *     clears `compute` but leaves the flag, so `OnboardingPage` filtered
 *     `compute` out of the visible step list while `deriveStep` returned
 *     `'compute'`. `indexOf` gave `-1` and `Math.max(0, -1)` silently
 *     highlighted the wrong step. The `Math.max` was a swallowed error, not a
 *     default.
 */
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import {
  BACK_CLEARS,
  ONBOARDING_STEPS,
  STEP_COMPONENTS,
  STEP_LABELS,
  deriveStep,
  getStepsForPlan,
  visibleStepsForPlan,
  type OnboardingFactsInput,
  type OnboardingStepId,
} from "../stepConfig";
import { requiresPayment } from "../requiresPayment";
import { isCloudCompute } from "../types";
import type { ComputeChoice, LaunchPlan, ModelProvider, OnboardingIntent } from "../types";
// Registration is a module-scope side effect of this import. Invariant 1
// depends on it having run.
import "../steps";

const ONBOARDING_SRC_DIR = join(__dirname, "..");
const ONBOARDING_STEPS_DIR = join(ONBOARDING_SRC_DIR, "steps");

/**
 * Every value each field can hold, INCLUDING `undefined` — an absent field is
 * a real state the flow spends most of its time in, not an edge case.
 */
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

const INTENT_VALUES: (OnboardingIntent | undefined)[] = [
  undefined,
  "build_app",
  "existing_codebase",
];

const AUTO_SKIPPED_VALUES = [false, true];

const PAID_VALUES = [false, true];

/**
 * Every combination of the two server facts. They are NOT in the plan — they
 * are server-owned and change under the user — so they multiply the space
 * rather than living in it.
 */
const FACT_VALUES: OnboardingFactsInput[] = [
  { computeEligible: false, walletFunded: false },
  { computeEligible: true, walletFunded: false },
  { computeEligible: false, walletFunded: true },
  { computeEligible: true, walletFunded: true },
];

/** The facts a brand-new account actually has: none. */
const NEW_USER_FACTS: OnboardingFactsInput = {
  computeEligible: false,
  walletFunded: false,
};

/** The facts of a fully entitled user — a coupon, or a completed checkout. */
const ENTITLED_FACTS: OnboardingFactsInput = {
  computeEligible: true,
  walletFunded: true,
};

/** Terminal steps — the ones that own a `completeOnboarding` call. */
const TERMINAL_STEPS: OnboardingStepId[] = [
  "project-choice",
  "project-picker",
  "github-connect",
];

type EnumeratedState = {
  plan: Partial<LaunchPlan>;
  facts: OnboardingFactsInput;
  label: string;
};

/** Every render-relevant state, as a flat list. */
function allStates(): EnumeratedState[] {
  const states: EnumeratedState[] = [];
  for (const compute of COMPUTE_VALUES) {
    for (const modelProvider of MODEL_PROVIDER_VALUES) {
      for (const intent of INTENT_VALUES) {
        for (const computeAutoSkipped of AUTO_SKIPPED_VALUES) {
          for (const paid of PAID_VALUES) {
            for (const facts of FACT_VALUES) {
              const plan: Partial<LaunchPlan> = {};
              if (compute) plan.compute = compute;
              if (modelProvider) plan.modelProvider = modelProvider;
              if (intent) plan.intent = intent;
              if (computeAutoSkipped) plan.computeAutoSkipped = true;
              if (paid) plan.paid = true;
              states.push({
                plan,
                facts,
                label:
                  `compute=${compute} provider=${modelProvider} intent=${intent} ` +
                  `autoSkipped=${computeAutoSkipped} paid=${paid} ` +
                  `eligible=${facts.computeEligible} funded=${facts.walletFunded}`,
              });
            }
          }
        }
      }
    }
  }
  return states;
}

const STATES = allStates();

describe("onboarding state space", () => {
  it("enumerates the full 1920-state render-relevant space", () => {
    // Pins the arithmetic in the header comment. If a field gains a value this
    // fails and forces the domain lists above to be revisited, rather than the
    // new value quietly going untested.
    expect(STATES).toHaveLength(
      COMPUTE_VALUES.length *
        MODEL_PROVIDER_VALUES.length *
        INTENT_VALUES.length *
        AUTO_SKIPPED_VALUES.length *
        PAID_VALUES.length *
        FACT_VALUES.length,
    );
    expect(STATES).toHaveLength(1920);
  });
});

describe("step registry drift", () => {
  // Four parallel records keyed by OnboardingStepId. TypeScript catches a
  // missing key in STEP_LABELS and BACK_CLEARS because they are declared
  // `Record<OnboardingStepId, …>`, but STEP_COMPONENTS is filled at runtime by
  // an import side effect and is typed through an assertion, so nothing but
  // this test can catch a step that was never registered.
  it("registers a component for every declared step", () => {
    expect(Object.keys(STEP_COMPONENTS).sort()).toEqual(
      [...ONBOARDING_STEPS].sort(),
    );
  });

  it("labels and back-clears every declared step", () => {
    expect(Object.keys(STEP_LABELS).sort()).toEqual([...ONBOARDING_STEPS].sort());
    expect(Object.keys(BACK_CLEARS).sort()).toEqual([...ONBOARDING_STEPS].sort());
  });
});

describe("invariant 1 — every state has a step the flow can render", () => {
  it("derives a registered step for all 1920 states", () => {
    const offenders: string[] = [];
    for (const { plan, facts, label } of STATES) {
      const step = deriveStep(plan, facts);
      if (!ONBOARDING_STEPS.includes(step)) {
        offenders.push(`${label}: derived unknown step ${step}`);
      } else if (!STEP_COMPONENTS[step]) {
        offenders.push(`${label}: no component registered for ${step}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("still fails for a step with no registered component", () => {
    // Guards the guard: proves the component lookup above is load-bearing
    // rather than vacuously true.
    const unregistered = "not-a-step" as OnboardingStepId;
    expect(STEP_COMPONENTS[unregistered]).toBeUndefined();
  });
});

describe("invariant 2 — the derived step is always visible in the progress bar", () => {
  // `OnboardingPage` computes `steps.indexOf(deriveStep(plan))` and wraps it in
  // `Math.max(0, …)`. If the derived step is ever absent from the visible list
  // that `Math.max` silently highlights the wrong step. This invariant makes
  // the swallowed error into a failure, and proves the `Math.max` is dead code.
  it("finds the derived step in the plan's full step list", () => {
    const offenders: string[] = [];
    for (const { plan, facts, label } of STATES) {
      const step = deriveStep(plan, facts);
      const steps = getStepsForPlan(plan, facts);
      if (!steps.includes(step)) {
        offenders.push(`${label}: ${step} not in ${steps.join(",")}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("finds the derived step in the VISIBLE step list, auto-skip included", () => {
    const offenders: string[] = [];
    for (const { plan, facts, label } of STATES) {
      const step = deriveStep(plan, facts);
      const visible = visibleStepsForPlan(plan, facts);
      if (!visible.includes(step)) {
        offenders.push(`${label}: ${step} not in visible [${visible.join(",")}]`);
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("invariant 3 — Back is a true inverse of forward motion", () => {
  it("moves strictly earlier in the step list from every state", () => {
    const offenders: string[] = [];
    for (const { plan, facts, label } of STATES) {
      const step = deriveStep(plan, facts);
      const fields = BACK_CLEARS[step];
      if (fields.length === 0) continue; // first step: no back action

      const cleared: Partial<LaunchPlan> = { ...plan };
      for (const field of fields) delete cleared[field];

      const previous = deriveStep(cleared, facts);
      const steps = getStepsForPlan(plan, facts);
      const from = steps.indexOf(step);
      const to = steps.indexOf(previous);

      if (to === -1) {
        offenders.push(`${label}: back from ${step} left the flow (${previous})`);
      } else if (to >= from) {
        offenders.push(
          `${label}: back from ${step} (${from}) went to ${previous} (${to}) — not earlier`,
        );
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("invariant 4 — no state strands the user", () => {
  /**
   * From every state there must exist a sequence of plan updates the UI can
   * actually issue that reaches a terminal step. Expressed as a breadth-first
   * search over the field assignments the steps make, which is the structural
   * form of the `63b18468` invariant ("the user always has a way forward").
   */
  function reachesTerminal(
    start: Partial<LaunchPlan>,
    facts: OnboardingFactsInput,
  ): boolean {
    const seen = new Set<string>();
    const queue: Partial<LaunchPlan>[] = [start];

    while (queue.length > 0) {
      const plan = queue.shift()!;
      const key = JSON.stringify([
        plan.compute,
        plan.modelProvider,
        plan.intent,
        plan.paid,
      ]);
      if (seen.has(key)) continue;
      seen.add(key);

      if (TERMINAL_STEPS.includes(deriveStep(plan, facts))) return true;

      // The moves the rendered steps can make: choose a compute, choose a
      // provider, choose an intent, and — new with the checkout step — pay.
      // Paying is a real move available from every state that owes money,
      // which is what stops the checkout step being a trap.
      for (const compute of COMPUTE_VALUES) {
        if (compute) queue.push({ ...plan, compute });
      }
      for (const modelProvider of MODEL_PROVIDER_VALUES) {
        if (modelProvider) queue.push({ ...plan, modelProvider });
      }
      for (const intent of INTENT_VALUES) {
        if (intent) queue.push({ ...plan, intent });
      }
      queue.push({ ...plan, paid: true });
    }
    return false;
  }

  it("can reach a terminal step from all 1920 states", () => {
    const offenders: string[] = [];
    for (const { plan, facts, label } of STATES) {
      if (!reachesTerminal(plan, facts)) offenders.push(label);
    }
    expect(offenders).toEqual([]);
  });

  // Guards the guard. Without the `paid` move the search must FAIL for a new
  // user on the cloud path, because every route to a terminal step runs
  // through checkout. If this ever passes, the search has a way around the
  // payment step and the invariant above is proving nothing about it.
  it("cannot reach a terminal step without paying, when payment is owed", () => {
    function reachesWithoutPaying(plan: Partial<LaunchPlan>): boolean {
      const seen = new Set<string>();
      const queue: Partial<LaunchPlan>[] = [plan];
      while (queue.length > 0) {
        const p = queue.shift()!;
        const key = JSON.stringify([p.compute, p.modelProvider, p.intent]);
        if (seen.has(key)) continue;
        seen.add(key);
        if (TERMINAL_STEPS.includes(deriveStep(p, NEW_USER_FACTS))) return true;
        for (const modelProvider of MODEL_PROVIDER_VALUES) {
          if (modelProvider) queue.push({ ...p, modelProvider });
        }
        for (const intent of INTENT_VALUES) {
          if (intent) queue.push({ ...p, intent });
        }
      }
      return false;
    }

    // Compute is pinned to cloud — the user has made that choice — and the
    // wallet and entitlement are both empty. Every onward move must stop at
    // checkout.
    expect(reachesWithoutPaying({ compute: "cloud_paid" })).toBe(false);
  });
});

describe("the checkout step appears exactly when money is owed", () => {
  /**
   * The consolidation guarantee, stated over the whole state space: the ONE
   * billing moment is present precisely when {@link requiresPayment} says
   * something is owed and the plan has not already been paid — never
   * otherwise.
   *
   * The failure this catches in each direction is a real product failure. A
   * spurious checkout step asks a user for money they do not owe; a missing
   * one walks an unpaid user into the app to fail at their first message,
   * which is the dead end the two deleted `/settings/billing` ejections used
   * to paper over.
   */
  it("derives checkout for exactly the states that owe money", () => {
    const offenders: string[] = [];
    for (const { plan, facts, label } of STATES) {
      const owes =
        Boolean(plan.compute) &&
        Boolean(plan.modelProvider) &&
        !plan.paid &&
        requiresPayment(plan, facts).any;
      const isCheckout = deriveStep(plan, facts) === "checkout";
      if (owes !== isCheckout) {
        offenders.push(
          `${label}: owes=${owes} but derived ${deriveStep(plan, facts)}`,
        );
      }
    }
    expect(offenders).toEqual([]);
  });

  // The free path, spelled out: local compute with your own key never sees a
  // payment screen, whatever the server facts say.
  it("never shows checkout to a user who owes nothing", () => {
    for (const facts of FACT_VALUES) {
      for (const intent of INTENT_VALUES) {
        const plan: Partial<LaunchPlan> = {
          compute: "local_daemon",
          modelProvider: "anthropic",
        };
        if (intent) plan.intent = intent;
        expect(deriveStep(plan, facts), JSON.stringify({ plan, facts })).not.toBe(
          "checkout",
        );
      }
    }
  });

  // The common cloud case. A brand-new account has no trial and no
  // subscription, so this is what nearly every first-time cloud user hits.
  it("shows checkout to a new cloud user, on both cloud tiers", () => {
    for (const compute of COMPUTE_VALUES.filter(isCloudCompute)) {
      expect(
        deriveStep({ compute, modelProvider: "anthropic" }, NEW_USER_FACTS),
      ).toBe("checkout");
    }
  });

  it("shows checkout for Reliant's models on an empty wallet, even on local compute", () => {
    expect(
      deriveStep(
        { compute: "local_daemon", modelProvider: "reliant_credits" },
        { computeEligible: true, walletFunded: false },
      ),
    ).toBe("checkout");
  });

  // A redeemed compute coupon flips eligibility, and the step simply is not
  // there — no button to press, which is the URL-derived design paying off.
  it("skips checkout entirely once a coupon has granted entitlement", () => {
    expect(
      deriveStep(
        { compute: "cloud_paid", modelProvider: "anthropic" },
        ENTITLED_FACTS,
      ),
    ).toBe("project-choice");
  });

  // `paid` is what stops the step recurring while the entitlement query is
  // still catching up with the webhook. Without it a confirmed purchase
  // derives the user straight back to a payment they already made.
  it("does not show checkout again once the plan is marked paid", () => {
    expect(
      deriveStep(
        { compute: "cloud_paid", modelProvider: "anthropic", paid: true },
        NEW_USER_FACTS,
      ),
    ).toBe("project-choice");
  });

  // Facts default to "nothing is entitled" so a caller that has not got them
  // yet errs toward showing the payment step rather than past it.
  it("defaults to owing money when facts are not supplied", () => {
    expect(deriveStep({ compute: "cloud_paid", modelProvider: "anthropic" })).toBe(
      "checkout",
    );
  });
});

describe("onboarding never leaves the flow for billing", () => {
  /**
   * The owner's headline ask, pinned at the source rather than through a
   * render.
   *
   * `ComputeStep` and `ModelStep` each used to navigate to
   * `/settings/billing`, which is a full exit from a wizard whose entire state
   * lives in one URL search param — hence the `returnTo` round-trip that
   * existed to undo it. A rendered test would only catch the exit on the
   * branch it happened to render; reading the source catches it everywhere,
   * including a branch a future edit adds.
   *
   * `useGoToBilling` itself is NOT deleted — `ResumeDaemonPill`,
   * `ConnectDaemonModal` and `UpgradeRequiredModal` are in-app callers and
   * still need it. What must not come back is onboarding calling it.
   */
  it("has no billing navigation left in any onboarding source file", () => {
    const dirs = [ONBOARDING_SRC_DIR, ONBOARDING_STEPS_DIR];
    const offenders: string[] = [];
    for (const dir of dirs) {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        if (!entry.isFile()) continue;
        if (!/\.tsx?$/.test(entry.name)) continue;
        const source = readFileSync(join(dir, entry.name), "utf8");
        // Comments legitimately discuss the removed exit; strip them first.
        const code = source
          .replace(/\/\*[\s\S]*?\*\//g, "")
          .replace(/^\s*\/\/.*$/gm, "");
        if (code.includes("useGoToBilling")) {
          offenders.push(`${entry.name}: imports useGoToBilling`);
        }
        if (code.includes("settings/billing")) {
          offenders.push(`${entry.name}: navigates to settings/billing`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("would notice a billing navigation if one were added", () => {
    // Guards the guard: the comment-stripping above must not swallow real
    // code, or the scan passes on a file that does navigate away.
    const withExit = `import { useGoToBilling } from "@/hooks/useGoToBilling";`;
    const stripped = withExit
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/^\s*\/\/.*$/gm, "");
    expect(stripped).toContain("useGoToBilling");
  });
});

describe("cloud-ness is decided in one place", () => {
  /**
   * Defect A. The rule to pin is NOT the current spelling of the comparison —
   * it is that every cloud `ComputeChoice` makes the same step decisions. A
   * third cloud tier added later must be caught by `isCloudCompute` alone, not
   * by remembering to update four `=== "cloud_free_trial"` literals.
   */
  it("treats cloud_paid exactly like cloud_free_trial at every step decision", () => {
    const offenders: string[] = [];
    for (const modelProvider of MODEL_PROVIDER_VALUES) {
      for (const intent of INTENT_VALUES) {
        for (const facts of FACT_VALUES) {
          const base: Partial<LaunchPlan> = {};
          if (modelProvider) base.modelProvider = modelProvider;
          if (intent) base.intent = intent;

          const free = { ...base, compute: "cloud_free_trial" as const };
          const paid = { ...base, compute: "cloud_paid" as const };
          const label =
            `provider=${modelProvider} intent=${intent} ` +
            `eligible=${facts.computeEligible} funded=${facts.walletFunded}`;

          if (deriveStep(free, facts) !== deriveStep(paid, facts)) {
            offenders.push(
              `${label}: deriveStep free=${deriveStep(free, facts)} paid=${deriveStep(paid, facts)}`,
            );
          }
          const freeSteps = getStepsForPlan(free, facts).join(",");
          const paidSteps = getStepsForPlan(paid, facts).join(",");
          if (freeSteps !== paidSteps) {
            offenders.push(`${label}: steps free=[${freeSteps}] paid=[${paidSteps}]`);
          }
          // Payment-ness is part of cloud-ness now. `cloud_free_trial` is a
          // legacy name, not a free tier — there is no trial — so a plan
          // spelled that way must owe exactly what a paid one owes.
          if (
            requiresPayment(free, facts).any !== requiresPayment(paid, facts).any
          ) {
            offenders.push(`${label}: requiresPayment disagrees across cloud tiers`);
          }
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("classifies every ComputeChoice, and only the cloud ones as cloud", () => {
    expect(isCloudCompute("cloud_free_trial")).toBe(true);
    expect(isCloudCompute("cloud_paid")).toBe(true);
    expect(isCloudCompute("local_daemon")).toBe(false);
    expect(isCloudCompute("undecided")).toBe(false);
    expect(isCloudCompute(undefined)).toBe(false);
  });

  it("routes every cloud choice through the step that renders the daemon gate", () => {
    // The concrete consequence of Defect A: a cloud plan whose provider and
    // intent are settled must land on a step that can show
    // DaemonConnectingGate, never on the local project-picker.
    //
    // Facts are ENTITLED here on purpose. The question is where a settled
    // cloud plan goes once nothing stands in the way; an un-entitled one goes
    // to checkout first, which the payment tests above cover.
    for (const compute of COMPUTE_VALUES.filter(isCloudCompute)) {
      const plan: Partial<LaunchPlan> = {
        compute,
        modelProvider: "anthropic",
        intent: "build_app",
      };
      expect(deriveStep(plan, ENTITLED_FACTS)).toBe("project-choice");
    }
  });
});
