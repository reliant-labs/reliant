/**
 * Journey analytics. Every event carries `journey` and the journey's
 * `version`, so a funnel can compare a journey's versions; step events carry
 * `step` too. Props go through trackEvent (snake_cased, strings/numbers).
 *
 *   journey_selected        {journey, source: landing|picker|menu}
 *   journey_step_viewed     {journey, step, version, step_index, total_steps}
 *   journey_step_completed  {journey, step, version, via: action|next|skip, duration_ms, signal?}
 *   journey_completed       {journey, version, duration_ms}
 *   journey_skipped         {journey, step, version}
 *
 * Durations are wall-clock from the step's (or journey's) first view in this
 * page load; module state, because the views and the completions are fired
 * from different components.
 */

import { trackEvent } from "../../lib/analytics";
import { getJourney, stepPosition } from "./journeys/registry";
import type { JourneyId, JourneySource } from "./tourParam";
import type { StepCompletionVia } from "./types";

let stepStartedAt: { key: string; at: number } | null = null;
let journeyStartedAt: { journey: JourneyId; at: number } | null = null;

const version = (journey: JourneyId) => getJourney(journey).version;

export function trackJourneySelected(
  journey: JourneyId,
  source: JourneySource,
  extra?: Record<string, string>,
): void {
  journeyStartedAt = { journey, at: Date.now() };
  trackEvent("journey_selected", { journey, source, version: version(journey), ...extra });
}

export function trackJourneyStepViewed(journey: JourneyId, step: string): void {
  const key = `${journey}:${step}`;
  if (stepStartedAt?.key === key) return;
  stepStartedAt = { key, at: Date.now() };
  if (journeyStartedAt?.journey !== journey) journeyStartedAt = { journey, at: Date.now() };
  const { index, total } = stepPosition(journey, step);
  trackEvent("journey_step_viewed", {
    journey,
    step,
    version: version(journey),
    step_index: index,
    total_steps: total,
  });
}

/** `signal` names the action that completed it (the step's `signal`), sent when `via` is "action". */
export function trackJourneyStepCompleted(
  journey: JourneyId,
  step: string,
  via: StepCompletionVia,
  signal?: string,
): void {
  const key = `${journey}:${step}`;
  const duration = stepStartedAt?.key === key ? Date.now() - stepStartedAt.at : 0;
  trackEvent("journey_step_completed", {
    journey,
    step,
    version: version(journey),
    via,
    duration_ms: duration,
    ...(via === "action" && signal ? { signal } : {}),
  });
}

export function trackJourneyCompleted(journey: JourneyId): void {
  const duration = journeyStartedAt?.journey === journey ? Date.now() - journeyStartedAt.at : 0;
  journeyStartedAt = null;
  stepStartedAt = null;
  trackEvent("journey_completed", { journey, version: version(journey), duration_ms: duration });
}

/** `journey` is "none" when the picker itself was dismissed. */
export function trackJourneySkipped(journey: JourneyId | "none", step: string): void {
  journeyStartedAt = null;
  stepStartedAt = null;
  trackEvent("journey_skipped", {
    journey,
    step,
    ...(journey === "none" ? {} : { version: version(journey) }),
  });
}

/** Test-only. */
export function __resetJourneyAnalytics(): void {
  stepStartedAt = null;
  journeyStartedAt = null;
}
