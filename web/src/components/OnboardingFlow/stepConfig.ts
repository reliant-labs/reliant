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

/** Derive which steps to show based on the current plan state. */
export function getStepsForPlan(plan: Partial<LaunchPlan>): OnboardingStepId[] {
  if (!plan.compute) return ['compute'];

  const isCloud = plan.compute === 'cloud_free_trial';

  const steps: OnboardingStepId[] = ['compute'];

  steps.push('model');

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
