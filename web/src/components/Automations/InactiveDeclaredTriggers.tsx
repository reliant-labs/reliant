// Copyright (c) 2025 Reliant Labs

/**
 * Declared-but-inactive triggers, with "Activate" (research/INTEGRATIONS_V1_BRIEF.md
 * §3a; WORKFLOW_EDITOR_UX_REVIEW.md quick win 11). A workflow's `triggers:`
 * say when its author meant it to run, and declaring one fires nothing, so a
 * list of what runs on its own must also show what does not run YET — or
 * "Nothing runs this workflow" reads as true about a workflow that declares a
 * schedule.
 *
 * The same rows on the workflow detail page (one workflow) and the
 * Automations page (every workflow in the library).
 */

import { useState } from "react";
import { CalendarClock, Webhook, Workflow, Zap } from "lucide-react";

import { Button } from "../ui/Button";
import { ActivateTriggerDialog } from "./ActivateTriggerDialog";
import { describeSchedule } from "@/lib/cronText";
import { describeDeclaredSource, integrationOf, sourceCase, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { useIntegrationEventNaming } from "@/hooks/connection-queries";
import type { InactiveDeclaredTrigger } from "@/lib/triggerRail";
import { IntegrationLogoTile } from "../icons/IntegrationLogo";

function SourceIcon({ declared }: { declared: DeclaredTrigger }) {
  const kind = sourceCase(declared);
  if (kind === "integration") return <IntegrationLogoTile icon={integrationOf(declared)?.integration} />;
  const Icon = kind === "schedule" ? CalendarClock : kind === "webhook" ? Webhook : kind === "workflowEvent" ? Workflow : Zap;
  return (
    <span className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border/60 bg-background">
      <Icon className="h-4 w-4 text-muted-foreground" aria-hidden />
    </span>
  );
}

export function InactiveDeclaredTriggers({
  items,
  defaultProjectId,
  showWorkflow = true,
}: {
  items: readonly InactiveDeclaredTrigger[];
  defaultProjectId?: string;
  /** Name the workflow on each row (the cross-workflow list does; a single workflow's page does not). */
  showWorkflow?: boolean;
}) {
  const [activating, setActivating] = useState<InactiveDeclaredTrigger | null>(null);
  const naming = useIntegrationEventNaming();
  if (items.length === 0) return null;
  return (
    <>
      <ul aria-label="Triggers that are not active" className="divide-y divide-border/60" data-testid="inactive-declared-triggers">
        {items.map((item) => {
          const name = item.declared.name ?? "";
          const label = showWorkflow ? `${item.workflowTitle} · ${name}` : name;
          return (
            <li key={`${item.workflowRef}/${name}`} className="flex items-center gap-3 px-4 py-2.5">
              <SourceIcon declared={item.declared} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-foreground">{label}</p>
                <p className="truncate text-xs text-muted-foreground">
                  {describeDeclaredSource(item.declared, describeSchedule, naming)} · Not active
                </p>
              </div>
              <Button size="sm" variant="outline" onClick={() => setActivating(item)} aria-label={`Activate ${label}`}>
                Activate
              </Button>
            </li>
          );
        })}
      </ul>
      {activating && (
        <ActivateTriggerDialog
          open
          onClose={() => setActivating(null)}
          workflowRef={activating.workflowRef}
          workflowTitle={activating.workflowTitle}
          declared={activating.declared}
          defaultProjectId={defaultProjectId}
        />
      )}
    </>
  );
}
