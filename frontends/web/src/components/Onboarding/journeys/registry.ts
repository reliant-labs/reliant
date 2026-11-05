/**
 * The journey registry, and the rules that walk a journey's steps.
 *
 * A journey walks `steps` then `outro`, minus any step that doesn't apply
 * here (`shell`, `isAvailable`). Everything that orders or counts steps —
 * Next, Back, "n / total", which step is last — goes through here, never a
 * journey's arrays directly, so a step that doesn't apply is invisible rather
 * than a dead stop. The full static list still defines the URL schema: every
 * declared id is recognised, applicable or not.
 */

import type { Journey, JourneyStep, StepPresentation } from "../types";
import {
  JOURNEY_IDS,
  LEGACY_TOUR_STEP_IDS,
  type JourneyId,
  type ParsedTourParam,
} from "../tourParam";
import { getTourShell, inShell } from "../tourShell";
import { workflowsJourney } from "./workflows";
import { forgeJourney } from "./forge";

export const JOURNEYS: Readonly<Record<JourneyId, Journey>> = {
  workflows: workflowsJourney,
  forge: forgeJourney,
};

const STEP_ID_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/** Every declared step of a journey, in walk order (steps, then outro). */
export function allSteps(journey: JourneyId | Journey): JourneyStep[] {
  const j = typeof journey === "string" ? JOURNEYS[journey] : journey;
  return [...j.steps, ...(j.outro ?? [])];
}

/**
 * Throws on malformed journeys. Run at module load below, so a content
 * mistake fails every test and dev boot instead of shipping a tour that
 * dead-ends. Exported for the registry test.
 */
export function validateJourneys(journeys: Readonly<Record<JourneyId, Journey>>): void {
  const owner = new Map<string, JourneyId>();
  for (const id of JOURNEY_IDS) {
    const journey = journeys[id];
    const where = `[Onboarding] journey "${id}"`;
    if (journey.id !== id) throw new Error(`${where}: declares id "${journey.id}"`);
    if (!Number.isInteger(journey.version) || journey.version < 1) {
      throw new Error(`${where}: version must be an integer >= 1`);
    }
    if (journey.steps.length === 0) throw new Error(`${where}: has no steps`);
    for (const step of allSteps(journey)) {
      if (!STEP_ID_RE.test(step.id)) throw new Error(`${where}: step id "${step.id}" is not kebab-case`);
      // Unique across journeys, so a bare `?tour=<step>` names one step.
      const taken = owner.get(step.id);
      if (taken) throw new Error(`${where}: step id "${step.id}" is already used in "${taken}"`);
      owner.set(step.id, id);
      if (step.type === "spotlight" && !step.target) {
        throw new Error(`${where}: spotlight step "${step.id}" has no target`);
      }
      if (step.type === "multi-spotlight" && !step.targets?.length) {
        throw new Error(`${where}: multi-spotlight step "${step.id}" has no targets`);
      }
      const advance = step.advanceOn ? [step.advanceOn].flat() : [];
      const clickWithoutTarget = advance.some((a) => a.kind === "click" && !a.selector);
      if (clickWithoutTarget && !step.target && !step.phases?.some((p) => p.target)) {
        throw new Error(`${where}: step "${step.id}" advances on a click but names no target or selector`);
      }
      if (step.phases?.slice(1).some((p) => !p.when)) {
        throw new Error(`${where}: step "${step.id}": every phase after the first needs a \`when\``);
      }
    }
  }
}

validateJourneys(JOURNEYS);

export function getJourney(id: JourneyId): Journey {
  return JOURNEYS[id];
}

/** Whether a journey runs here: its shells and its own availability. */
export function isJourneyAvailable(id: JourneyId): boolean {
  const journey = JOURNEYS[id];
  return inShell(journey.shells) && (journey.isAvailable?.() ?? true);
}

/** Journeys this user can take here, in picker order. */
export function availableJourneys(): Journey[] {
  return JOURNEY_IDS.filter(isJourneyAvailable).map((id) => JOURNEYS[id]);
}

export function getJourneyStep(journey: JourneyId, stepId: string): JourneyStep | undefined {
  return allSteps(journey).find((step) => step.id === stepId);
}

