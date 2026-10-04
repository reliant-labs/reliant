/**
 * What the builder's trigger rail needs from its host: which workflow and
 * project it projects, and what editing a line or adding one does.
 *
 * A context rather than node data, deliberately: node data is what the builder
 * serialises, and the rail must never put a trigger into the workflow
 * definition (research/WORKFLOW_UI.md §3.2). Outside a provider the rail shows
 * only the always-present chat line.
 */

import { createContext, useContext } from "react";
import type { Trigger } from "../../api/trigger-grpc";

export interface TriggerRailContextValue {
  /** The workflow on the canvas, as the builder knows it. */
  workflowRef: string;
  /** The current project; the rail lists triggers in it only. */
  projectId: string;
  /**
   * Whether a trigger can be added. False for a workflow that has never been
   * saved, since an automation must name a workflow that exists.
   */
  canAddTrigger: boolean;
  onEditTrigger: (trigger: Trigger) => void;
  onAddTrigger: () => void;
}

const TriggerRailCtx = createContext<TriggerRailContextValue | null>(null);

export const TriggerRailProvider = TriggerRailCtx.Provider;

export function useTriggerRailContext(): TriggerRailContextValue | null {
  return useContext(TriggerRailCtx);
}
