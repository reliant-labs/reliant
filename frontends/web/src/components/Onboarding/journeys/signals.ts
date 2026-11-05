/**
 * Tour signals: the events a journey step can wait for, declared into the
 * app's existing typed bus (`@/lib/events`). The emit sites own the actions
 * (NewWorkflowDialog, WorkflowBuilder, the Automations dialog, …); emitting
 * is free when no step is listening. Emit through `tourEvents` (tourEvents.ts)
 * so the bus is initialised even on a shell that never set it up.
 *
 * To add a signal: add it to TourSignals with its payload AND to
 * TOUR_SIGNAL_NAMES (the type check below fails if they drift), then emit it
 * where the action happens. Names are `<area>:<camelCasePastTense>`, the
 * bus's convention. JOURNEYS.md lists them.
 */

import type { ChecklistItemId } from "../types";

export interface TourSignals {
  /** A user-owned draft exists. `start` names the path that made it (e.g. "tour-fix-my-pr"). */
  "workflow:draftCreated": { slug: string; draftId?: string; start?: string };
  /** Two canvas nodes were connected (onConnect, CanvasInsertApi.connect). */
  "workflow:edgeConnected": { sourceId: string; targetId: string; sourceHandle?: string };
  /** A canvas node was selected. */
  "workflow:nodeSelected": { nodeId: string; type: string };
  /** A loop node was expanded on the canvas. */
  "workflow:loopExpanded": { nodeId: string };
  /** A node was placed; `anchor` says how it was inserted. */
  "workflow:nodeAdded": {
    nodeId: string;
    type: string;
    anchor?: "splice" | "output" | "selection" | "end";
    spliceTarget?: string;
  };
  /** A trigger was declared on the workflow (schedule, webhook, …). */
  "workflow:triggerDeclared": { name: string; source: string };
  /** The workflow saved. */
  "workflow:saved": { slug: string; triggerSources: string[] };
  "automation:dialogOpened": { workflowRef: string; declaredTrigger?: string };
  "automation:dialogClosed": { workflowRef: string; declaredTrigger?: string };
  /** "Runs on" changed in the Enable dialog. */
  "automation:machineChosen": { machine: string };
  /** The Ask-an-agent surface opened (chat or handoff sheet). */
  "askAgent:opened": { subjectKind: string; mode: string };
  /** Something was copied from the handoff sheet. */
  "askAgent:copied": { what: string };
  /** An onboarding-checklist item flipped to complete (onboardingChecklistStore). */
  "checklist:itemCompleted": { item: ChecklistItemId };
}

export type TourSignalName = keyof TourSignals;

/** Every signal, so a `when` predicate can see whichever fired. Keep in sync with TourSignals. */
export const TOUR_SIGNAL_NAMES = [
  "workflow:draftCreated",
  "workflow:edgeConnected",
  "workflow:nodeSelected",
  "workflow:loopExpanded",
  "workflow:nodeAdded",
  "workflow:triggerDeclared",
  "workflow:saved",
  "automation:dialogOpened",
  "automation:dialogClosed",
  "automation:machineChosen",
  "askAgent:opened",
  "askAgent:copied",
  "checklist:itemCompleted",
] as const satisfies readonly TourSignalName[];

// Compile-time drift check: every TourSignals key is listed.
type Missing = Exclude<TourSignalName, (typeof TOUR_SIGNAL_NAMES)[number]>;
const _allListed: Missing extends never ? true : Missing = true;
void _allListed;

declare module "../../../lib/events" {
  interface EventMap extends TourSignals {}
}
