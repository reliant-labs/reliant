import { useNavigate, useSearch } from '@tanstack/react-router';
import { useCallback } from 'react';
import type { LaunchPlan } from './types';

// URL-backed onboarding plan state.
//
// The plan is stored as a single object-valued `plan` search param on
// `/onboarding`. tanstack-router handles JSON-encoding/URL-encoding via the
// route's Zod `validateSearch` schema (see routes.tsx + routeSchemas.ts).
// Callers pass and receive plain objects — no manual encodeURIComponent or
// JSON.stringify anywhere.

const EMPTY_PLAN: Partial<LaunchPlan> = {};

export function useOnboardingPlan() {
  const navigate = useNavigate();
  const search = useSearch({ from: '/_authenticated/onboarding' });

  // The route schema produces an already-typed Partial<LaunchPlan> here.
  const plan = (search.plan ?? EMPTY_PLAN) as Partial<LaunchPlan>;

  const updatePlan = useCallback(
    (updates: Partial<LaunchPlan>) => {
      // Re-read the current URL plan inside the updater so concurrent updates
      // within the same event handler compose correctly without relying on
      // React state. Returns the navigate promise so callers that follow
      // updatePlan with onNext() can `await` it.
      return navigate({
        to: '/onboarding',
        search: (prev) => {
          const current = (prev.plan ?? EMPTY_PLAN) as Partial<LaunchPlan>;
          const merged = { ...current, ...updates };
          // Drop fields that were explicitly cleared (set to undefined) so
          // they don't sit in the URL as null/undefined.
          const cleaned: Partial<LaunchPlan> = {};
          for (const [k, v] of Object.entries(merged)) {
            if (v !== undefined) {
              (cleaned as Record<string, unknown>)[k] = v;
            }
          }
          const isEmpty = Object.keys(cleaned).length === 0;
          return { ...prev, plan: isEmpty ? undefined : cleaned };
        },
        replace: true,
      });
    },
    [navigate],
  );

  const resetPlan = useCallback(() => {
    navigate({
      to: '/onboarding',
      search: (prev) => {
        const { plan: _omit, ...rest } = prev;
        return rest;
      },
      replace: true,
    });
  }, [navigate]);

  return { plan, updatePlan, resetPlan };
}
