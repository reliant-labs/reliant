// Copyright (c) 2025 Reliant Labs

/**
 * "Used by" on workflow detail (WORKFLOW_UI.md §2.3): the other workflows that
 * reference this one through `ref:`.
 *
 * No request of its own. ListWorkflows already returns every workflow's nodes
 * (builtin, project and yours), and detail has that list loaded for its
 * header, so this is a scan of data in hand.
 *
 * A reference is a `workflow` or `loop` node's `ref`, or a router's workflow
 * candidate, including inside inline bodies at any depth. Refs resolve the way
 * the runtime loads them (load_workflow.go): `builtin://x` names a built-in
 * exactly; anything else is a user or project workflow, found by slug.
 * A ref computed by a CEL expression cannot be resolved without running the
 * workflow, so it is not counted.
 *
 * Direct references only. A workflow that reaches this one through a third is
 * listed under the third.
 */

import type { WorkflowResponse } from "@/api/workflow-grpc";
import { getStepInline, getStepRef, type Step } from "@/types/workflow";

const BUILTIN_PREFIX = "builtin://";

/** The runtime's slug (generateWorkflowSlug): lower, `-` for space and `_`, [a-z0-9-] only. */
export function workflowSlug(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[ _]/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
}

/** What a ref resolves to, as a comparable key; null when it cannot be known statically. */
export function refKey(ref: string): string | null {
  const trimmed = ref.trim();
  if (!trimmed || trimmed.includes("{{")) return null;
  if (trimmed.startsWith(BUILTIN_PREFIX)) return trimmed;
  const bare = trimmed.replace(/^project:\/\//, "");
  const slug = workflowSlug(bare);
  return slug ? `slug:${slug}` : null;
}

interface RouterCandidate {
  ref?: string;
}

/** Every ref a definition's nodes name, inline bodies included. */
export function refsInNodes(nodes: readonly Step[] | undefined, into = new Set<string>()): Set<string> {
  for (const node of nodes ?? []) {
    const ref = getStepRef(node);
    if (ref) into.add(ref);
    if (node.args?.case === "router") {
      const candidates = (node.args.value as { workflows?: RouterCandidate[] }).workflows ?? [];
      for (const candidate of candidates) if (candidate.ref) into.add(candidate.ref);
    }
    const inline = getStepInline(node);
    if (inline?.nodes) refsInNodes(inline.nodes, into);
  }
  return into;
}

/** The workflows (other than the target) whose nodes reference `targetRef`, by name. */
export function workflowsUsing(targetRef: string, workflows: readonly WorkflowResponse[]): WorkflowResponse[] {
  const target = refKey(targetRef);
  if (!target) return [];
  return workflows
    .filter((workflow) => refKey(workflow.name) !== target)
    .filter((workflow) => [...refsInNodes(workflow.nodes)].some((ref) => refKey(ref) === target))
    .sort((a, b) => a.name.replace(BUILTIN_PREFIX, "").localeCompare(b.name.replace(BUILTIN_PREFIX, "")));
}
