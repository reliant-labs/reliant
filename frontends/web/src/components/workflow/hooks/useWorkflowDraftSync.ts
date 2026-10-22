// Copyright (c) 2025 Reliant Labs

/**
 * Keeps the editor canvas in step with its draft when something else edits it
 * (an agent in any chat, another tab). The backend pushes WORKFLOW_DRAFT_UPDATED
 * on every draft write; this hook turns that into "refetch and apply", unless
 * the push is our own save echoing back or the canvas holds unsaved edits.
 */

import { useCallback, useEffect, useRef } from "react";
import { getWorkflowByDraftId } from "../../../api/workflow-grpc";
import type { DraftStatus } from "../workflowDraftStatus";
import type { Workflow } from "../../../types/workflow";
import { subscribeToDraftUpdates, type WorkflowDraftUpdate } from "../../../store/workflowDraftUpdates";

export interface RemoteWorkflowState {
  workflow: Workflow;
  version: number;
  yamlDefinition?: string;
  status: DraftStatus;
}

interface UseWorkflowDraftSyncArgs {
  projectId?: string;
  draftId?: string;
  /** Version the canvas is based on (the OCC version of the last load or save). */
  version: number;
  hasModifications: boolean;
  /** A save is in flight; its echo must not be mistaken for someone else's edit. */
  isSaving: boolean;
  onRemoteUpdate: (state: RemoteWorkflowState) => void;
  /** The draft moved but the canvas has unsaved edits; `reload` discards them and applies it. */
  onModifiedElsewhere: (reload: () => Promise<void>) => void;
  /** The draft was deleted elsewhere. Unsaved edits are left alone; the caller offers a way out. */
  onDeleted?: () => void;
}

export function useWorkflowDraftSync({
  projectId,
  draftId,
  version,
  hasModifications,
  isSaving,
  onRemoteUpdate,
  onModifiedElsewhere,
  onDeleted,
}: UseWorkflowDraftSyncArgs): void {
  const latest = useRef({ version, hasModifications, isSaving, onRemoteUpdate, onModifiedElsewhere, onDeleted });
  latest.current = { version, hasModifications, isSaving, onRemoteUpdate, onModifiedElsewhere, onDeleted };
  const pendingRef = useRef<WorkflowDraftUpdate | null>(null);

  // Highest version the canvas holds, updated synchronously when a fetch is
  // applied (the `version` prop only catches up on the next render).
  const appliedVersionRef = useRef(version);
  const appliedDraftIdRef = useRef(draftId);
  if (appliedDraftIdRef.current !== draftId) {
    appliedDraftIdRef.current = draftId;
    appliedVersionRef.current = version;
  } else if (version > appliedVersionRef.current) {
    appliedVersionRef.current = version;
  }

  const fetchInFlightRef = useRef(false);
  const refetchRequestedRef = useRef(false);

  const fetchAndApply = useCallback(async (): Promise<void> => {
    if (!projectId || !draftId) return;
    if (fetchInFlightRef.current) {
      refetchRequestedRef.current = true;
      return;
    }
    fetchInFlightRef.current = true;
    try {
      do {
        refetchRequestedRef.current = false;
        const fetched = await getWorkflowByDraftId(projectId, draftId);
        if (!fetched.workflow) continue;
        if (fetched.version <= appliedVersionRef.current) continue;
        appliedVersionRef.current = fetched.version;
        latest.current.onRemoteUpdate({
          workflow: fetched.workflow,
          version: fetched.version,
          yamlDefinition: fetched.yamlDefinition,
          status: fetched.status,
        });
      } while (refetchRequestedRef.current);
    } finally {
      fetchInFlightRef.current = false;
    }
  }, [projectId, draftId]);

  const reloadDiscardingEdits = useCallback(async (): Promise<void> => {
    if (!projectId || !draftId) return;
    const fetched = await getWorkflowByDraftId(projectId, draftId);
    if (!fetched.workflow) return;
    appliedVersionRef.current = fetched.version;
    latest.current.onRemoteUpdate({
      workflow: fetched.workflow,
      version: fetched.version,
      yamlDefinition: fetched.yamlDefinition,
      status: fetched.status,
    });
  }, [projectId, draftId]);

  const handle = useCallback(
    (update: WorkflowDraftUpdate) => {
      const current = latest.current;
      if (update.deleted) {
        current.onDeleted?.();
        return;
      }
      if (update.version <= Math.max(current.version, appliedVersionRef.current)) return;
      if (current.isSaving) {
        pendingRef.current = update;
        return;
      }
      if (current.hasModifications) {
        current.onModifiedElsewhere(reloadDiscardingEdits);
        return;
      }
      fetchAndApply().catch((error) => {
        console.error("Failed to apply workflow draft update:", error);
      });
    },
    [fetchAndApply, reloadDiscardingEdits],
  );

  useEffect(() => {
    if (!draftId) return;
    return subscribeToDraftUpdates(draftId, handle);
  }, [draftId, handle]);

  // A push that arrived mid-save is judged once the save has settled and the
  // canvas version reflects it: our own echo is then not newer and drops out.
  useEffect(() => {
    if (isSaving || !pendingRef.current) return;
    const pending = pendingRef.current;
    pendingRef.current = null;
    handle(pending);
  }, [isSaving, version, handle]);
}
