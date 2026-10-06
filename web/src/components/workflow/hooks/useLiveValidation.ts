/**
 * useLiveValidation — validate the canvas as it is edited, not only on save.
 *
 * While there are unsaved edits, the definition on screen is sent to the
 * server's ValidateWorkflow (which validates exactly what a save would store)
 * once typing pauses. So the header's problem count and every step's markers
 * describe what the user is looking at, before they save.
 *
 * - Debounced: one request per pause, not per keystroke or drag frame.
 * - Positions don't count: moving a step does not revalidate.
 * - Latest wins: a slow response for an older canvas is dropped.
 */
import { useEffect, useRef } from "react";

import { workflowGrpc, type ValidationError } from "../../../api/workflow-grpc";
import type { Workflow } from "../../../types/workflow";
import type { ValidationStatus } from "../workflowFindings";

export const LIVE_VALIDATION_DEBOUNCE_MS = 800;

/** What validation reads: the definition without the canvas layout. */
export function validationKey(workflow: Workflow): string {
  return JSON.stringify({ ...workflow, ui: undefined });
}

export interface UseLiveValidationArgs {
  projectId: string | undefined;
  /** The canvas as a definition (memoized by the builder). */
  workflow: Workflow;
  /** Only unsaved edits need it: loads and saves report their own findings. */
  enabled: boolean;
  setStatus: (status: ValidationStatus) => void;
  setFindings: (findings: ValidationError[]) => void;
  debounceMs?: number;
}

export function useLiveValidation({
  projectId,
  workflow,
  enabled,
  setStatus,
  setFindings,
  debounceMs = LIVE_VALIDATION_DEBOUNCE_MS,
}: UseLiveValidationArgs): void {
  const lastKeyRef = useRef<string | null>(null);
  const requestSeqRef = useRef(0);

  useEffect(() => {
    if (!enabled || !projectId) return;
    const timer = setTimeout(() => {
      const key = validationKey(workflow);
      if (key === lastKeyRef.current) return;
      lastKeyRef.current = key;
      const seq = ++requestSeqRef.current;
      setStatus("validating");
      workflowGrpc
        .validateWorkflow(projectId, workflow)
        .then((result) => {
          if (seq !== requestSeqRef.current) return;
          setFindings(result.errors);
          setStatus(result.valid ? "valid" : "invalid");
        })
        .catch((error) => {
          if (seq !== requestSeqRef.current) return;
          console.error("Live validation failed:", error);
          // Retry the same canvas on the next change rather than never.
          lastKeyRef.current = null;
          setStatus("unknown");
        });
    }, debounceMs);
    return () => clearTimeout(timer);
  }, [enabled, projectId, workflow, setStatus, setFindings, debounceMs]);

  // A load or a save reports findings for the canvas itself; forget the last
  // key so the next edit is validated even if it lands on the same content.
  useEffect(() => {
    if (!enabled) {
      lastKeyRef.current = null;
      requestSeqRef.current += 1;
    }
  }, [enabled]);
}
