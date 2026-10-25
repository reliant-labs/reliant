// Copyright (c) 2025 Reliant Labs

/**
 * "Turned on": where a workflow runs by itself, on its own page — the ONE
 * place it is configured (automation-ux-spec.md §2.1).
 *
 *   - One row per project the workflow is on in: health, a one-line summary
 *     of its settings, Settings (or the state's call to action), and a
 *     Pause/Resume switch. Expanding a row shows its triggers, each linking to
 *     that trigger's history.
 *   - "Turn on in another project" in the header; with no rows, the body is
 *     the call to action: "Set up {workflow} to run it by itself…".
 *   - Custom automations that run this workflow (a hand-made schedule or
 *     webhook) are listed under the rows, and the footer offers to add one —
 *     the old "New automation", where it explains itself.
 *
 * A row is `#automation-<projectId>`: Automations and the Inbox link there,
 * and arriving on it expands and scrolls to it.
 */

import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight, Power } from "lucide-react";
import { toast } from "sonner";

import { triggerErrorMessage, type Trigger } from "@/hooks/trigger-model";
import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import { useDisableWorkflow, useSetTriggerEnabled, useTriggers } from "@/hooks/trigger-queries";
import { workflowAutomationsFor, unitLastEvent, type WorkflowAutomation } from "@/lib/automationUnits";
import { automationHealth } from "@/lib/automationHealth";
import { describeSchedule, describeTriggerSource } from "@/lib/cronText";
import { describeDeclaredSource, integrationOf, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { formatRelativeTime } from "@/lib/relativeTime";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { automationAnchorId } from "@/lib/workflowsArea";
import { cn } from "@/lib/utils";
import { useProjects } from "@/hooks/project-queries";
import Card, { CardInset } from "../../forge-ui/card";
import ToggleSwitch from "../../forge-ui/toggle_switch";
import Button from "@/components/forge-ui/button";
import { AutomationCta } from "../../Automations/AutomationCta";
import { HealthIndicator, AutomationRow } from "../../Automations/AutomationRow";
import { automationMachineLabel } from "../../Automations/daemonChoices";
import { WorkflowSetupDialog, type SetupMode } from "../../Automations/WorkflowSetupDialog";

export interface TurnedOnCardProps {
  workflowRef: string;
  workflowTitle: string;
  declared: readonly DeclaredTrigger[];
  /** The page's project: where Set up starts. */
  projectId: string;
  /** Why it cannot be set up (a draft, a definition that failed to load). */
  setupBlockedReason?: string | null;
  onAddCustom: () => void;
}

/** "acme/api, acme/web" from the inputs a trigger is scoped by. */
function scopeSummary(unit: WorkflowAutomation, declared: readonly DeclaredTrigger[]): string | undefined {
  const inputs = new Set(declared.flatMap((d) => Object.values(integrationOf(d)?.matchInput ?? {})));
  const values = [...inputs].flatMap((name) => {
    const value = unit.triggers.find((t) => t.params[name] !== undefined)?.params[name];
    return Array.isArray(value) ? value.map(String) : value ? [String(value)] : [];
  });
  return values.length > 0 ? values.join(", ") : undefined;
}

export function unitSummary(unit: WorkflowAutomation, declared: readonly DeclaredTrigger[]): string {
  const first = unit.triggers[0]!;
  const parts: string[] = [];
  const watching = scopeSummary(unit, declared);
  if (watching) parts.push(`Watching ${watching}`);
  parts.push(automationMachineLabel(first));
  if (declared.some((d) => integrationOf(d))) parts.push(first.actingIdentity === "app" ? "Writes as Reliant bot" : "Writes as you");
  const last = unitLastEvent(unit);
  parts.push(last ? `last fired ${formatRelativeTime(last.occurredAt)}` : "not fired yet");
  return parts.join(" · ");
}

export function TurnedOnCard({ workflowRef, workflowTitle, declared, projectId, setupBlockedReason, onAddCustom }: TurnedOnCardProps) {
  // Every project: the card says where this workflow is on, not only here.
  const triggers = useTriggers();
  const { projects } = useProjects();
  const naming = useIntegrationEventNaming();
  const units = useMemo(() => workflowAutomationsFor(triggers.data ?? [], workflowRef), [triggers.data, workflowRef]);
  const custom = useMemo(
    () => (triggers.data ?? []).filter((t) => !t.workflowTrigger && triggerRunsWorkflow(t, workflowRef)),
    [triggers.data, workflowRef],
  );
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [setup, setSetup] = useState<{ projectId: string; mode?: SetupMode } | null>(null);

  // Arriving on #automation-<projectId> opens that row and brings it into view.
  useEffect(() => {
    if (units.length === 0) return;
    const hash = window.location.hash.replace(/^#/, "");
    const unit = units.find((u) => automationAnchorId(u.projectId) === hash);
    if (!unit) return;
    setExpanded((prev) => (prev[unit.projectId] ? prev : { ...prev, [unit.projectId]: true }));
    requestAnimationFrame(() => document.getElementById(hash)?.scrollIntoView?.({ block: "center" }));
  }, [units]);

  const hasDeclared = declared.length > 0;
  const otherProject = projects.find((p) => !units.some((u) => u.projectId === p.id))?.id ?? projectId;
  const when = declared.map((d) => describeDeclaredSource(d, describeSchedule, naming)).join("; ");

  return (
    <section aria-labelledby="turned-on-heading" data-testid="turned-on-card">
      <Card padding="none" className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 px-4 py-2.5">
          <h2 id="turned-on-heading" className="text-sm font-semibold text-foreground">
            Turned on
          </h2>
          {hasDeclared && units.length > 0 && (
            <Button variant="ghost" size="sm" onClick={() => setSetup({ projectId: otherProject, mode: "setup" })} disabled={!!setupBlockedReason}>
              Turn on in another project
            </Button>
          )}
        </div>

        {triggers.isLoading ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">
            Loading where it runs…
          </p>
        ) : triggers.isError ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">
            Where this workflow runs could not be loaded.
          </p>
        ) : !hasDeclared ? (
          <p className="px-4 py-3 text-sm text-muted-foreground">
            This workflow declares no triggers, so it runs only when someone starts it.
          </p>
        ) : units.length === 0 ? (
          <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-4" data-testid="turned-on-empty">
            <p className="max-w-xl text-sm text-muted-foreground">
              <span className="font-medium text-foreground">Not turned on.</span> Set up {workflowTitle} to run it by itself: {when}.
              {setupBlockedReason && <span className="mt-1 block text-warning-ink">{setupBlockedReason}</span>}
            </p>
            <Button variant="primary" size="sm" onClick={() => setSetup({ projectId })} disabled={!!setupBlockedReason}>
              Set up {workflowTitle}
            </Button>
          </div>
        ) : (
          <ul aria-label="Projects it is turned on in" className="divide-y divide-border/60">
            {units.map((unit) => (
              <UnitRow
                key={unit.key}
                unit={unit}
                declared={declared}
                expanded={!!expanded[unit.projectId]}
                onToggleExpanded={() => setExpanded((prev) => ({ ...prev, [unit.projectId]: !prev[unit.projectId] }))}
                onSettings={() => setSetup({ projectId: unit.projectId })}
                workflowRef={workflowRef}
              />
            ))}
          </ul>
        )}

        {custom.length > 0 && (
          <div className="border-t border-border/60">
            <h3 className="px-4 pb-1 pt-2.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Custom automations</h3>
            <ul aria-label="Custom automations that run this workflow" className="divide-y divide-border/60" data-testid="workflow-detail-automations">
              {custom.map((trigger) => (
                <AutomationRow key={trigger.id} trigger={trigger} />
              ))}
            </ul>
          </div>
        )}

        <div className="border-t border-border/60 bg-background px-4 py-2 text-xs text-muted-foreground">
          {hasDeclared ? "Need it on a different schedule or event? " : "Run it on a schedule, a webhook or an app event: "}
          <button
            type="button"
            onClick={onAddCustom}
            className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Add a custom automation
          </button>
        </div>
      </Card>

      {setup && (
        <WorkflowSetupDialog
          open
          onClose={() => setSetup(null)}
          workflowRef={workflowRef}
          workflowTitle={workflowTitle}
          declared={declared}
          defaultProjectId={setup.projectId}
          mode={setup.mode}
        />
      )}
    </section>
  );
}

