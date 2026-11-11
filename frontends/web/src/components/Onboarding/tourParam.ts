/**
 * The `?tour=` grammar, and the journey ids, with no dependencies.
 *
 * Kept free of React, stores and journey content so the boot-time landing
 * handoff (lib/landingAttribution.ts) and the route schemas can import it
 * without dragging the tour in. Which step ids exist is the registry's
 * business (journeys/registry.ts); this module only knows the shape.
 *
 *   ?tour=workflows:wf-new         journey `workflows`, step `wf-new` (canonical)
 *   ?tour=wf-new                   a bare step id: step ids are unique across
 *                                  journeys, so the registry finds its journey
 *   ?tour=workflows                journey `workflows`, its first step
 *   ?tour=pick                     the journey picker (after onboarding)
 *   ?tour=pick:menu                the journey picker, opened from a menu
 *   ?tour=chat-and-sidebars        a step id of the retired single tour,
 *                                  still honoured (LEGACY_TOUR_STEP_IDS)
 */

/** Every journey, in picker order. */
export const JOURNEY_IDS = ["forge", "workflows"] as const;

export type JourneyId = (typeof JOURNEY_IDS)[number];

export function isJourneyId(value: unknown): value is JourneyId {
  return typeof value === "string" && (JOURNEY_IDS as readonly string[]).includes(value);
}

/** The other journey: what "Try the other journey" offers. */
export function otherJourney(journey: JourneyId): JourneyId {
  return journey === "forge" ? "workflows" : "forge";
}

/** Where a journey choice came from, for `journey_selected`. */
export type JourneySource = "landing" | "picker" | "menu" | "demo";

/** The `?tour=` value that opens the picker. */
export const TOUR_PICKER = "pick";

/** `?tour=` that opens the picker; `menu` records that a menu opened it. */
export function pickerParam(source?: "menu"): string {
  return source ? `${TOUR_PICKER}:${source}` : TOUR_PICKER;
}

/** `?tour=` for a journey step, or for the journey's first step when stepId is omitted. */
export function formatTourParam(journey: JourneyId, stepId?: string): string {
  return stepId ? `${journey}:${stepId}` : journey;
}

/**
 * Step ids of the single tour that journeys replaced. A bookmarked or
 * in-flight `?tour=<old id>` keeps working: it opens the journey that now
 * covers that ground, at its first step. The chat-system steps map to the
 * workflows journey (the product's default story); Apps maps to forge.
 */
export const LEGACY_TOUR_STEP_IDS: Readonly<Record<string, JourneyId>> = {
  "chat-and-sidebars": "workflows",
  "workflow-controls": "workflows",
  "workspaces": "workflows",
  "deployments": "forge",
  "workflow-intro": "workflows",
  "workflow-hub": "workflows",
  "workflow-builder": "workflows",
};

export type ParsedTourParam =
  | { kind: "picker"; source: "onboarding" | "menu" }
  | { kind: "journey"; journey: JourneyId; stepId: string | null }
  /** A bare step id; which journey declares it is the registry's to say. */
  | { kind: "step"; stepId: string };

const BARE_ID_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/**
 * Parse a raw `?tour=` value. Unknown shapes are null (no tour), never an
 * error: the param is user-editable. Whether `stepId` exists in the journey
 * is checked by the caller against the registry.
 */
export function parseTourParam(raw: unknown): ParsedTourParam | null {
  if (typeof raw !== "string" || raw === "") return null;
  if (raw === TOUR_PICKER) return { kind: "picker", source: "onboarding" };
  if (raw === `${TOUR_PICKER}:menu`) return { kind: "picker", source: "menu" };
  const sep = raw.indexOf(":");
  const journey = sep === -1 ? raw : raw.slice(0, sep);
  if (!isJourneyId(journey)) {
    // A bare id: a journey step (wins), or a retired single-tour id.
    return sep === -1 && BARE_ID_RE.test(raw) ? { kind: "step", stepId: raw } : null;
  }
  const stepId = sep === -1 ? null : raw.slice(sep + 1);
  return { kind: "journey", journey, stepId: stepId || null };
}

/**
 * Root-route search middleware: keep `?tour=` across navigations that don't
 * mention it. A journey spans pages the user moves between themselves — a
 * Link or a navigate({ to }) with its own search would otherwise drop the
 * param and silently end the tour mid-step. Only an explicit
 * `tour: undefined` (every tour exit, useTourNavigation) clears it.
 */
export function retainTourParam({
  search,
  next,
}: {
  search: Record<string, unknown>;
  next: (search: Record<string, unknown>) => Record<string, unknown>;
}): Record<string, unknown> {
  const result = next(search);
  if (!("tour" in result) && search.tour !== undefined) return { ...result, tour: search.tour };
  return result;
}
