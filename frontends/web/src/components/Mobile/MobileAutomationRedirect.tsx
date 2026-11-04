/**
 * `/m/automations/$triggerId` — an automation link, on a phone.
 *
 * An automation is a trigger of its workflow, so a link that names only the
 * trigger (an Inbox notification, an old bookmark) is resolved here: the
 * trigger names its workflow, and the screen replaces itself with that
 * workflow's read-only viewer. Triggers are configured on desktop.
 */

import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";

import { isTriggerNotFound } from "@/hooks/trigger-model";
import { useTrigger } from "@/hooks/trigger-queries";

export function MobileAutomationRedirect() {
  const { triggerId } = useParams({ strict: false }) as { triggerId?: string };
  const navigate = useNavigate();
  const trigger = useTrigger(triggerId);

  useEffect(() => {
    if (trigger.data) {
      void navigate({ to: "/m/workflows/$workflowName", params: { workflowName: trigger.data.workflow }, replace: true });
    }
  }, [trigger.data, navigate]);

  if (!triggerId || trigger.isError) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-muted-foreground">
        <p>{isTriggerNotFound(trigger.error) || !triggerId ? "This automation no longer exists." : "This automation could not be loaded."}</p>
        <button
          type="button"
          onClick={() => void navigate({ to: "/m/workflows", replace: true })}
          className="min-h-[44px] rounded-lg border border-border px-4 font-medium text-foreground"
        >
          Show workflows
        </button>
      </div>
    );
  }
  return (
    <div className="flex h-full items-center justify-center text-muted-foreground">
      <Loader2 className="h-5 w-5 animate-spin" aria-label="Opening its workflow" />
    </div>
  );
}
