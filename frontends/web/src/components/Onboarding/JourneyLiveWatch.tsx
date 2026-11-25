// Copyright (c) 2026 Reliant Labs

/**
 * Tells a journey when it goes live and when it stops (`Journey.onLiveChange`).
 *
 * A journey is live while `?tour=` names one of its steps. However it stops
 * (Finish, Exit tour, Back off the first step, a link that drops `?tour=`, or
 * the next journey starting), the URL says so, so this is the one place that
 * reads it: the tour's own controls never have to remember to.
 *
 * Mounted once at the app's root (routes.tsx), outside every guard, so it sees
 * every change whatever the tour UI does. Renders nothing.
 */

import { useEffect } from "react";
import { useLocation } from "@tanstack/react-router";

import { getJourney, resolveTour } from "./journeys/registry";
import { parseTourParam, type JourneyId } from "./tourParam";

/** The journey `?tour=` has a step of, or null (no tour, or the picker). */
export function liveJourneyOf(tour: unknown): JourneyId | null {
  const resolved = resolveTour(parseTourParam(tour));
  return resolved?.kind === "step" ? resolved.journey : null;
}

function notify(journey: JourneyId, live: boolean): void {
  try {
    getJourney(journey).onLiveChange?.(live);
  } catch {
    // A content hook must never wedge the tour.
  }
}

export function JourneyLiveWatch() {
  const live = useLocation({ select: (location) => liveJourneyOf((location.search as Record<string, unknown>).tour) });
  useEffect(() => {
    if (!live) return;
    notify(live, true);
    return () => notify(live, false);
  }, [live]);
  return null;
}
