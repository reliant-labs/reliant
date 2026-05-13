import { useNavigate, useSearch } from '@tanstack/react-router';
import { useCallback } from 'react';
import type { LaunchPlan } from './types';

// URL-backed onboarding plan state.
//
// Phase 3 refactor: the plan used to live in localStorage under
// `reliant-onboarding-plan`, which caused split-brain bugs (React state lagging
// behind localStorage during a single render) and let the plan persist
// indefinitely across sessions. Durable navigation state belongs in the URL.
//
// The plan is serialized as a single JSON-encoded `plan` search param to keep
// the route schema minimal and updates atomic. Plan fields are non-sensitive
// (no model API keys live in the plan), so URL encoding is safe.

const EMPTY_PLAN: Partial<LaunchPlan> = {};

function parsePlan(encoded: string | undefined): Partial<LaunchPlan> {
  if (!encoded) return EMPTY_PLAN;
  try {
    return JSON.parse(decodeURIComponent(encoded)) as Partial<LaunchPlan>;
  } catch {
    return EMPTY_PLAN;
  }
}

function encodePlan(plan: Partial<LaunchPlan>): string | undefined {
  if (!plan || Object.keys(plan).length === 0) return undefined;
  return encodeURIComponent(JSON.stringify(plan));
}

export function useOnboardingPlan() {
  const navigate = useNavigate();
  const search = useSearch({ from: '/' }) as { plan?: string };

  const plan = parsePlan(search.plan);

  const updatePlan = useCallback(
    (updates: Partial<LaunchPlan>) => {
      // Re-parse the current URL plan inside the updater so concurrent
      // updates within the same event handler compose correctly without
      // relying on React state. `navigate` with the function form of
      // `search` will use the latest URL state at the time of the call.
      navigate({
        to: '/',
        search: (prev: Record<string, unknown>) => {
          const current = parsePlan(prev.plan as string | undefined);
          const next = { ...current, ...updates };
          return { ...prev, plan: encodePlan(next) };
        },
        replace: true,
      });
    },
    [navigate],
  );

  const resetPlan = useCallback(() => {
    navigate({
      to: '/',
      search: (prev: Record<string, unknown>) => {
        const { plan: _omit, ...rest } = prev;
        return rest;
      },
      replace: true,
    });
  }, [navigate]);

  return { plan, updatePlan, resetPlan };
}