/** The journey that declares `stepId`, if any. */
export function journeyOfStep(stepId: string): JourneyId | undefined {
  return JOURNEY_IDS.find((id) => allSteps(id).some((step) => step.id === stepId));
}

/** How a step is drawn when it doesn't say. */
export function stepType(step: StepPresentation & Pick<JourneyStep, "type">): NonNullable<JourneyStep["type"]> {
  if (step.type) return step.type;
  if (step.targets?.length) return "multi-spotlight";
  if (step.target) return "spotlight";
  return "card";
}

/** The step's presentation in this shell, with `phase`'s overrides on top. */
export function presentStep(step: JourneyStep, phase = 0): JourneyStep {
  const byShell = step.copyByShell?.[getTourShell()];
  const p = step.phases?.[phase];
  const merged: JourneyStep = { ...step, ...byShell };
  if (p) {
    const { when: _when, ...overrides } = p;
    void _when;
    Object.assign(merged, overrides);
  }
  return merged;
}

const appliesHere = (step: JourneyStep) => inShell(step.shell) && (step.isAvailable?.() ?? true);

/** The steps this user's journey actually walks here. */
export function activeJourneySteps(journey: JourneyId): JourneyStep[] {
  return allSteps(journey).filter(appliesHere);
}

export function firstStepId(journey: JourneyId): string | null {
  return activeJourneySteps(journey)[0]?.id ?? null;
}

/**
 * The applicable step after `stepId`. Walks the FULL list from its static
 * position, so it still answers when the current step has itself just
 * stopped applying.
 */
export function nextStepId(journey: JourneyId, stepId: string): string | null {
  const steps = allSteps(journey);
  const index = steps.findIndex((step) => step.id === stepId);
  if (index === -1) return null;
  return steps.slice(index + 1).find(appliesHere)?.id ?? null;
}

/** The applicable step before `stepId`; see nextStepId. */
export function previousStepId(journey: JourneyId, stepId: string): string | null {
  const steps = allSteps(journey);
  const index = steps.findIndex((step) => step.id === stepId);
  if (index <= 0) return null;
  return steps.slice(0, index).reverse().find(appliesHere)?.id ?? null;
}

/**
 * 0-based position for "n / total". A step that stopped applying while open
 * shows at the position it would have held, so the counter never reads 0.
 */
export function stepPosition(journey: JourneyId, stepId: string): { index: number; total: number } {
  const steps = allSteps(journey);
  const active = activeJourneySteps(journey);
  const total = active.length;
  const activeIndex = active.findIndex((step) => step.id === stepId);
  if (activeIndex !== -1) return { index: activeIndex, total };
  const staticIndex = steps.findIndex((step) => step.id === stepId);
  const before = active.filter((step) => steps.indexOf(step) < staticIndex).length;
  return { index: Math.max(0, Math.min(before, total - 1)), total };
}

export type ResolvedTour =
  | { kind: "picker"; source: "onboarding" | "menu" }
  | { kind: "step"; journey: JourneyId; stepId: string; step: JourneyStep };

/**
 * Resolve a parsed `?tour=` against the registry:
 *   - a journey with no (or an unknown) step opens at its first step;
 *   - a bare step id opens that step in the journey declaring it;
 *   - a retired single-tour id opens its successor journey's first step.
 * Null when nothing renderable is left: an unknown id, or a journey that
 * doesn't run here.
 */
export function resolveTour(parsed: ParsedTourParam | null): ResolvedTour | null {
  if (!parsed) return null;
  if (parsed.kind === "picker") return parsed;
  let journey: JourneyId;
  let requested: string | null;
  if (parsed.kind === "step") {
    const declaring = journeyOfStep(parsed.stepId);
    const legacy = LEGACY_TOUR_STEP_IDS[parsed.stepId];
    if (declaring) [journey, requested] = [declaring, parsed.stepId];
    else if (legacy) [journey, requested] = [legacy, null];
    else return null;
  } else {
    [journey, requested] = [parsed.journey, parsed.stepId];
  }
  if (!isJourneyAvailable(journey)) return null;
  const declared = requested ? getJourneyStep(journey, requested) : undefined;
  const stepId = declared ? declared.id : firstStepId(journey);
  if (!stepId) return null;
  return { kind: "step", journey, stepId, step: getJourneyStep(journey, stepId)! };
}
