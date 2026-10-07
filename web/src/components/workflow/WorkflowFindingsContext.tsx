/**
 * The current canvas's validation findings, for the parts of the builder that
 * draw them on the thing they are about: the config panel (each field's
 * inline error) and the step's problem list.
 *
 * Two levels: the builder provides every finding by step, plus a "focus"
 * request when the user picks a finding in the problems list; a config panel
 * scopes that to its own step, so a field asks only `useFieldFindings(key)`.
 * Without a provider every lookup is empty, which is what keeps the shared
 * field renderer unchanged everywhere else it is used.
 */
import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { LocatedFinding } from "./workflowFindings";

/** "Take me to this finding": select the step, open its panel, focus the field. */
export interface FindingFocus {
  nodeId: string;
  fieldKey?: string;
  /** Bumped on every request, so picking the same finding twice refocuses. */
  seq: number;
}

interface BuilderFindings {
  byNode: ReadonlyMap<string, LocatedFinding[]>;
  focus: FindingFocus | null;
}

const BuilderFindingsContext = createContext<BuilderFindings | null>(null);
const NodeScopeContext = createContext<string | null>(null);

export function WorkflowFindingsProvider({
  byNode,
  focus,
  children,
}: BuilderFindings & { children: ReactNode }) {
  const value = useMemo(() => ({ byNode, focus }), [byNode, focus]);
  return <BuilderFindingsContext.Provider value={value}>{children}</BuilderFindingsContext.Provider>;
}

/** Scopes field lookups to one step (the config panel's). */
export function NodeFindingsScope({ nodeId, children }: { nodeId: string; children: ReactNode }) {
  return <NodeScopeContext.Provider value={nodeId}>{children}</NodeScopeContext.Provider>;
}

const EMPTY: LocatedFinding[] = [];

/** Every finding about a step, and the pending focus request for it. */
export function useNodeFindings(nodeId: string | undefined): { findings: LocatedFinding[]; focus: FindingFocus | null } {
  const ctx = useContext(BuilderFindingsContext);
  if (!ctx || !nodeId) return { findings: EMPTY, focus: null };
  const focus = ctx.focus && ctx.focus.nodeId === nodeId ? ctx.focus : null;
  return { findings: ctx.byNode.get(nodeId) ?? EMPTY, focus };
}

/** The findings about one field of the scoped step, and whether to focus it. */
export function useFieldFindings(fieldKey: string): { findings: LocatedFinding[]; focusSeq: number | null } {
  const nodeId = useContext(NodeScopeContext);
  const { findings, focus } = useNodeFindings(nodeId ?? undefined);
  const forField = useMemo(() => findings.filter((f) => f.fieldKey === fieldKey), [findings, fieldKey]);
  return {
    findings: forField,
    focusSeq: focus && focus.fieldKey === fieldKey ? focus.seq : null,
  };
}
