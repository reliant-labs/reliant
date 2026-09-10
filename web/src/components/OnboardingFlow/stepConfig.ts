import { isCloudCompute } from './types';
import { requiresPayment } from './requiresPayment';
import type { PaymentFacts } from './requiresPayment';
import type { LaunchPlan, StepProps } from './types';
import type { ComponentType } from 'react';

export const ONBOARDING_STEPS = [
  'compute',
  'model',
  'checkout',
  'project-choice',
  'github-connect',
  'project-picker',
] as const;

export type OnboardingStepId = typeof ONBOARDING_STEPS[number];

// Lazy-import step components to avoid circular deps
export const STEP_COMPONENTS: Record<OnboardingStepId, ComponentType<StepProps>> = {} as any;

// Called once from index.ts to register components
export function registerStepComponents(map: Record<string, ComponentType<StepProps>>) {
  Object.assign(STEP_COMPONENTS, map);
}

export const STEP_LABELS: Record<OnboardingStepId, string> = {
  'compute': 'Machine',
  'model': 'Model',
  'checkout': 'Payment',
  'project-choice': 'Project',
  'github-connect': 'GitHub',
  'project-picker': 'Project',
};

/** Max width of the onboarding card.
 *
 *  Every step is text and form controls, which want a readable measure — so
 *  they all share one width and the card never resizes between steps.
 *
 *  The compute step used to be widened to 1164px to fit a topology diagram
 *  at its intrinsic 1060px. That diagram is gone: at 480px tall it pushed the
 *  actual choice buttons below the fold of the default 1400x900 window, and
 *  being the largest thing on screen it read as the interface rather than as
 *  an illustration. The step now leads with the question itself.
 */
const STEP_MAX_WIDTH_DEFAULT = 'max-w-[840px]';

export function stepMaxWidth(_step: OnboardingStepId): string {
  return STEP_MAX_WIDTH_DEFAULT;
}

/**
 * The server facts step derivation depends on.
 *
 * `deriveStep` widened from a pure function of the plan to a pure function of
 * `(plan, facts)` when the checkout step landed, and that is a real cost — the
 * enumeration test's state space multiplied by four. It stays PURE, which is
 * what makes the cost acceptable. The alternative, writing eligibility and
 * wallet balance into the URL plan, puts a server-owned time-varying fact into
 * user-editable state: precisely the hazard that let `cloud_paid` route to the
 * local project picker.
 *
 * Callers get these from {@link useOnboardingFacts}, which reads them
 * pessimistically while loading.
 */
export type OnboardingFactsInput = PaymentFacts;

/**
 * Facts for a caller that only wants the plan-shaped answer — the progress bar
 * building a step list before the queries have settled, and every test that
 * predates the checkout step.
 *
 * PESSIMISTIC, matching {@link useOnboardingFacts}: "nothing is entitled yet",
 * so a paid plan's checkout step is listed rather than silently omitted.
 */
export const UNKNOWN_FACTS: OnboardingFactsInput = {
  computeEligible: false,
  walletFunded: false,
  // NOT pessimistic, and deliberately so. The two above are server reads, where
  // "unknown" must mean "might owe". This one is a build constant read from
  // `capabilities.billing` — there is no unknown state for it to be in, so
  // defaulting it false here would claim every caller of UNKNOWN_FACTS is on a
  // deployment that cannot bill, which is the opposite of pessimistic: it would
  // suppress the checkout step rather than list it.
  reliantBillingAvailable: true,
};

/** All steps that *would* appear in the user's onboarding given the plan
 *  branch they're on. Used only by the progress bar; current-step
 *  selection is via `deriveStep` below.
 */
