/**
 * Draft-update channel: WORKFLOW_DRAFT_UPDATED user updates, fanned out by draft id.
 *
 * The backend publishes one of these on every workflow-draft write, whoever made it
 * (any chat's agent, another tab). An open editor subscribes for the draft it shows.
 */

export interface WorkflowDraftUpdate {
  draftId: string;
  slug: string;
  version: number;
  /** The draft was deleted; `version` is the last one and is not bumped. */
  deleted?: boolean;
}

type Listener = (update: WorkflowDraftUpdate) => void;

const listenersByDraftId = new Map<string, Set<Listener>>();

export function subscribeToDraftUpdates(draftId: string, listener: Listener): () => void {
  let listeners = listenersByDraftId.get(draftId);
  if (!listeners) {
    listeners = new Set();
    listenersByDraftId.set(draftId, listeners);
  }
  listeners.add(listener);
  return () => {
    const current = listenersByDraftId.get(draftId);
    if (!current) return;
    current.delete(listener);
    if (current.size === 0) listenersByDraftId.delete(draftId);
  };
}

export function publishDraftUpdate(update: WorkflowDraftUpdate): void {
  const listeners = listenersByDraftId.get(update.draftId);
  if (!listeners) return;
  for (const listener of [...listeners]) listener(update);
}