function UnitRow({
  unit,
  declared,
  expanded,
  onToggleExpanded,
  onSettings,
  workflowRef,
}: {
  unit: WorkflowAutomation;
  declared: readonly DeclaredTrigger[];
  expanded: boolean;
  onToggleExpanded: () => void;
  onSettings: () => void;
  workflowRef: string;
}) {
  const disable = useDisableWorkflow();
  const setEnabled = useSetTriggerEnabled();
  const naming = useIntegrationEventNaming();
  const project = unit.projectName ?? "Deleted project";
  const panelId = `${automationAnchorId(unit.projectId)}-triggers`;
  const on = unit.enabled || unit.partlyEnabled;
  const attention = unit.health.needsAttention && unit.health.cta;

  const onToggle = (next: boolean) => {
    if (next) {
      for (const t of unit.triggers.filter((t) => !t.enabled)) {
        setEnabled.mutate({ id: t.id, enabled: true }, { onError: (error) => toast.error(`Could not resume ${t.name}`, { description: triggerErrorMessage(error) }) });
      }
      return;
    }
    disable.mutate(
      { workflow: workflowRef, projectId: unit.projectId },
      {
        onSuccess: (result) => {
          if (result.failed.length > 0) {
            toast.error(`${result.failed.length === 1 ? "One trigger" : `${result.failed.length} triggers`} could not be paused`, {
              description: result.failed.map((f) => `${f.name}: ${f.message}`).join("; "),
            });
          }
        },
        onError: (error) => toast.error("Nothing was paused", { description: triggerErrorMessage(error) }),
      },
    );
  };

  return (
    <li id={automationAnchorId(unit.projectId)} className="scroll-mt-24" data-testid={`turned-on-row-${unit.projectId}`} data-health={unit.health.key}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={panelId}
          onClick={onToggleExpanded}
          className="flex min-w-0 flex-1 items-start gap-2 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {expanded ? <ChevronDown className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden /> : <ChevronRight className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />}
          <span className="min-w-0">
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="text-sm font-medium text-foreground">{project}</span>
              <HealthIndicator health={unit.health} />
            </span>
            <span className="mt-0.5 block truncate text-xs text-muted-foreground">{unitSummary(unit, declared)}</span>
          </span>
        </button>
        <div className="flex items-center gap-3">
          {attention ? (
            <AutomationCta cta={unit.health.cta!} triggers={unit.triggers} workflowRef={workflowRef} projectId={unit.projectId} variant="primary" subject={project} />
          ) : (
            <Button variant="outline" size="sm" onClick={onSettings} aria-label={`Settings for ${project}`}>
              Settings
            </Button>
          )}
          <ToggleSwitch checked={on} onChange={onToggle} disabled={disable.isPending || setEnabled.isPending} ariaLabel={`On in ${project}`} size="lg" className="forge-ui" />
        </div>
      </div>
      {expanded && (
        <div id={panelId} className="px-4 pb-3">
          <CardInset padding="none">
            <ul aria-label={`Triggers in ${project}`} className="divide-y divide-border/60">
              {unit.triggers.map((trigger) => (
                <TriggerLine key={trigger.id} trigger={trigger} declared={declared.find((d) => d.name === trigger.workflowTrigger)} naming={naming} />
              ))}
            </ul>
          </CardInset>
        </div>
      )}
    </li>
  );
}

