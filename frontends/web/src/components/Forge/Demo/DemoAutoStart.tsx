// Copyright (c) 2025 Reliant Labs

/**
 * The public demo starts the forge tour on every visit (demo shell only,
 * demoShellRoutes.tsx). Renders nothing.
 *
 *  - Once per PAGE LOAD: module state, not a ref, so a remount does not start
 *    it again, and Finish or End tour (which clear `?tour=`) never loop back
 *    into it. "Every visit" is every load.
 *  - A `?tour=` that already names a forge step (a deep link, or a reload in
 *    the middle of the tour) is left alone.
 *  - Nothing is stored: the tour's storage is memory (demoShellBoot.ts).
 *  - Phones get JourneyTour's "open this on a computer" card, as everywhere.
 */

import { useEffect } from "react";
import { useSearch } from "@tanstack/react-router";

import { getJourneyStep } from "@/components/Onboarding/journeys/registry";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { useTourNavigation } from "@/components/Onboarding/useTourNavigation";
import { trackEvent } from "@/lib/analytics";

let autoStarted = false;

/** Whether a raw `?tour=` already points into the forge journey. */
export function namesForgeStep(raw: unknown): boolean {
  const parsed = parseTourParam(raw);
  if (parsed?.kind === "journey") return parsed.journey === "forge";
  if (parsed?.kind === "step") return !!getJourneyStep("forge", parsed.stepId);
  return false;
}

export function DemoAutoStart() {
  const { startJourney } = useTourNavigation();
  const search = useSearch({ strict: false }) as { tour?: unknown };

  useEffect(() => {
    if (autoStarted) return;
    autoStarted = true;
    const deepLink = namesForgeStep(search.tour);
    trackEvent("demo_tour_autostarted", { deep_link: deepLink });
    if (!deepLink) startJourney("forge", "demo");
    // Once per load, with the URL it arrived on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return null;
}

/** Test seam: forget that this page load started the tour. */
export function resetDemoAutoStartForTests(): void {
  autoStarted = false;
}
