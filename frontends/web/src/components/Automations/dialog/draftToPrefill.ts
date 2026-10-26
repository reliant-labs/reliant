// Copyright (c) 2025 Reliant Labs

/**
 * A draft (from "Describe it" or the chat's draft_automation tool) as the
 * dialog's starting values. Only the WHEN and the WHAT come from a draft;
 * where it runs and as whom are left for the dialog to infer and the person
 * to confirm.
 */

import type { AutomationDraft } from "@/hooks/trigger-model";
import type { AutomationPrefill } from "./openAutomationDialog";

export function draftToPrefill(draft: AutomationDraft, opts: { projectId?: string } = {}): AutomationPrefill {
  const prefill: AutomationPrefill = {
    message: draft.message,
    workflow: draft.workflow,
    assumptions: draft.assumptions.length > 0 ? draft.assumptions : undefined,
  };
  if (draft.name) prefill.name = draft.name;
  if (opts.projectId) prefill.projectId = opts.projectId;
  if (draft.source.kind !== "unknown") prefill.source = draft.source;
  if (draft.filter) prefill.filter = draft.filter;
  if (Object.keys(draft.params).length > 0) prefill.params = draft.params;
  if (Object.keys(draft.presets).length > 0) prefill.presets = draft.presets;
  return prefill;
}
