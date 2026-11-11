// Copyright (c) 2025 Reliant Labs
/**
 * Old shared links keep working (v1 back-compat; decided 2026-11-08). On the
 * public demo, a forge tour link naming a step that runs on an open trace
 * (fg-logs, fg-hops, fg-inside, fg-profile), but carrying no `trace`, is a
 * legacy page deep link like `?view=logs&tour=forge:fg-logs`. It explains the
 * page it opens with the page card (PageEntryTour): no prerequisite, Next
 * enabled. Guided steps would otherwise wait on an action that needs a trace
 * the visitor does not have. A tour link that carries its trace, and every
 * other step, stays guided (JourneyTour).
 */
import { isDemoShell } from "../../lib/shell";
import { getJourneyStep } from "./journeys/registry";
import { parseTourParam } from "./tourParam";

export function isLegacyDemoTourLink(search: Record<string, unknown>): boolean {
  if (!isDemoShell() || search.trace) return false;
  const parsed = parseTourParam(search.tour);
  if (!parsed || parsed.kind !== "journey" || parsed.journey !== "forge" || !parsed.stepId) return false;
  const open = getJourneyStep("forge", parsed.stepId)?.route?.open;
  if (!open || typeof open === "function") return false;
  return Boolean(open.search?.trace);
}
