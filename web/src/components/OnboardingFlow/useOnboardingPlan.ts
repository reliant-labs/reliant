import { useState, useCallback } from 'react';
import type { LaunchPlan } from './types';

const PLAN_KEY = 'reliant-onboarding-plan';

function loadPlan(): Partial<LaunchPlan> {
  try {
    const stored = localStorage.getItem(PLAN_KEY);
    return stored ? JSON.parse(stored) : {};
  } catch {
    return {};
  }
}

function savePlan(plan: Partial<LaunchPlan>) {
  localStorage.setItem(PLAN_KEY, JSON.stringify(plan));
}

export function useOnboardingPlan() {
  const [plan, setPlan] = useState<Partial<LaunchPlan>>(loadPlan);

  const updatePlan = useCallback((updates: Partial<LaunchPlan>) => {
    setPlan(prev => {
      const next = { ...prev, ...updates };
      savePlan(next);
      return next;
    });
  }, []);

  const resetPlan = useCallback(() => {
    localStorage.removeItem(PLAN_KEY);
    setPlan({});
  }, []);

  return { plan, updatePlan, resetPlan };
}
