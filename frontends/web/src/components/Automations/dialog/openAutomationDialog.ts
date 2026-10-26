// Copyright (c) 2025 Reliant Labs

/**
 * The one way to open the automation dialog — create or edit, every trigger
 * kind, from anywhere (a page, a chat card, the builder).
 *
 * There is one dialog and one word for it, "automation" (ui-review report 03,
 * §6.8 option 1). The server keeps two models — an activation of a workflow's
 * DECLARED trigger (EnableWorkflow) and an INLINE trigger row (CreateTrigger /
 * UpdateTrigger) — and the dialog chooses between them from what it is opened
 * with; callers never do.
 *
 *   openAutomationDialog()                                  New automation, blank (Do = the agent)
 *   openAutomationDialog({ workflowRef, projectId })        Run this workflow: When is "set by the
 *                                                           workflow" when it declares triggers
 *   openAutomationDialog({ workflowRef, custom: true })     ...but with its own When (a schedule etc.)
 *   openAutomationDialog({ trigger })                       Edit (an activation edits its workflow's
 *                                                           declared set in that project)
 *   openAutomationDialog({ prefill })                       Starting values (a run, a chat draft)
 *
 * Nothing here creates anything: only the user's Create in the dialog does.
 *
 * The state is a tiny store read by AutomationDialogHost, mounted once in the
 * root shell, so callers need no local `open` state and no import of the
 * dialog itself.
 */

import { create } from "zustand";

import type { Trigger, TriggerSource } from "@/hooks/trigger-model";

/** Declared-mode phase: first turn-on, repair of a NEEDS_SETUP activation, or its settings. */
export type AutomationDialogMode = "setup" | "finish" | "settings";

/**
 * Starting values for a NEW automation. Every field is optional; unset fields
 * take the dialog's inferred defaults. Ignored when editing, where the stored
 * trigger is the source of truth.
 */
export interface AutomationPrefill {
  /** The automation's name. Unset: derived from When + Do. */
  name?: string;
  projectId?: string;
  /** Workflow ref the runs execute. "" or unset: the agent (your default). */
  workflow?: string;
  /** The prompt each run starts from. */
  message?: string;
  /** The When, as an inline source of any kind. Unset: every weekday at 09:00 in the browser's zone. */
  source?: TriggerSource;
  /** For an integration source: the catalog trigger-type ref (its events and connection methods). */
  catalogRef?: string;
  /** CEL filter for a non-schedule inline source. */
  filter?: string;
  presets?: Record<string, string>;
  /** Nested, as the wire carries them. */
  params?: Record<string, unknown>;
  worktreeId?: string;
  /** A daemon id, or NO_MACHINE (daemonChoices.ts). Unset: inferred. */
  machine?: string;
  notifyOnComplete?: boolean;
  /** Assumptions a draft made ("Assumed 9:00 in Pacific Time"), shown for the user to check. */
  assumptions?: string[];
}

export interface OpenAutomationDialogOptions {
  /** Edit this automation (loaded by id when `trigger` is not given). */
  triggerId?: string;
  trigger?: Trigger;
  /**
   * The workflow it runs. When the workflow declares triggers and `custom` is
   * not set, When is set by the workflow and the dialog submits EnableWorkflow.
   */
  workflowRef?: string;
  workflowTitle?: string;
  /** The project it runs in. Unset: the current project. */
  projectId?: string;
  /** Use an inline When even though the workflow declares triggers ("Need a different trigger?"). */
  custom?: boolean;
  /** Declared mode, opened from one declared trigger (the builder's Turn on): name it in the header. */
  declaredTrigger?: string;
  /** Declared mode: force the phase; otherwise it follows what exists in the project. */
  mode?: AutomationDialogMode;
  /** A field to focus once the form is ready: an input name, "machine", "connection", "message". */
  focus?: string;
  /** Offer "Open workflow" on success (opened outside the workflow page). */
  showOpenWorkflow?: boolean;
  prefill?: AutomationPrefill;
  /** Called with what was saved, once it is saved. */
  onSaved?: (triggers: Trigger[]) => void;
}

interface AutomationDialogState {
  /** What the dialog is open with; null when closed. */
  request: OpenAutomationDialogOptions | null;
  /** Bumped on every open, so reopening with equal options remounts a fresh form. */
  openCount: number;
  open: (options: OpenAutomationDialogOptions) => void;
  close: () => void;
}

export const useAutomationDialogStore = create<AutomationDialogState>((set) => ({
  request: null,
  openCount: 0,
  open: (options) => set((s) => ({ request: options, openCount: s.openCount + 1 })),
  close: () => set({ request: null }),
}));

/** Open the automation dialog. Replaces whatever it had open. */
export function openAutomationDialog(options: OpenAutomationDialogOptions = {}): void {
  useAutomationDialogStore.getState().open(options);
}

export function closeAutomationDialog(): void {
  useAutomationDialogStore.getState().close();
}
