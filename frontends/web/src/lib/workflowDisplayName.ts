import { getCachedWorkflows, useCachedWorkflows } from "../hooks/workflow-queries";

/** Strip the scheme from a workflow reference: "builtin://agent", "project://triage". */
export function stripWorkflowRefPrefix(ref: string): string {
  return ref.replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
}

/** "forge-one-shot" -> "Forge One Shot". */
export function formatWorkflowSlug(ref: string): string {
  return stripWorkflowRefPrefix(ref)
    .replace(/[-_]/g, " ")
    .split(" ")
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/**
 * Single source of truth for a workflow's display name: the definition's
 * `title` when set, otherwise the slug title-cased.
 */
export function workflowDisplayName(workflow: { name: string; title?: string | null }): string {
  const title = workflow.title?.trim();
  return title ? title : formatWorkflowSlug(workflow.name);
}

type TitledWorkflow = { name: string; title?: string };

function resolveFromList(ref: string, workflows: readonly TitledWorkflow[]): string {
  const slug = stripWorkflowRefPrefix(ref);
  const match = workflows.find((w) => stripWorkflowRefPrefix(w.name) === slug);
  return workflowDisplayName({ name: ref, title: match?.title });
}

/** Non-reactive lookup against the cached workflow list (no network). */
export function getCachedWorkflowDisplayName(ref: string): string {
  return resolveFromList(ref, getCachedWorkflows());
}

/** Reactive lookup against the cached workflow list (no network). */
export function useWorkflowDisplayName(ref: string): string {
  return resolveFromList(ref, useCachedWorkflows());
}
