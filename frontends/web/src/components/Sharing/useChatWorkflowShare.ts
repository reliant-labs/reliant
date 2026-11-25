// Copyright (c) 2025 Reliant Labs

/**
 * The saved workflow a chat was started from, when the caller owns it and can
 * share it: what a chat row's "Share workflow…" acts on.
 *
 * A chat records the workflow it runs by reference (Chat.workflow_name), not
 * by saved-workflow id. GetWorkflow resolves that reference by the rule a run
 * does (the caller's own saved workflow shadows the project's of the same
 * name), and answers a saved workflow only to its owner, with its sharing
 * state. So the reference resolves to the saved workflow the chat runs, as it
 * is now: a builtin or project workflow, or one since deleted, has none.
 */

import { useWorkflowDefinitionQuery } from "@/hooks/workflow-queries";
import { workflowDisplayName } from "@/lib/workflowDisplayName";

import { shareableWorkflow, sharingAvailable, type ShareableWorkflow } from "./shareTargets";

interface ChatWithWorkflow {
  projectId: string;
  workflowName?: string;
  viewerAccess?: boolean;
}

/** Builtins are nobody's to share, so a builtin reference is never read. */
function savedWorkflowRef(chat: ChatWithWorkflow | null | undefined): string | undefined {
  const ref = chat?.workflowName;
  if (!chat?.projectId || chat.viewerAccess || !ref || ref.startsWith("builtin://") || !sharingAvailable()) return undefined;
  return ref;
}

/**
 * `chat`'s saved workflow when its owner can share it, else null. Reads only
 * while `chat` is set (a row whose menu is open), from the cache while fresh.
 */
export function useChatWorkflowShare(chat: ChatWithWorkflow | null | undefined): ShareableWorkflow | null {
  const ref = savedWorkflowRef(chat);
  const query = useWorkflowDefinitionQuery(ref ? chat?.projectId : undefined, ref);
  if (!ref || !query.data) return null;
  return shareableWorkflow(query.data, workflowDisplayName({ name: ref, title: query.data.workflow?.title }));
}
