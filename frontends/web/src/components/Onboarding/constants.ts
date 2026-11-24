/**
 * Onboarding Constants
 *
 * Shared settings keys for the Setup Guide and the tour. The guide's items
 * are checklistItems.ts; the tour's steps are journeys (journeys/registry.ts
 * and JOURNEYS.md).
 */

import type { JourneyId } from "./tourParam";

// ─── Settings Keys ───────────────────────────────────────────────────────────

export const CHECKLIST_SETTINGS_KEYS = {
  COMPLETED_ITEMS: "onboarding.checklist.completed_items",
  PANEL_STATE: "onboarding.checklist.panel_state",
  WELCOME_SHOWN: "onboarding.checklist.welcome_shown",
} as const;

/** Tour-specific settings keys */
export const TOUR_SETTINGS_KEYS = {
  /** Some journey (or the picker) was finished or skipped; also the retired single tour's flag. */
  COMPLETED: "onboarding.completed",
  SKIPPED_ALL: "onboarding.skipped_all",
  // No current-step key: the URL (?tour=<journey>:<step>) is the source of
  // truth for the active step.
  /** `<journey>:<step>` keys (bare step ids from the retired tour may remain). */
  COMPLETED_STEPS: "onboarding.completed_steps",
  SKIPPED_STEPS: "onboarding.skipped_steps",
  /** The journey the user last chose. */
  LAST_JOURNEY: "onboarding.journey",
  /** `<journey>:<step>` the user ended the tour on, offered as "Resume". */
  RESUME: "onboarding.journey_resume",
  /** JSON per-journey context ({ workflows: { workflowSlug, … } }). */
  JOURNEY_CONTEXT: "onboarding.journey_context",
} as const;

/**
 * The settings key recording a journey's outcome at one version
 * ("completed" | "skipped" | "shown"). Per version, so a changed journey is
 * offered again to users who completed an older one, and never loops.
 */
export function journeyCompletionKey(journey: JourneyId, version: number): string {
  return `onboarding.tour.${journey}.v${version}.completed`;
}