function TriggerLine({
  trigger,
  declared,
  naming,
}: {
  trigger: Trigger;
  declared?: DeclaredTrigger;
  naming: ReturnType<typeof useIntegrationEventNaming>;
}) {
  const health = automationHealth(trigger);
  const what = declared ? describeDeclaredSource(declared, describeSchedule, naming) : describeTriggerSource(trigger.source, naming);
  return (
    <li className={cn("flex flex-wrap items-center justify-between gap-x-4 gap-y-1 px-3 py-2", !trigger.enabled && "text-muted-foreground")}>
      <span className="min-w-0 text-sm">
        <span className="font-mono text-foreground">{trigger.workflowTrigger}</span>
        <span className="text-muted-foreground"> · {what}</span>
      </span>
      <span className="flex items-center gap-3 text-xs text-muted-foreground">
        <HealthIndicator health={health} />
        <span>{trigger.lastEvent ? `fired ${formatRelativeTime(trigger.lastEvent.occurredAt)}` : "not fired yet"}</span>
        {!trigger.enabled && (
          <span className="inline-flex items-center gap-1">
            <Power className="h-3 w-3" aria-hidden /> paused
          </span>
        )}
        <Link
          to="/workflows/automations/$triggerId"
          params={{ triggerId: trigger.id }}
          className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          aria-label={`History of ${trigger.workflowTrigger}`}
        >
          History
        </Link>
      </span>
    </li>
  );
}
