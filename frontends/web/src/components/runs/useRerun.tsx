// Copyright (c) 2025 Reliant Labs

/**
 * Re-run and "Fire trigger now", for run detail.
 *
 * Re-run (current definition) opens the Run… dialog prefilled with what the
 * original run was started with — the recorded workflow, presets and inputs,
 * its workspace, its machine, and its prompt — so the user can adjust any of
 * it before starting. It goes through the same dialog and StartChat request
 * as any manual run, so it is ATTENDED (§14.1 decision 11) and opens as a
 * chat. The workflow definition is the current one (decision 3); the
 * button's tooltip and the dialog's title say so. A failed run's Retry is
 * this same action.
 *
 * "Fire trigger now" fires the run's trigger (FireTrigger), which is a
 * different thing: the run it starts is the trigger's, unattended under its
 * settings, and lands in Runs rather than opening.
 *
 * "Run this on a schedule" turns a run that worked into an automation: the
 * automation dialog, prefilled with the same workflow, prompt, inputs,
 * workspace and machine, so the person only picks when (report 03 §6.2).
 */

import { useState, type ReactNode } from "react";
import { toast } from "sonner";

import type { Chat } from "@/api/client";
import { runErrorMessage, type LaunchEvent } from "@/hooks/run-queries";
import { useFirstPrompt } from "@/hooks/run-queries";
import { useFireTrigger } from "@/hooks/trigger-service-hooks_gen";
import { NO_MACHINE } from "../Automations/daemonChoices";
import { openAutomationDialog } from "../Automations/dialog/openAutomationDialog";
import { RunWorkflowDialog } from "../workflow/run/RunWorkflowDialog";

export interface Rerun {
  /** Undefined while the recorded start is unknown: no button. */
  onRerun?: () => void;
  /** Undefined unless the run belongs to a live automation. */
  onRunAutomationNow?: () => void;
  /** Make an automation of this run; undefined while its start is unknown, or when an automation started it. */
  onSchedule?: () => void;
  /** The recorded prompt, once loaded. */
  prompt?: string;
  busy: boolean;
  /** The dialog, to render once anywhere in the page. */
  dialog: ReactNode;
}

export function useRerun(chat: Chat, event: LaunchEvent | null | undefined, triggerName?: string): Rerun {
  const [open, setOpen] = useState(false);
  const start = event?.start;
  // The launch records the prompt it started from; only a run from before that
  // was kept falls back to guessing it from the transcript.
  const recordedPrompt = start?.prompt;
  const guessedPrompt = useFirstPrompt(chat.id, !!start && recordedPrompt === undefined);
  const promptLoading = recordedPrompt === undefined && guessedPrompt.isLoading;
  const promptText = recordedPrompt ?? guessedPrompt.data ?? undefined;
  const fire = useFireTrigger();

  // Every kind of automation can be run now (FireTrigger fires each one), and
  // only a run an automation launched names a trigger.
  const automationId = chat.triggerId || undefined;
  const onRunAutomationNow = automationId
    ? () =>
        fire.mutate({ id: automationId }, {
          onSuccess: () =>
            toast.success(`Fired ${triggerName ?? "the trigger"}`, {
              description: "It runs now, unattended. What it did appears in Runs.",
            }),
          onError: (error) => toast.error(`Couldn't fire ${triggerName ?? "the trigger"}`, { description: runErrorMessage(error) }),
        })
    : undefined;

  const onSchedule =
    start && !promptLoading && !automationId
      ? () =>
          openAutomationDialog({
            workflowRef: start.workflow,
            projectId: chat.projectId,
            custom: true,
            prefill: {
              workflow: start.workflow,
              message: promptText ?? "",
              presets: start.presets,
              params: start.params,
              worktreeId: chat.worktreeId || undefined,
              machine: chat.noMachine ? NO_MACHINE : chat.activeDaemonId || undefined,
            },
          })
      : undefined;

  return {
    // Wait for the prompt too, so the dialog never opens blank and then fills.
    onRerun: start && !promptLoading ? () => setOpen(true) : undefined,
    onRunAutomationNow,
    onSchedule,
    prompt: promptText,
    busy: fire.isPending,
    dialog: start ? (
      <RunWorkflowDialog
        open={open}
        onClose={() => setOpen(false)}
        projectId={chat.projectId}
        workflowRef={start.workflow}
        title="Re-run (current definition)"
        submitLabel="Re-run"
        initialPrompt={promptText ?? ""}
        initialValue={{
          presets: start.presets,
          params: start.params,
          worktreeId: chat.worktreeId || undefined,
          daemonId: chat.activeDaemonId || undefined,
        }}
      />
    ) : null,
  };
}
