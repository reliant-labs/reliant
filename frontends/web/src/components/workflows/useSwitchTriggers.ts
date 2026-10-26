// Copyright (c) 2025 Reliant Labs

/**
 * Turning a set of triggers On or Off — a workflow's row on the list, one
 * project's triggers on its page, one trigger — with one vocabulary and one
 * set of messages, wherever the switch is.
 *
 * Off: a workflow's declared triggers in a project are turned off together
 * (DisableWorkflow, which settles them as one); a hand-made trigger on its
 * own. On: every trigger in the set that is off is turned back on.
 */

import { toast } from "sonner";

import { triggerErrorMessage, type Trigger } from "@/hooks/trigger-model";
import { useDisableWorkflow, useSetTriggerEnabled } from "@/hooks/trigger-queries";

export function useSwitchTriggers() {
  const disable = useDisableWorkflow();
  const setEnabled = useSetTriggerEnabled();

  const switchTriggers = (triggers: readonly Trigger[], next: boolean, subject: string) => {
    if (next) {
      for (const trigger of triggers.filter((t) => !t.enabled)) {
        setEnabled.mutate(
          { id: trigger.id, enabled: true },
          { onError: (error) => toast.error(`Couldn't turn on ${trigger.name}`, { description: triggerErrorMessage(error) }) },
        );
      }
      return;
    }
    const declared = new Map<string, Trigger>();
    for (const trigger of triggers) {
      if (!trigger.enabled) continue;
      if (trigger.workflowTrigger) {
        declared.set(`${trigger.workflow}::${trigger.projectId}`, trigger);
        continue;
      }
      setEnabled.mutate(
        { id: trigger.id, enabled: false },
        { onError: (error) => toast.error(`Couldn't turn off ${trigger.name}`, { description: triggerErrorMessage(error) }) },
      );
    }
    for (const trigger of declared.values()) {
      disable.mutate(
        { workflow: trigger.workflow, projectId: trigger.projectId },
        {
          onSuccess: (result) => {
            if (result.failed.length > 0) {
              toast.error(`${result.failed.length === 1 ? "One trigger" : `${result.failed.length} triggers`} of ${subject} couldn't be turned off`, {
                description: result.failed.map((f) => `${f.name}: ${f.message}`).join("; "),
              });
            }
          },
          onError: (error) => toast.error(`Couldn't turn off ${subject}`, { description: triggerErrorMessage(error) }),
        },
      );
    }
  };

  return { switchTriggers, isPending: disable.isPending || setEnabled.isPending };
}