export function getStepsForPlan(
  plan: Partial<LaunchPlan>,
  facts: OnboardingFactsInput = UNKNOWN_FACTS,
): OnboardingStepId[] {
  if (!plan.compute) return ['compute'];

  const steps: OnboardingStepId[] = ['compute', 'model'];

  // Listed only when money is genuinely owed, and only once both choices that
  // could cost money have been made — the same condition `deriveStep` uses, so
  // the bar can never show a step derivation will not route to.
  if (checkoutIsOwed(plan, facts)) steps.push('checkout');

  if (isCloudCompute(plan.compute)) {
    steps.push('project-choice');
    if (plan.intent === 'existing_codebase') {
      steps.push('github-connect');
    }
  } else {
    steps.push('project-picker');
  }

  return steps;
}

/** The steps the progress bar actually shows.
 *
 *  A compute step that auto-skipped is hidden from the flow entirely: the user
 *  was never asked anything, so showing "1 Daemon" implies a choice they never
 *  made and numbers every later step from a question that did not happen.
 *
 *  It is hidden only while it is genuinely behind the user. `computeAutoSkipped`
 *  outlives the field it describes — Back from `model` clears `compute` but
 *  leaves the flag set — so filtering unconditionally removed the step the user
 *  was standing on. `indexOf` then returned -1 and `OnboardingPage`'s
 *  `Math.max(0, …)` silently highlighted the wrong step: a swallowed error
 *  rather than a default. Never hide the current step.
 */
export function visibleStepsForPlan(
  plan: Partial<LaunchPlan>,
  facts: OnboardingFactsInput = UNKNOWN_FACTS,
): OnboardingStepId[] {
  const all = getStepsForPlan(plan, facts);
  if (!plan.computeAutoSkipped) return all;
  const current = deriveStep(plan, facts);
  return all.filter(id => id !== 'compute' || id === current);
}

/**
 * Is the checkout step owed right now?
 *
 * Shared by `deriveStep` and `getStepsForPlan` rather than written twice: the
 * one thing that must never happen is the bar listing a step derivation will
 * not route to (or vice versa), and two copies of this condition is how that
 * happens.
 *
 * There is deliberately NO settlement short-circuit here. There used to be —
 * `if (plan.paid) return false` — and because it sat above `requiresPayment`
 * it cancelled the whole bill rather than the leg it had actually paid. AI
 * credit bought on a local plan therefore satisfied a cloud subscription the
 * user chose afterwards via Back, and derivation walked past the only screen
 * that can charge. Settlement is now read per leg INSIDE `requiresPayment`,
 * beside the server fact each one stands in for, so it can only ever cancel
 * its own debt.
 */
function checkoutIsOwed(
  plan: Partial<LaunchPlan>,
  facts: OnboardingFactsInput,
): boolean {
  if (!plan.compute || !plan.modelProvider) return false;
  return requiresPayment(plan, facts).any;
}

/**
 * NOTE — a compute bill with no `computePlanId` chosen.
 *
 * The checkout step has no size picker BY DESIGN (the machine is chosen on the
 * compute step, beside its price), so such a bill is one nothing on that
 * screen can settle: it renders "we couldn't load the machine plans", blaming
 * the catalog for a missing field.
 *
 * It is NOT fixed by re-routing here, and the attempt is instructive. Making
 * `checkoutIsOwed` return false says "nothing is owed" and derivation falls
 * through to the PROJECT steps, walking an unpaid user into the app — the one
 * invariant `deriveStep.transitions` exists to defend. Returning 'compute'
 * from `deriveStep` instead makes the step non-monotonic and breaks the
 * enumeration's "checkout appears exactly when money is owed" contract.
 *
 * The real fix is upstream, where the field goes missing: `BACK_CLEARS.checkout`
 * now clears `compute` alongside `computePlanId`, so a Back that un-chooses the
 * machine returns to the step that can re-choose it. See `backFromCheckout.trap`.
 */

/** Derive the single step the user should be on right now from plan state.
 *  This is the source of truth — there is no `step` URL param. Forward
 *  motion happens when a step updates the plan; backward motion happens
 *  when `onBack` clears the relevant plan fields via BACK_CLEARS.
 *
 *  Note: we deliberately do NOT take `hasGithubCredential` into account. The
 *  picker phase of github-connect handles missing/revoked credentials via
 *  its built-in reconnect UI. Gating step derivation on a credential check
 *  would re-route the user mid-flow if a token check transiently fails.
 */
