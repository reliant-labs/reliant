/**
 * What the builder's trigger lane needs from its host: which workflow and
 * project it shows, the workflow's declared triggers, whether its Chat
 * trigger is on, and what each card's affordances do.
 *
 * A context rather than node data, deliberately: node data is what the builder
 * serialises as the graph. Declared triggers ARE part of the definition
 * (research/INTEGRATIONS_V1_BRIEF.md §3a), but they live in the workflow's
 * `triggers:` field — held by the builder and edited through
 * WorkflowMutationContext — never in a node. Activations (trigger rows) are
 * read from ListTriggers at render time. Outside a provider the lane shows
 * only the Chat card.
 */

import { createContext, useContext } from "react";
import type { Trigger } from "../../api/trigger-grpc";
import type { DeclaredTrigger, TriggerFinding } from "../../lib/declaredTriggers";

export interface TriggerRailContextValue {
  /** The workflow on the canvas, as the builder knows it. */
  workflowRef: string;
  /** The current project; ad hoc automations are listed in it only. */
  projectId: string;
  /**
   * Whether a trigger can be activated or added as an automation. False for
   * a workflow that has never been saved, since an activation names a stored
   * workflow (and its stored declaration).
   */
  canAddTrigger: boolean;
  /** Whether the definition can be edited (false for builtin/read-only). */
  canEditDefinition: boolean;
  /** The workflow's declared `triggers:`, in definition order. */
  declared: readonly DeclaredTrigger[];
  /** Server findings for a declared trigger (index + name), from the last validation. */
  findingsFor: (index: number, name: string) => TriggerFinding[];
  /** Declared triggers edited since the last save: activating them uses the stored version. */
  unsavedDeclared: ReadonlySet<string>;
  /** The declared trigger whose panel is open, for its card's selected state. */
  selectedDeclared?: number | null;
  /**
   * The Chat trigger: whether a chat can start this workflow (the
   * definition's `automation_only`, inverted). Off keeps it out of every chat
   * picker, and the server refuses a chat start of it.
   */
  chatEnabled: boolean;
  onSetChatEnabled: (enabled: boolean) => void;
  onEditTrigger: (trigger: Trigger) => void;
  onAddTrigger: () => void;
  onEditDeclared: (index: number) => void;
  onActivateDeclared: (index: number) => void;
}

const TriggerRailCtx = createContext<TriggerRailContextValue | null>(null);

export const TriggerRailProvider = TriggerRailCtx.Provider;

export function useTriggerRailContext(): TriggerRailContextValue | null {
  return useContext(TriggerRailCtx);
}
