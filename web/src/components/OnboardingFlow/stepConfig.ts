import type { LaunchPlan, StepProps } from './types';
import type { ComponentType } from 'react';

export const ONBOARDING_STEPS = [
  'goal', 'compute', 'daemon-connect', 'github-connect',
  'local-project-location', 'forge-style', 'model',
] as const;

export type OnboardingStepId = typeof ONBOARDING_STEPS[number];

// Lazy-import step components to avoid circular deps
export const STEP_COMPONENTS: Record<OnboardingStepId, ComponentType<StepProps>> = {} as any;

// Called once from index.ts to register components
export function registerStepComponents(map: Record<string, ComponentType<StepProps>>) {
  Object.assign(STEP_COMPONENTS, map);
}

export const STEP_LABELS: Record<OnboardingStepId, string> = {
  'goal': 'Goal',
  'compute': 'Daemon',
  'daemon-connect': 'Connect',
  'github-connect': 'Repository',
  'local-project-location': 'Directory',
  'forge-style': 'Style',
  'model': 'Model',
};

/** Derive which steps to show based on the current plan state. */
export function getStepsForPlan(plan: Partial<LaunchPlan>): OnboardingStepId[] {
  if (!plan.intent) return ['goal'];
  if (!plan.compute) return ['goal', 'compute'];

  const isCloud = plan.compute === 'cloud_free_trial';
  const isPreconnected = plan.daemonPreConnected;

  const steps: OnboardingStepId[] = ['goal', 'compute'];

  // Daemon connect (local only, not pre-connected)
  if (!isCloud && !isPreconnected) {
    steps.push('daemon-connect');
  }

  // GitHub connect (cloud + existing codebase only)
  if (isCloud && plan.intent === 'existing_codebase') {
    steps.push('github-connect');
  }

  // Project location (local only)
  if (!isCloud) {
    steps.push('local-project-location');
  }

  // Forge style (build_app only)
  if (plan.intent === 'build_app') {
    steps.push('forge-style');
  }

  steps.push('model');
  return steps;
}
