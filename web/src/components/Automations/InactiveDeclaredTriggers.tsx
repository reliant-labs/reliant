// Copyright (c) 2025 Reliant Labs

/**
 * Workflows that declare triggers nothing runs yet, with one "Enable" each.
 * A workflow's `triggers:` say when its author meant it to run, and declaring
 * them fires nothing, so a list of what runs on its own must also show what
 * does not run YET. Enabling is per workflow, not per trigger: one dialog
 * turns on all of a workflow's triggers (WorkflowSetupDialog).
 */

import { useState } from "react";
import { CalendarClock, Webhook, Workflow, Zap } from "lucide-react";

import { Button } from "../ui/Button";
import { WorkflowSetupDialog } from "./WorkflowSetupDialog";
import { describeSchedule } from "@/lib/cronText";
import { describeDeclaredSource, integrationOf, sourceCase, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { useIntegrationEventNaming } from "@/hooks/connection-queries";
import type { InactiveWorkflow } from "@/lib/triggerRail";
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
}: {
  items: readonly InactiveWorkflow[];
  defaultProjectId?: string;
}) {
  const [enabling, setEnabling] = useState<InactiveWorkflow | null>(null);
  const naming = useIntegrationEventNaming();
  if (items.length === 0) return null;
  return (
    <>
      <ul aria-label="Workflows with triggers that are not enabled" className="divide-y divide-border/60" data-testid="inactive-declared-triggers">
        {items.map((item) => {
          const first = item.declared[0]!;
          const summary =
            item.declared.length === 1
              ? describeDeclaredSource(first, describeSchedule, naming)
              : `${item.declared.length} triggers: ${item.declared.map((d) => d.name).join(", ")}`;
          return (
            <li key={item.workflowRef} className="flex items-center gap-3 px-4 py-2.5">
              <SourceIcon declared={first} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-foreground">{item.workflowTitle}</p>
                <p className="truncate text-xs text-muted-foreground">{summary} · Not enabled</p>
              </div>
              <Button size="sm" variant="outline" onClick={() => setEnabling(item)} aria-label={`Enable ${item.workflowTitle}`}>
                Enable
              </Button>
            </li>
          );
        })}
      </ul>
      {enabling && (
        <WorkflowSetupDialog
          open
          onClose={() => setEnabling(null)}
          workflowRef={enabling.workflowRef}
          workflowTitle={enabling.workflowTitle}
          declared={enabling.declared}
          defaultProjectId={defaultProjectId}
        />
      )}
    </>
  );
}
