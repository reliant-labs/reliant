// Copyright (c) 2025 Reliant Labs

/**
 * Re-run and "Run automation now", for run detail.
 *
 * Re-run (current definition) opens the Run… dialog prefilled with what the
 * original run was started with — the recorded workflow, presets and inputs,
 * its workspace, its machine, and its prompt — so the user can adjust any of
 * it before starting. It goes through the same dialog and StartChat request
 * as any manual run, so it is ATTENDED (§14.1 decision 11) and opens as a
 * chat. The workflow definition is the current one (decision 3); the label
 * says so. A failed run's Retry is this same action.
 *
 * "Run automation now" fires the run's automation (FireTrigger), which is a
 * different thing: the run it starts is the automation's, unattended under
 * its policy, and lands in Runs rather than opening.
 */

import { useState, type ReactNode } from "react";
import { toast } from "sonner";

import type { Chat } from "@/api/client";
import { runErrorMessage, type LaunchEvent } from "@/api/run-grpc";
import { useFirstPrompt } from "@/hooks/run-queries";
import { useFireTrigger } from "@/hooks/trigger-queries";
import { RunWorkflowDialog } from "../workflow/run/RunWorkflowDialog";

export interface Rerun {
  /** Undefined while the recorded start is unknown: no button. */
  onRerun?: () => void;
  /** Undefined unless the run belongs to a live automation. */
  onRunAutomationNow?: () => void;
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

  const automationId = chat.launchKind === "schedule" ? chat.triggerId : undefined;
  const onRunAutomationNow = automationId
    ? () =>
        fire.mutate(automationId, {
          onSuccess: () =>
            toast.success("Automation started", {
              description: `${triggerName ?? "It"} runs now, unattended. The new run appears in Runs.`,
            }),
          onError: (error) => toast.error("Could not run the automation", { description: runErrorMessage(error) }),
        })
    : undefined;

  return {
    // Wait for the prompt too, so the dialog never opens blank and then fills.
    onRerun: start && !promptLoading ? () => setOpen(true) : undefined,
    onRunAutomationNow,
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
