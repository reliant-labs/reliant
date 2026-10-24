// Copyright (c) 2025 Reliant Labs

/**
 * One automation on the Automations overview (automation-ux-spec.md §2.1):
 * a workflow turned on in a project (however many triggers it has) or one
 * custom trigger. Same columns as AutomationRow, so a list mixes them freely:
 * what it is and where, what starts it ("GitHub · 3 triggers"), its health
 * (the worst trigger's), last run, next run, and the switch.
 *
 * A workflow automation links to its workflow page, scrolled to it; a custom
 * one to its own page. NeedsYouRow is the "Needs you" variant: the reason on
 * its own line and the one call to action as a button.
 */

import { Link } from "@tanstack/react-router";
import { AlertOctagon } from "lucide-react";
import { toast } from "sonner";

import { sourceKindLabel, triggerErrorMessage, type Trigger } from "@/api/trigger-grpc";
import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import { useDisableWorkflow, useSetTriggerEnabled } from "@/hooks/trigger-queries";
import { unitLastEvent, unitNextFireAt, type AutomationUnit } from "@/lib/automationUnits";
import { describeTriggerSource } from "@/lib/cronText";
import { RAW_EVENT_NAMING, type IntegrationEventNaming } from "@/lib/integrationEventNames";
import { automationAnchorId } from "@/lib/workflowsArea";
import { cn } from "@/lib/utils";
import { Toggle } from "../ui/Toggle";
import { AUTOMATION_ROW_GRID, HealthIndicator, LastRun, NextFire, sourceIcon } from "./AutomationRow";
import { AutomationCta } from "./AutomationCta";
import { automationMachineLabel } from "./daemonChoices";
import { unitProject, unitTitle, unitWorkflow } from "./automationGrouping";
import { integrationDisplayName } from "../workflow/inputs/inputTypeNouns";

/** "GitHub · 3 triggers", "Weekdays at 09:00", "2 triggers". */
export function unitStartsOn(unit: AutomationUnit, naming: IntegrationEventNaming = RAW_EVENT_NAMING): string {
  if (unit.kind === "custom") return describeTriggerSource(unit.trigger.source, naming);
  const rows = unit.triggers;
  if (rows.length === 1) return describeTriggerSource(rows[0]!.source, naming);
  const integrations = new Set(
    rows.map((t) => {
      const source = t.source.kind === "activation" ? t.source.declared : t.source;
      return source?.kind === "passthrough" && source.arm.case === "integration" ? source.arm.value.integration : "";
    }),
  );
  const [only] = [...integrations];
  // A naming that only echoes the id back (the catalog not loaded yet) loses to
  // the built-in display name: "GitHub", not "github".
  const named = only ? naming.integration(only) : "";
  const display = named && named !== only ? named : only ? integrationDisplayName(only) : "";
  const prefix = integrations.size === 1 && display ? `${display} · ` : "";
  return `${prefix}${rows.length} triggers`;
}

function unitTriggers(unit: AutomationUnit): Trigger[] {
  return unit.kind === "workflow" ? unit.triggers : [unit.trigger];
}

function UnitLink({ unit, className }: { unit: AutomationUnit; className?: string }) {
  const title = unitTitle(unit);
  if (unit.kind === "custom") {
    return (
      <Link to="/workflows/automations/$triggerId" params={{ triggerId: unit.trigger.id }} className={className}>
        {title}
      </Link>
    );
  }
  return (
    <Link
      to="/workflows/library/$workflowRef"
      params={{ workflowRef: unit.workflowRef }}
      search={{ project: unit.projectId }}
      hash={automationAnchorId(unit.projectId)}
      className={className}
    >
      {title}
    </Link>
  );
}

function KindTile({ unit }: { unit: AutomationUnit }) {
  const first = unitTriggers(unit)[0]!;
  const Icon = unit.health.key === "broken" ? AlertOctagon : sourceIcon(first.source);
  return (
    <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border/60 bg-background text-muted-foreground">
      <Icon className="h-4 w-4" aria-hidden="true" />
      <span className="sr-only">{sourceKindLabel(first.source)}</span>
    </span>
  );
}

