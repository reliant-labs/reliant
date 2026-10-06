// Copyright (c) 2025 Reliant Labs

/**
 * Renaming a step rewrites the expressions that name it.
 *
 * A step's id is the path every other step reads it by:
 * `{{nodes.call_llm.content}}` in a template, `nodes.call_llm.tool_calls.size() > 0`
 * in a Switch condition or a loop's `while`, `nodes["call_llm"]` in either.
 * Renaming the step without rewriting those leaves expressions that fail
 * validation or, worse, quietly read nothing. So a rename rewrites every
 * `nodes.<old>` reference, wherever a string holds one.
 *
 * An inline sub-workflow (a loop body or an Agent's inline definition) is its
 * own scope: its `nodes.x` names ITS steps, not the parent's, so it is left
 * alone.
 */

const IDENT = /^[A-Za-z][A-Za-z0-9_]*$/;

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** `text` with every reference to step `oldId` pointing at `newId`. */
export function rewriteNodeReferencesInText(text: string, oldId: string, newId: string): string {
  if (!text.includes(oldId)) return text;
  const old = escapeRegExp(oldId);
  return text
    // nodes.old   — but not nodes.older, and not something.nodes.old
    .replace(new RegExp(`(?<![A-Za-z0-9_.])(nodes\\s*\\.\\s*)${old}(?![A-Za-z0-9_])`, "g"), `$1${newId}`)
    // nodes["old"] / nodes['old']
    .replace(new RegExp(`(?<![A-Za-z0-9_.])(nodes\\s*\\[\\s*)(["'])${old}\\2(\\s*\\])`, "g"), `$1$2${newId}$2$3`);
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object") return false;
  const proto = Object.getPrototypeOf(value);
  return proto === Object.prototype || proto === null;
}

/** An inline workflow definition: a separate scope of step ids. */
function isSubWorkflow(value: Record<string, unknown>): boolean {
  return Array.isArray(value.nodes) && ("edges" in value || "entry" in value || "name" in value);
}

/**
 * `value` with every string's references to `oldId` rewritten. Objects and
 * arrays are copied only where something changed, so an untouched step keeps
 * its identity.
 */
export function rewriteNodeReferences<T>(value: T, oldId: string, newId: string): T {
  if (!IDENT.test(oldId) || !IDENT.test(newId) || oldId === newId) return value;
  const visit = (current: unknown): unknown => {
    if (typeof current === "string") return rewriteNodeReferencesInText(current, oldId, newId);
    if (Array.isArray(current)) {
      let changed = false;
      const next = current.map((item) => {
        const rewritten = visit(item);
        if (rewritten !== item) changed = true;
        return rewritten;
      });
      return changed ? next : current;
    }
    if (isPlainObject(current)) {
      if (isSubWorkflow(current)) return current;
      let changed = false;
      const next: Record<string, unknown> = {};
      for (const [key, item] of Object.entries(current)) {
        const rewritten = visit(item);
        if (rewritten !== item) changed = true;
        next[key] = rewritten;
      }
      return changed ? next : current;
    }
    return current;
  };
  return visit(value) as T;
}

interface WorkflowNodeFields {
  outputs?: Record<string, string>;
  entry?: string[];
  resumeNode?: string;
  transitionTo?: string;
}

/**
 * The workflow-level fields that name steps: `outputs` expressions, and the
 * `entry`, `resume_node` and `transition_to` ids.
 */
export function rewriteWorkflowNodeReferences<W extends WorkflowNodeFields>(workflow: W, oldId: string, newId: string): W {
  if (oldId === newId) return workflow;
  const outputs = workflow.outputs ? rewriteNodeReferences(workflow.outputs, oldId, newId) : workflow.outputs;
  const rename = (id: string | undefined) => (id === oldId ? newId : id);
  const entry = workflow.entry?.some((id) => id === oldId) ? workflow.entry.map((id) => rename(id)!) : workflow.entry;
  const resumeNode = rename(workflow.resumeNode);
  const transitionTo = rename(workflow.transitionTo);
  if (outputs === workflow.outputs && entry === workflow.entry && resumeNode === workflow.resumeNode && transitionTo === workflow.transitionTo) {
    return workflow;
  }
  return { ...workflow, outputs, entry, resumeNode, transitionTo };
}
