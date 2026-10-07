// Copyright (c) 2025 Reliant Labs

/**
 * A BROKEN activation: the trigger activates a workflow-declared trigger that
 * can no longer be used — the workflow is gone, it no longer declares that
 * name, or the declaration changed kind or integration. Its firings fail
 * until the declaration is restored or the activation is removed, so the
 * notice offers exactly those two fixes, beside the server's reason.
 */

import { AlertOctagon, Pencil, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import ConfirmationDialog from "../forge-ui/confirmation_dialog";
import { triggerErrorMessage, type Trigger } from "@/api/trigger-grpc";
import { useDeleteTrigger } from "@/hooks/trigger-queries";
import { normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import { cn } from "@/lib/utils";

/** The builder URL for a trigger's workflow; builtin workflows open read-only there. */
export function workflowEditHref(workflow: string): string {
  return `/workflow/${encodeURIComponent(normalizeWorkflowRef(workflow))}`;
}

export interface BrokenActivationNoticeProps {
  trigger: Trigger;
  /** "Edit workflow" target; defaults to the trigger's workflow in the builder. Omit with `hideEdit`. */
  editHref?: string;
  /** Already in that workflow's builder: offer no link to it. */
  hideEdit?: boolean;
  onRemoved?: () => void;
  className?: string;
  compact?: boolean;
}

export function BrokenActivationNotice({ trigger, editHref, hideEdit = false, onRemoved, className, compact = false }: BrokenActivationNoticeProps) {
  const remove = useDeleteTrigger();
  const [confirming, setConfirming] = useState(false);
  const reason = trigger.health.lastFailureDetail || "Its workflow's trigger is missing or changed.";
  const declared = trigger.workflowTrigger ? `“${trigger.workflowTrigger}”` : "its trigger";

  const onRemove = () => {
    remove.mutate(trigger.id, {
      onSuccess: () => {
        setConfirming(false);
        toast.success(`Removed ${trigger.name}`);
        onRemoved?.();
      },
      onError: (error) => {
        setConfirming(false);
        toast.error(`Could not remove ${trigger.name}`, { description: triggerErrorMessage(error) });
      },
    });
  };

  return (
    <div
      role="alert"
      className={cn(
        "rounded-lg border border-danger-border bg-danger-surface text-sm",
        compact ? "px-2.5 py-2" : "px-4 py-3",
        className,
      )}
    >
      <div className="flex items-start gap-2">
        <AlertOctagon className="mt-0.5 h-4 w-4 flex-shrink-0 text-danger-ink" aria-hidden />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="font-medium text-danger-ink">
            {compact ? "Broken" : `This automation can't run: ${declared} no longer works.`}
          </p>
          <p className={cn("text-foreground", compact && "text-xs")}>{reason}</p>
          <div className="flex flex-wrap items-center gap-3 pt-1">
            {!hideEdit && trigger.workflow && (
              <a
                href={editHref ?? workflowEditHref(trigger.workflow)}
                className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <Pencil className="h-3.5 w-3.5" aria-hidden /> Edit workflow
              </a>
            )}
            <button
              type="button"
              onClick={() => setConfirming(true)}
              className="inline-flex items-center gap-1 text-xs font-medium text-danger-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Trash2 className="h-3.5 w-3.5" aria-hidden /> Remove activation
            </button>
          </div>
        </div>
      </div>
      <ConfirmationDialog
        open={confirming}
        title={`Remove ${trigger.name}?`}
        description="It stops listening for this trigger. The workflow and the chats it already started are kept."
        confirmLabel="Remove activation"
        loading={remove.isPending}
        onConfirm={onRemove}
        onCancel={() => setConfirming(false)}
      />
    </div>
  );
}
