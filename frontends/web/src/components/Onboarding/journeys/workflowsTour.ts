/**
 * What the workflows journey's emit and anchor sites need to know about the
 * tour without importing the journey itself: whether `?tour=` names it, and
 * the one line the Turn on dialog shows during W6.
 */

import { parseTourParam } from "../tourParam";

/** The step whose card explains the Turn on dialog (journeys-spec.md W6). */
export const WF_ENABLE_STEP = "wf-enable";

/** The line DeclaredAutomationForm shows above its footer during W6. */
export const ENABLE_TOUR_NOTE = "Turning it on is up to you. The tour works the same either way.";

function tourStepOf(raw: unknown): { journey?: string; stepId: string | null } | null {
  const parsed = parseTourParam(raw);
  if (!parsed || parsed.kind === "picker") return null;
  return parsed.kind === "journey" ? { journey: parsed.journey, stepId: parsed.stepId } : { stepId: parsed.stepId };
}

/** Whether a raw `?tour=` names the workflows journey (`workflows:wf-new`, a bare `wf-*` id). */
export function isWorkflowsTour(raw: unknown): boolean {
  const step = tourStepOf(raw);
  if (!step) return false;
  return step.journey ? step.journey === "workflows" : !!step.stepId?.startsWith("wf-");
}

/** The Turn on dialog's tour note for a raw `?tour=`, if W6 is open. */
export function enableTourNote(raw: unknown): string | undefined {
  const step = tourStepOf(raw);
  return step && isWorkflowsTour(raw) && step.stepId === WF_ENABLE_STEP ? ENABLE_TOUR_NOTE : undefined;
}

/** `?tour=` from the address bar, for code outside the router's tree (dialogs mounted at the root). */
export function currentTourParam(): string | null {
  try {
    return new URLSearchParams(window.location.search).get("tour");
  } catch {
    return null;
  }
}
