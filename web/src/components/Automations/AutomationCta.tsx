// Copyright (c) 2025 Reliant Labs

/**
 * The one thing to do about an automation's state (automation-ux-spec.md
 * §4.1), as a button, wired. The health vocabulary (lib/automationHealth.ts)
 * names the action; this is the only place that knows what each one does, so
 * the Automations list, the workflow page, the Inbox and the trigger page
 * all act the same way on the same words.
 */

import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";

import { triggerErrorMessage, type Trigger } from "@/api/trigger-grpc";
import { useDeleteTrigger, useSetTriggerEnabled } from "@/hooks/trigger-queries";
import type { AutomationCtaKind, AutomationCtaSpec } from "@/lib/automationHealth";
import { workflowEditHref } from "@/lib/workflowsArea";
import ConfirmationDialog from "../forge-ui/confirmation_dialog";
import { Button } from "../ui/Button";
import { RunNowDialog } from "../workflow/run/RunNowDialog";
import { SetupDialogFor } from "./SetupDialogFor";

export interface AutomationCtaProps {
  cta: AutomationCtaSpec;
  /** The automation's trigger rows: one for a custom automation, its activations for a workflow one. */
  triggers: readonly Trigger[];
  /** The workflow and project the automation runs. */
  workflowRef: string;
  projectId: string;
  size?: "sm" | "md";
  variant?: "primary" | "outline" | "ghost";
  className?: string;
  /** Names the automation in the button's accessible name ("Finish setup for PR review"). */
  subject?: string;
}

/** The newest firing's run across the triggers, for Open run / Open last run. */
function newestRunChat(triggers: readonly Trigger[]): string | undefined {
  let best: Trigger["lastEvent"];
  for (const t of triggers) {
    if (t.lastEvent?.chatId && (!best || Date.parse(t.lastEvent.occurredAt) > Date.parse(best.occurredAt))) best = t.lastEvent;
  }
  return best?.chatId;
}

/** The trigger the state is about: the worst-off one. */
function subjectTrigger(triggers: readonly Trigger[], kind: AutomationCtaKind): Trigger | undefined {
  if (kind === "remove_trigger") return triggers.find((t) => t.health.status === "broken");
  if (kind === "see_why") return triggers.find((t) => t.health.status === "degraded") ?? triggers[0];
  return triggers[0];
}

export function AutomationCta({ cta, triggers, workflowRef, projectId, size = "sm", variant = "outline", className, subject }: AutomationCtaProps) {
  const navigate = useNavigate();
  const setEnabled = useSetTriggerEnabled();
  const remove = useDeleteTrigger();
  const [dialog, setDialog] = useState<"finish" | "settings" | "run" | "remove" | null>(null);
  const label = subject ? `${cta.label} for ${subject}` : undefined;

  const act = () => {
    switch (cta.kind) {
      case "finish_setup":
        setDialog("finish");
        return;
      case "fix_settings":
        setDialog("settings");
        return;
      case "try_now":
      case "run_now":
        setDialog("run");
        return;
      case "remove_trigger":
        setDialog("remove");
        return;
      case "open_workflow":
        void navigate({ href: workflowEditHref(workflowRef) });
        return;
      case "open_last_run":
      case "open_run": {
        const chatId = newestRunChat(triggers);
        if (chatId) void navigate({ to: "/workflows/runs/$runId", params: { runId: chatId } });
        else void navigate({ to: "/workflows/automations/$triggerId", params: { triggerId: triggers[0]!.id } });
        return;
      }
      case "see_why": {
        const t = subjectTrigger(triggers, cta.kind);
        if (t) void navigate({ to: "/workflows/automations/$triggerId", params: { triggerId: t.id } });
        return;
      }
      case "resume":
        for (const t of triggers.filter((t) => !t.enabled)) {
          setEnabled.mutate(
            { id: t.id, enabled: true },
            { onError: (error) => toast.error(`Could not resume ${t.name}`, { description: triggerErrorMessage(error) }) },
          );
        }
        return;
    }
  };

  const broken = cta.kind === "remove_trigger" ? subjectTrigger(triggers, cta.kind) : undefined;

  return (
    <>
      <Button
        type="button"
        size={size}
        variant={variant}
        className={className}
        onClick={act}
        loading={setEnabled.isPending || remove.isPending}
        aria-label={label}
        data-testid={`automation-cta-${cta.kind}`}
      >
        {cta.label}
      </Button>
      {(dialog === "finish" || dialog === "settings") && (
        <SetupDialogFor workflowRef={workflowRef} projectId={projectId} mode={dialog} onClose={() => setDialog(null)} />
      )}
      {dialog === "run" && <RunNowDialog open projectId={projectId} workflowRef={workflowRef} onClose={() => setDialog(null)} />}
      {broken && (
        <ConfirmationDialog
          open={dialog === "remove"}
          title={`Remove ${broken.workflowTrigger ? `“${broken.workflowTrigger}”` : broken.name}?`}
          description="It stops listening for this trigger. The workflow and the chats it already started are kept."
          confirmLabel="Remove trigger"
          loading={remove.isPending}
          onConfirm={() =>
            remove.mutate(broken.id, {
              onSuccess: () => {
                setDialog(null);
                toast.success(`Removed ${broken.name}`);
              },
              onError: (error) => {
                setDialog(null);
                toast.error(`Could not remove ${broken.name}`, { description: triggerErrorMessage(error) });
              },
            })
          }
          onCancel={() => setDialog(null)}
        />
      )}
    </>
  );
}
