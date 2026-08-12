import type { LaunchPlan, StepProps } from './types';
import type { ComponentType } from 'react';

export const ONBOARDING_STEPS = [
  'compute',
  'model',
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
  'compute': 'Daemon',
  'model': 'Model',
  'project-choice': 'Project',
  'github-connect': 'GitHub',
  'project-picker': 'Project',
};

/** Max width of the onboarding card, per step.
 *
 *  Text-and-form steps want a readable measure, so they stay narrow. The
 *  compute step leads with the daemon-topology diagram, which scales itself
 *  down to fit its container (see DaemonConnectionDiagrams) — inside the
 *  narrow card it rendered at roughly three-quarter size and the node labels
 *  got small enough to skim past.
 *
 *  The wide value is derived, not eyeballed: the diagram's intrinsic 1060px
 *  plus its own 40px of section padding plus the card's 64px of horizontal
 *  padding. That lands it at exactly 1:1, and since the diagram caps itself
 *  at its intrinsic width, going wider would only pad the sides.
 *
 *  Width is keyed on the STEP, not on what the step is currently showing, so
 *  the card only ever resizes on a step transition.
 */
const STEP_MAX_WIDTH_DEFAULT = 'max-w-[840px]';

export const STEP_MAX_WIDTH: Partial<Record<OnboardingStepId, string>> = {
  'compute': 'max-w-[1164px]',
};

export function stepMaxWidth(step: OnboardingStepId): string {
  return STEP_MAX_WIDTH[step] ?? STEP_MAX_WIDTH_DEFAULT;
}

/** All steps that *would* appear in the user's onboarding given the plan
 *  branch they're on. Used only by the progress bar; current-step
 *  selection is via `deriveStep` below.
 */
export function getStepsForPlan(plan: Partial<LaunchPlan>): OnboardingStepId[] {
  if (!plan.compute) return ['compute'];

  const isCloud = plan.compute === 'cloud_free_trial';

  const steps: OnboardingStepId[] = ['compute', 'model'];

  if (isCloud) {
    steps.push('project-choice');
    if (plan.intent === 'existing_codebase') {
      steps.push('github-connect');
    }
  } else {
    steps.push('project-picker');
  }

  return steps;
}

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
export function deriveStep(plan: Partial<LaunchPlan>): OnboardingStepId {
  if (!plan.compute) return 'compute';
  if (!plan.modelProvider) return 'model';

  const isCloud = plan.compute === 'cloud_free_trial';
  if (!isCloud) return 'project-picker';

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
  'model': ['compute'],
  'project-choice': ['modelProvider'],
  'project-picker': ['modelProvider'],
  'github-connect': ['intent'],
};
