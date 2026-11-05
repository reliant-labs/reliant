// Copyright (c) 2025 Reliant Labs

/**
 * What a visitor arrived on the forge shell with: the landing handoff (`rid`,
 * `utm_*`) and the journey they picked (`journey=forge|workflows`), stamped on
 * the shell's FIRST page_visited event so a forge funnel can start at the
 * landing page.
 *
 * Captured when the forge route module evaluates — before the router exists,
 * so before the auth guard's sign-in redirect replaces the URL. `rid`/`utm_*`
 * go through landingAttribution's reader (read-only here: the boot already
 * holds them, attributionBoot.ts), so the shell keeps exactly what the
 * telemetry runtime keeps; this module adds `journey`, which that one does
 * not carry.
 */

import { handoffQuery, readLandingAttribution } from "@/lib/landingAttribution";

/** The journeys the landing picker offers (landing.md: `?journey=forge|workflows`). */
const JOURNEYS = new Set(["forge", "workflows"]);

/** First-visit props for this search string, falling back to a held handoff. Pure but for the read. */
export function forgeEntryProps(search: string, held: string | undefined = readLandingAttribution()): Record<string, string> {
  const props: Record<string, string> = {};
  const handoff = handoffQuery(search) || held || "";
  for (const [key, value] of new URLSearchParams(handoff)) props[key] = value;
  let journey: string | null = null;
  try {
    journey = new URLSearchParams(search).get("journey");
  } catch {
    journey = null;
  }
  const normalized = journey?.trim().toLowerCase() ?? "";
  if (JOURNEYS.has(normalized)) props.journey = normalized;
  return props;
}

let captured: Record<string, string> | null = null;

/** The entry this page load arrived with; computed once, on first call. */
export function captureForgeEntry(): Record<string, string> {
  if (captured) return captured;
  captured = typeof window === "undefined" ? {} : forgeEntryProps(window.location.search);
  return captured;
}
