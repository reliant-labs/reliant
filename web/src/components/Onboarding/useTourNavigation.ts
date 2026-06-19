/**
 * Tour Navigation Hook
 *
 * Single source of truth for "what step is the tour on" and "how do I move
 * between steps". The active step lives in the URL as `?tour=<step-id>`; this
 * hook reads/writes that param via tanstack-router. There are no useEffects
 * here — every transition is the direct, synchronous result of a user action.
 *
 * Step components import this hook (not `useTourStore`) for navigation.
 * `useTourStore` is now strictly the persistence/achievement layer.
 */

import { useCallback } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import {
  ONBOARDING_STEPS,
  ONBOARDING_STEP_IDS,
  getNextStepId,
  getPreviousStepId,
} from "./constants";
import type { OnboardingStepId } from "./types";
import { useTourStore } from "../../store/tourStore";
import { useChatStore } from "../../store/chatStore";

// Whitelist of valid step IDs. The route schemas reject unknown values via
// Zod, but routes without a schema (or tests using a memory router) can let
// arbitrary `?tour=...` strings through — coerce those to null so the wizard
// never tries to render a non-existent step.
const VALID_STEP_IDS = new Set<string>(ONBOARDING_STEP_IDS);

async function promptApiKeyIfNeededAfterTour(): Promise<void> {
  const { useApiKeySetupStore } = await import("../../store/apiKeySetupStore");
  if (useApiKeySetupStore.getState().hasApiKey === true) return;
  useApiKeySetupStore.setState({ hasChecked: false });
  await useApiKeySetupStore.getState().ensureApiKeyOrShowModal();
}

// ─── Per-step expected pathname ─────────────────────────────────────────────
// Tells callers (and the step components) where each step's spotlight lives:
//
//   "/workflow"  → hub-only step (matched with === '/workflow' or startsWith
//                  if router appends a trailing slash, callers should use
//                  startsWith).
//   "/workflow/" → workflow-builder routes — pathname must start with
//                  '/workflow/' (the trailing slash distinguishes hub).
//   null         → step has no path constraint; render wherever the user is.
//
// Returned as a path prefix; the wizard's step components check
// `pathname.startsWith(STEP_EXPECTED_PATH(id))`.

export function STEP_EXPECTED_PATH(stepId: OnboardingStepId): string | null {
  switch (stepId) {
    case "workflow-hub":
      return "/workflow";
    case "workflow-builder":
    case "workflow-builder-chat":
      return "/workflow/";
    default:
      return null;
  }
}

// ─── Hook ───────────────────────────────────────────────────────────────────

export interface TourNavigation {
  currentStepId: OnboardingStepId | null;
  isWizardActive: boolean;
  goToStep: (stepId: OnboardingStepId) => void;
  exitTour: () => void;
  completeAndAdvance: () => Promise<void>;
  goBack: () => void;
  skipAll: () => Promise<void>;
}

export function useTourNavigation(): TourNavigation {
  const navigate = useNavigate();
  // Reading via { strict: false } so the hook works at the root (which has no
  // search schema). Wizard is mounted globally — we can't constrain `from`.
  const search = useSearch({ strict: false }) as { tour?: string };
  const rawTour = search.tour;
  const currentStepId: OnboardingStepId | null =
    rawTour && VALID_STEP_IDS.has(rawTour) ? (rawTour as OnboardingStepId) : null;
  const isWizardActive = currentStepId !== null;

  const goToStep = useCallback(
    (stepId: OnboardingStepId) => {
      const expected = STEP_EXPECTED_PATH(stepId);
      if (stepId === "workflow-hub") {
        // workflow-hub specifically needs `/workflow` (not the builder).
        void navigate({
          to: "/workflow",
          search: (prev: Record<string, unknown>) => ({ ...prev, tour: stepId }),
        });
        return;
      }
      if (expected === "/workflow/") {
        void navigate({
          to: "/workflow/$workflowName",
          params: { workflowName: "builtin://get-it-right" },
          search: (prev: Record<string, unknown>) => ({
            ...prev,
            drill: "attempt",
            tour: stepId,
          }),
        });
        return;
      }
      // Path-agnostic step: just update the search param on whatever route
      // the user is on. tanstack-router preserves the current path when no
      // `to` is supplied. With `to` omitted the router can't resolve which
      // route's search schema applies, so it types the reducer's return as
      // `never`; the runtime contract (merge `tour` into the current
      // search) is route-agnostic, so we cast the reducer to satisfy the
      // over-narrowed signature without changing behavior.
      void navigate({
        search: ((prev: Record<string, unknown>) => ({ ...prev, tour: stepId })) as never,
      });
    },
    [navigate],
  );

  const exitTour = useCallback(() => {
    // See goToStep: `to` is omitted so the router over-narrows the reducer
    // return to `never`; the route-agnostic "strip the tour param" contract
    // is unchanged, so cast to satisfy the signature.
    void navigate({
      search: ((prev: Record<string, unknown>) => {
        const { tour: _tour, ...rest } = prev;
        return rest;
      }) as never,
    });
  }, [navigate]);

  const completeAndAdvance = useCallback(async () => {
    if (!currentStepId) return;
    await useTourStore.getState().completeStep(currentStepId);
    const nextId = getNextStepId(currentStepId);
    if (nextId) {
      goToStep(nextId);
    } else {
      // Last step — tour finished.
      await useTourStore.getState().markTourCompleted();
      exitTour();
      // Match the prior wizard behavior: clear active chat so the user lands
      // on a fresh new-chat view after finishing the tour, then prompt for an
      // API key if we still don't have one.
      useChatStore.getState().clearCurrentChat();
      void promptApiKeyIfNeededAfterTour();
    }
  }, [currentStepId, goToStep, exitTour]);

  const goBack = useCallback(() => {
    if (!currentStepId) return;
    const prevId = getPreviousStepId(currentStepId);
    if (prevId) {
      goToStep(prevId);
    } else {
      // First step — Back = exit.
      exitTour();
    }
  }, [currentStepId, goToStep, exitTour]);

  const skipAll = useCallback(async () => {
    const store = useTourStore.getState();
    // Mark every remaining (not-completed, not-skipped) step as skipped so
    // the persistence layer reflects what the user did.
    for (const step of ONBOARDING_STEPS) {
      if (!store.completedSteps.has(step.id) && !store.skippedSteps.has(step.id)) {
        await store.skipStep(step.id);
      }
    }
    await store.markTourCompleted();
    exitTour();
    useChatStore.getState().clearCurrentChat();
    void promptApiKeyIfNeededAfterTour();
  }, [exitTour]);

  return {
    currentStepId,
    isWizardActive,
    goToStep,
    exitTour,
    completeAndAdvance,
    goBack,
    skipAll,
  };
}