function useUnitToggle(unit: AutomationUnit) {
  const setEnabled = useSetTriggerEnabled();
  const disable = useDisableWorkflow();
  const title = unitTitle(unit);
  const toggle = (next: boolean) => {
    if (unit.kind === "workflow" && !next) {
      disable.mutate(
        { workflow: unit.workflowRef, projectId: unit.projectId },
        { onError: (error) => toast.error(`Could not pause ${title}`, { description: triggerErrorMessage(error) }) },
      );
      return;
    }
    for (const t of unitTriggers(unit).filter((t) => t.enabled !== next)) {
      setEnabled.mutate(
        { id: t.id, enabled: next },
        { onError: (error) => toast.error(`Could not ${next ? "resume" : "pause"} ${title}`, { description: triggerErrorMessage(error) }) },
      );
    }
  };
  return { toggle, pending: setEnabled.isPending || disable.isPending };
}

export function AutomationUnitRow({ unit }: { unit: AutomationUnit }) {
  const naming = useIntegrationEventNaming();
  const { toggle, pending } = useUnitToggle(unit);
  const project = unitProject(unit);
  const first = unitTriggers(unit)[0]!;
  const on = unit.enabled || (unit.kind === "workflow" && unit.partlyEnabled);
  const startsOn = unitStartsOn(unit, naming);
  const nextFireAt = unitNextFireAt(unit);
  const title = unitTitle(unit);

  return (
    <li
      className={cn(AUTOMATION_ROW_GRID, "py-2.5", !on && "text-muted-foreground")}
      data-testid={`automation-unit-${unit.key}`}
      data-health={unit.health.key}
    >
      <KindTile unit={unit} />
      <div className="min-w-0">
        <UnitLink
          unit={unit}
          className={cn(
            "block truncate rounded-sm text-sm font-medium hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
            on ? "text-foreground" : "text-muted-foreground",
          )}
        />
        <p className="mt-0.5 truncate text-xs text-muted-foreground">
          {project.name} · {automationMachineLabel(first)}
        </p>
      </div>
      <div className="order-last col-span-3 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 lg:contents">
        <p className="min-w-0 truncate text-xs text-muted-foreground" title={startsOn}>
          <span className="sr-only">Starts on: </span>
          {startsOn}
        </p>
        <div className="min-w-0">
          <span className="sr-only">Health: </span>
          <HealthIndicator health={unit.health} />
        </div>
        <div className="flex min-w-0 items-center gap-2">
          <span className="sr-only">Last run: </span>
          <LastRun event={unitLastEvent(unit)} />
        </div>
        <div className="min-w-0 truncate text-xs text-muted-foreground">
          <NextFire trigger={{ enabled: on, nextFireAt }} />
        </div>
      </div>
      <div className="flex items-center justify-end">
        <Toggle checked={on} onChange={toggle} disabled={pending} srLabel={`${title} in ${project.name} on`} />
      </div>
    </li>
  );
}

/** A "Needs you" row: what, where, the state and why, and the one thing to do. */
export function NeedsYouRow({ unit }: { unit: AutomationUnit }) {
  const project = unitProject(unit);
  const title = unitTitle(unit);
  return (
    <li
      className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:gap-4"
      data-testid={`needs-you-${unit.key}`}
      data-health={unit.health.key}
    >
      <div className="flex min-w-0 flex-1 items-start gap-3">
        <KindTile unit={unit} />
        <div className="min-w-0">
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <UnitLink
              unit={unit}
              className="truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            />
            <span className="text-xs text-muted-foreground">{project.name}</span>
            <HealthIndicator health={{ ...unit.health, detail: undefined }} />
          </p>
          {unit.health.detail && <p className="mt-0.5 text-xs text-muted-foreground">{unit.health.detail}</p>}
        </div>
      </div>
      {unit.health.cta && (
        <AutomationCta
          cta={unit.health.cta}
          triggers={unitTriggers(unit)}
          workflowRef={unitWorkflow(unit)}
          projectId={project.id}
          variant="primary"
          subject={`${title} in ${project.name}`}
          className="max-sm:w-full"
        />
      )}
    </li>
  );
}
