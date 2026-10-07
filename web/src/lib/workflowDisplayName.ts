import { useGlobalDataStore } from "../store/globalDataStore";

/** Strip protocol prefixes from a workflow reference. */
export function stripWorkflowRefPrefix(ref: string): string {
  if (ref.startsWith("builtin://")) return ref.slice("builtin://".length);
  return ref;
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
  return resolveFromList(ref, useGlobalDataStore.getState().workflows);
}

/** Reactive lookup against the cached workflow list (no network). */
export function useWorkflowDisplayName(ref: string): string {
  const workflows = useGlobalDataStore((state) => state.workflows);
  return resolveFromList(ref, workflows);
}