export function deriveStep(
  plan: Partial<LaunchPlan>,
  facts: OnboardingFactsInput = UNKNOWN_FACTS,
): OnboardingStepId {
  if (!plan.compute) return 'compute';
  if (!plan.modelProvider) return 'model';

  // Both choices that can cost money are made. If either does and it has not
  // been paid, this is the ONE place onboarding asks — and it is absent
  // entirely when nothing is owed. Two steps used to eject the user to
  // /settings/billing here instead; that is what this clause replaces.
  if (checkoutIsOwed(plan, facts)) return 'checkout';

  if (!isCloudCompute(plan.compute)) return 'project-picker';

  if (!plan.intent) return 'project-choice';
  if (plan.intent === 'existing_codebase') return 'github-connect';

  // Cloud + build_app — terminal step is project-choice (it owns the
  // completeOnboarding call). Until completion fires, leave the user there.
  return 'project-choice';
}

/** When a step's Back button is clicked, these plan fields are cleared.
 *  Derivation then naturally lands the user on the previous step.
 *  Empty array = no back action available (we're at the first step).
 */
export const BACK_CLEARS: Record<OnboardingStepId, (keyof LaunchPlan)[]> = {
  'compute': [],
  // `computeAutoSkipped` describes a PAST EVENT ("we resolved compute without
  // asking"), and clearing `compute` un-does that event — so the flag has to go
  // with it. Leaving it set made a field outlive the thing it describes, which
  // is the same defect class as the old `paid` verdict surviving a Back that
  // changed the bill. `visibleStepsForPlan` still tolerates a stale flag,
  // because a hand-edited URL can carry one that no Back produced.
  'model': ['compute', 'computeAutoSkipped'],
  // Back from payment returns to the decision that created the cost. It must
  // also drop the purchase selections made ON this step, or derivation lands
  // the user back here with a plan chosen for a compute option they just
  // rejected.
  //
  // The settlement flags go too, and leaving them out was a real trap. They
  // record "this leg's debt is cleared" — a COUPON sets one without any card,
  // which is the common case here. A Back that keeps `computeSettled` sends
  // derivation straight past the compute leg to the credit leg, so a user who
  // redeemed a compute coupon could never return to change their machine size:
  // Back landed on the model step, and the next forward move skipped compute
  // entirely. Going back to reconsider a leg has to un-settle that leg,
  // otherwise the flag outlives the decision it describes — the same defect
  // class as the `computeAutoSkipped` note above.
  //
  // Re-settling is cheap and safe: a redeemed coupon still reads as
  // `computeEligible` from the server, so `requiresPayment` owes nothing and
  // the step is simply not derived again. The grant is not spent by this.
  // `compute` goes too, and leaving it out was the second trap on this step.
  // Back drops `computePlanId` — right, since a machine chosen for a bill the
  // user is reconsidering should not survive — but the plan TILES live on the
  // compute step. Clearing only `modelProvider` landed the user on the model
  // step, which cannot set a plan id, and their next forward move derived
  // straight back to checkout with none. That is the state that renders "we
  // couldn't load the machine plans": the message blames the catalog for a
  // field Back deleted.
  //
  // Going back from payment returns to the decision that CREATED the cost.
  // Compute is one of the two legs of that bill, so reconsidering the bill has
  // to be able to reconsider the machine. `computeAutoSkipped` goes with
  // `compute` for the same reason it does on the model step — the flag
  // describes an event that clearing `compute` un-does.
  'checkout': [
    'compute',
    'computeAutoSkipped',
    'modelProvider',
    'computePlanId',
    'aiCreditCents',
    'computeSettled',
    'creditSettled',
  ],
  'project-choice': ['modelProvider'],
  'project-picker': ['modelProvider'],
  'github-connect': ['intent'],
};
