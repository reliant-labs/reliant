// Copyright (c) 2025 Reliant Labs

/**
 * "Triggers": what makes a workflow run by itself, on its own page — the ONE
 * place it is configured. An automation is not a separate object: it is
 * these triggers.
 *
 *   - Every trigger is listed the same way whether it is declared in the
 *     workflow file (and turned on in a project) or was added here by hand;
 *     a badge says which. They are grouped by project, since a workflow's
 *     declared triggers in one project share their settings and switch
 *     together.
 *   - A project row: the group's health, Settings (or the state's call to
 *     action), and an On/Off switch for all of its triggers.
 *   - A trigger row: what fires it, its health, its last trigger event and
 *     next run, and its own switch. Expanding it shows its definition, Fire
 *     trigger now, Edit or Delete, and its history — its recent runs AND the
 *     trigger events that started none (Skipped, Couldn't start), from the
 *     same run log the Runs tab reads.
 *   - Declared triggers not yet set up in the project in scope are listed
 *     too, Off, with Set up….
 *   - One "Add trigger…" in the header, for a schedule, webhook or app event
 *     of the user's own. Every create and edit goes through
 *     openAutomationDialog.
 *
 * Scope is the area's: one project, or every project. A trigger row is
 * `#trigger-<id>` and a project group `#automation-<projectId>`; every link to
 * an automation lands on one (automationHref), and arriving on it expands it
 * and scrolls it into view.
 */

import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Plus, Zap } from "lucide-react";
import { toast } from "sonner";

import { triggerErrorMessage, triggerSchedule, type Trigger } from "@/hooks/trigger-model";
import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import { useDeleteTrigger, useTrigger, useTriggers } from "@/hooks/trigger-queries";
import { useFireTrigger, triggerServiceKeys } from "@/hooks/trigger-service-hooks_gen";
import { runServiceKeys } from "@/hooks/run-service-hooks_gen";
import { useRunList } from "@/hooks/run-queries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useProjects } from "@/hooks/project-queries";
import { useProjectStore } from "@/store/projectStore";
import { automationHealth } from "@/lib/automationHealth";
import { automationUnits, type AutomationUnit } from "@/lib/automationUnits";
import { describeSchedule, describeTriggerSource } from "@/lib/cronText";
import { describeDeclaredSource, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { automationAnchorId, triggerAnchorId } from "@/lib/workflowsArea";
import { cn } from "@/lib/utils";
import Card, { CardInset } from "../../forge-ui/card";
import ConfirmationDialog from "../../forge-ui/confirmation_dialog";
import ToggleSwitch from "../../forge-ui/toggle_switch";
import Button from "@/components/forge-ui/button";
import { AutomationCta } from "../../Automations/AutomationCta";
import { automationMachineLabel, daemonStatusLabel } from "../../Automations/daemonChoices";
import { openAutomationDialog } from "../../Automations/dialog/openAutomationDialog";
import { RunList } from "../../runs/RunList";
import { HealthIndicator, LastTriggerEvent, NextFire } from "../automationStatus";
import { WorkflowBadge } from "../WorkflowSourceBadge";
import { useSwitchTriggers } from "../useSwitchTriggers";

export interface TriggersCardProps {
  workflowRef: string;
  workflowTitle: string;
  declared: readonly DeclaredTrigger[];
  /** The current project: where Add trigger… and Set up… start. */
  projectId: string;
  /** The project in scope; undefined when it is every project. */
  scopeProjectId?: string;
  /** Why it cannot be turned on (a draft, a definition that failed to load). */
  setupBlockedReason?: string | null;
  /** Widens the area's scope to every project. */
  onShowAllProjects?: () => void;
}

/** How many runs and events a trigger's history shows; "View all" opens Runs. */
const TRIGGER_HISTORY_LIMIT = 5;

/** The trigger's own name: the declared one, or the name it was given. */
export function triggerLabel(trigger: Trigger): string {
  return trigger.workflowTrigger || trigger.name;
}

interface ProjectGroup {
  projectId: string;
  projectName: string;
  triggers: Trigger[];
  units: AutomationUnit[];
}

export function TriggersCard({
  workflowRef,
  workflowTitle,
  declared,
  projectId,
  scopeProjectId,
  setupBlockedReason,
  onShowAllProjects,
}: TriggersCardProps) {
  // Every project's, then narrowed: the card also says where else it is on.
  const triggers = useTriggers();
  const { projects } = useProjects();
  const naming = useIntegrationEventNaming();
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const mine = useMemo(() => (triggers.data ?? []).filter((t) => triggerRunsWorkflow(t, workflowRef)), [triggers.data, workflowRef]);
  const inScope = useMemo(() => mine.filter((t) => !scopeProjectId || t.projectId === scopeProjectId), [mine, scopeProjectId]);
  const elsewhere = new Set(mine.filter((t) => t.enabled && !inScope.includes(t)).map((t) => t.projectId)).size;

  const groups = useMemo<ProjectGroup[]>(() => {
    const byProject = new Map<string, Trigger[]>();
    for (const t of inScope) byProject.set(t.projectId, [...(byProject.get(t.projectId) ?? []), t]);
    return [...byProject.entries()]
      .map(([id, rows]) => ({
        projectId: id,
        projectName: rows.find((t) => t.projectName)?.projectName ?? projects.find((p) => p.id === id)?.name ?? "Deleted project",
        triggers: [...rows].sort((a, b) => Number(!a.workflowTrigger) - Number(!b.workflowTrigger) || triggerLabel(a).localeCompare(triggerLabel(b))),
        units: automationUnits(rows),
      }))
      .sort((a, b) => Number(b.projectId === projectId) - Number(a.projectId === projectId) || a.projectName.localeCompare(b.projectName));
  }, [inScope, projects, projectId]);

  // Declared triggers the current project has not set up (shown when it is in scope).
  const currentInScope = !scopeProjectId || scopeProjectId === projectId;
  const setUpHere = new Set(mine.filter((t) => t.projectId === projectId && t.workflowTrigger).map((t) => t.workflowTrigger));
  const notSetUp = currentInScope ? declared.filter((d) => !setUpHere.has(d.name)) : [];
  const storeProject = useProjectStore((state) => state.currentProject);
  const currentProjectName =
    projects.find((p) => p.id === projectId)?.name ?? (storeProject?.id === projectId ? storeProject.name : undefined) ?? "this project";

  // Arriving on #trigger-<id> or #automation-<projectId> opens it and brings it into view.
  useEffect(() => {
    if (inScope.length === 0) return;
    const hash = window.location.hash.replace(/^#/, "");
    const trigger = inScope.find((t) => triggerAnchorId(t.id) === hash);
    if (trigger) setExpanded((prev) => (prev[trigger.id] ? prev : { ...prev, [trigger.id]: true }));
    if (!trigger && !inScope.some((t) => automationAnchorId(t.projectId) === hash)) return;
    requestAnimationFrame(() => document.getElementById(hash)?.scrollIntoView?.({ block: "center" }));
  }, [inScope]);

  const addTrigger = () =>
    openAutomationDialog({
      workflowRef,
      projectId,
      // While the declared ones are not all set up here, the dialog starts from them.
      custom: declared.length === 0 || notSetUp.length === 0,
    });

  return (
    <section aria-labelledby="triggers-heading" data-testid="triggers-card">
      <Card padding="none" className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 px-4 py-2.5">
          <h2 id="triggers-heading" className="text-sm font-semibold text-foreground">
            Triggers
          </h2>
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Plus className="h-4 w-4" />}
            onClick={addTrigger}
            disabled={!!setupBlockedReason}
            title={setupBlockedReason ?? undefined}
          >
            Add trigger…
          </Button>
        </div>

        {triggers.isLoading ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">
            Loading its triggers…
          </p>
        ) : triggers.isError ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">
            Its triggers could not be loaded.
          </p>
        ) : groups.length === 0 && notSetUp.length === 0 ? (
          <div className="px-4 py-4" data-testid="triggers-empty">
            <p className="max-w-xl text-sm text-muted-foreground">
              <span className="font-medium text-foreground">Off.</span> {workflowTitle} runs only when someone starts it. Add a
              trigger to run it on a schedule, a webhook or an app event.
            </p>
            {setupBlockedReason && <p className="mt-1 text-sm text-warning-ink">{setupBlockedReason}</p>}
          </div>
        ) : (
          <ul aria-label="Triggers by project" className="divide-y divide-border/60">
            {groups.map((group) => (
              <ProjectGroupRow
                key={group.projectId}
                group={group}
                workflowRef={workflowRef}
                workflowTitle={workflowTitle}
                declared={declared}
                naming={naming}
                expanded={expanded}
                onToggleExpanded={(id) => setExpanded((prev) => ({ ...prev, [id]: !prev[id] }))}
              />
            ))}
            {notSetUp.length > 0 && (
              <li className="px-4 py-3" data-testid="triggers-not-set-up">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <p className="min-w-0 text-sm text-muted-foreground">
                    <span className="font-medium text-foreground">
                      {groups.some((g) => g.projectId === projectId) ? "Also declared, off" : `Off in ${currentProjectName}`}
                    </span>
                    {": "}
                    {notSetUp.map((d) => describeDeclaredSource(d, describeSchedule, naming)).join("; ")}
                    {setupBlockedReason && <span className="mt-1 block text-warning-ink">{setupBlockedReason}</span>}
                  </p>
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => openAutomationDialog({ workflowRef, projectId })}
                    disabled={!!setupBlockedReason}
                  >
                    Set up…
                  </Button>
                </div>
                <ul aria-label="Declared triggers that are off" className="mt-2 flex flex-wrap gap-1.5">
                  {notSetUp.map((d) => (
                    <li key={d.name} className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                      <span className="font-mono text-foreground/80">{d.name}</span>
                      <WorkflowBadge label="From the workflow file" variant="neutral" />
                    </li>
                  ))}
                </ul>
              </li>
            )}
          </ul>
        )}

        {scopeProjectId && elsewhere > 0 && onShowAllProjects && (
          <div className="border-t border-border/60 bg-background px-4 py-2 text-xs text-muted-foreground">
            Also on in {elsewhere === 1 ? "1 other project" : `${elsewhere} other projects`}.{" "}
            <button
              type="button"
              onClick={onShowAllProjects}
              className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              Show all projects
            </button>
          </div>
        )}
      </Card>
    </section>
  );
}

function ProjectGroupRow({
  group,
  workflowRef,
  workflowTitle,
  declared,
  naming,
  expanded,
  onToggleExpanded,
}: {
  group: ProjectGroup;
  workflowRef: string;
  workflowTitle: string;
  declared: readonly DeclaredTrigger[];
  naming: ReturnType<typeof useIntegrationEventNaming>;
  expanded: Record<string, boolean>;
  onToggleExpanded: (triggerId: string) => void;
}) {
  const { switchTriggers, isPending } = useSwitchTriggers();
  const on = group.triggers.some((t) => t.enabled);
  const worst = [...group.units].sort((a, b) => a.health.severity - b.health.severity)[0]!;
  const attention = worst.health.needsAttention && worst.health.cta ? worst : undefined;
  const declaredOnes = group.triggers.filter((t) => t.workflowTrigger);
  const subject = `${workflowTitle} in ${group.projectName}`;

  return (
    <li id={automationAnchorId(group.projectId)} className="scroll-mt-24" data-testid={`triggers-project-${group.projectId}`} data-health={worst.health.key}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
          <span className="text-sm font-medium text-foreground">{group.projectName}</span>
          <HealthIndicator health={worst.health} />
        </div>
        <div className="flex items-center gap-3">
          {attention ? (
            <AutomationCta
              cta={attention.health.cta!}
              triggers={attention.kind === "workflow" ? attention.triggers : [attention.trigger]}
              workflowRef={workflowRef}
              projectId={group.projectId}
              variant="primary"
              subject={group.projectName}
            />
          ) : (
            declaredOnes.length > 0 && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => openAutomationDialog({ triggerId: declaredOnes[0]!.id })}
                aria-label={`Settings for ${group.projectName}`}
              >
                Settings
              </Button>
            )
          )}
          <ToggleSwitch
            checked={on}
            onChange={(next) => switchTriggers(group.triggers, next, subject, true)}
            disabled={isPending}
            ariaLabel={`On in ${group.projectName}`}
            size="lg"
            className="forge-ui"
          />
        </div>
      </div>
      <div className="px-4 pb-3">
        <CardInset padding="none">
          <ul aria-label={`Triggers in ${group.projectName}`} className="divide-y divide-border/60">
            {group.triggers.map((trigger) => (
              <TriggerRow
                key={trigger.id}
                trigger={trigger}
                declared={declared.find((d) => d.name === trigger.workflowTrigger)}
                naming={naming}
                expanded={!!expanded[trigger.id]}
                onToggleExpanded={() => onToggleExpanded(trigger.id)}
              />
            ))}
          </ul>
        </CardInset>
      </div>
    </li>
  );
}

function TriggerRow({
  trigger,
  declared,
  naming,
  expanded,
  onToggleExpanded,
}: {
  trigger: Trigger;
  declared?: DeclaredTrigger;
  naming: ReturnType<typeof useIntegrationEventNaming>;
  expanded: boolean;
  onToggleExpanded: () => void;
}) {
  const { switchTriggers, isPending } = useSwitchTriggers();
  const health = automationHealth(trigger);
  const what = declared ? describeDeclaredSource(declared, describeSchedule, naming) : describeTriggerSource(trigger.source, naming);
  const label = triggerLabel(trigger);
  const panelId = `${triggerAnchorId(trigger.id)}-detail`;

  return (
    <li id={triggerAnchorId(trigger.id)} className="scroll-mt-24" data-testid={`trigger-row-${trigger.id}`}>
      <div className={cn("flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2", !trigger.enabled && "text-muted-foreground")}>
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={panelId}
          onClick={onToggleExpanded}
          className="flex min-w-0 flex-1 items-start gap-2 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {expanded ? (
            <ChevronDown className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          ) : (
            <ChevronRight className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          )}
          <span className="min-w-0 text-sm">
            <span className={cn("text-foreground", trigger.workflowTrigger && "font-mono")}>{label}</span>
            <span className="text-muted-foreground"> · {what}</span>
          </span>
        </button>
        <span className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <WorkflowBadge label={trigger.workflowTrigger ? "From the workflow file" : "Added here"} variant="neutral" />
          <HealthIndicator health={health} />
          <span className="inline-flex items-center gap-1.5" data-testid="trigger-last-event">
            <LastTriggerEvent event={trigger.lastEvent} />
          </span>
          <span data-testid="trigger-next-run">
            <span aria-hidden="true">Next </span>
            <NextFire trigger={trigger} />
          </span>
          <ToggleSwitch
            checked={trigger.enabled}
            onChange={(next) => switchTriggers([trigger], next, label)}
            disabled={isPending}
            ariaLabel={`${label} on`}
            size="sm"
            className="forge-ui"
          />
        </span>
      </div>
      {health.key === "broken" && (
        // Broken is the one state that needs no click to explain: every
        // firing is refused until it is removed or its workflow is fixed. The
        // switch beside it pauses it.
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 pb-2 pl-9">
          <p role="alert" className="min-w-0 flex-1 text-xs text-danger-ink" data-testid="trigger-broken-reason">
            {health.detail}
          </p>
          {health.cta && (
            <AutomationCta cta={health.cta} triggers={[trigger]} workflowRef={trigger.workflow} projectId={trigger.projectId} subject={label} />
          )}
        </div>
      )}
      {expanded && (
        <div id={panelId} className="space-y-3 border-t border-border/60 px-3 py-3">
          <TriggerActions trigger={trigger} />
          <TriggerDefinition trigger={trigger} />
          <TriggerHistory trigger={trigger} />
          <TriggerConfigurationHistory triggerId={trigger.id} />
        </div>
      )}
    </li>
  );
}

/** How long after Fire trigger now the history is re-read, while the new event or run lands. */
const FIRE_REFRESH_MS = [2_000, 5_000, 10_000, 20_000];

/**
 * Fire trigger now — fires the trigger by hand: unattended, under its own
 * settings, recorded as a trigger event. (Run… on the page header is the
 * attended start, a different thing.) Then Edit — a hand-made trigger's own
 * form; a declared one's settings are its project's — and Delete, or Remove
 * for a declared one that is broken.
 */
function TriggerActions({ trigger }: { trigger: Trigger }) {
  const queryClient = useQueryClient();
  const fire = useFireTrigger();
  const deleteTrigger = useDeleteTrigger();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const label = triggerLabel(trigger);
  const declared = !!trigger.workflowTrigger;
  const canRemove = !declared || automationHealth(trigger).key === "broken";

  const onFire = () =>
    fire.mutate(
      { id: trigger.id },
      {
        onSuccess: () => {
          toast.success(`Fired ${label}`, { description: "What it did appears in its history in a moment." });
          for (const delay of FIRE_REFRESH_MS) {
            window.setTimeout(() => {
              void queryClient.invalidateQueries({ queryKey: runServiceKeys.all });
              void queryClient.invalidateQueries({ queryKey: triggerServiceKeys.all });
            }, delay);
          }
        },
        onError: (error) => toast.error(`Couldn't fire ${label}`, { description: triggerErrorMessage(error) }),
      },
    );

  const onDelete = () =>
    deleteTrigger.mutate(
      { id: trigger.id },
      {
        onSuccess: () => {
          setConfirmingDelete(false);
          toast.success(declared ? `Removed ${label}` : `Deleted ${label}`);
        },
        onError: (error) => {
          setConfirmingDelete(false);
          toast.error(`Couldn't ${declared ? "remove" : "delete"} ${label}`, { description: triggerErrorMessage(error) });
        },
      },
    );

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button variant="outline" size="sm" leftIcon={<Zap className="h-4 w-4" />} onClick={onFire} loading={fire.isPending}>
        Fire trigger now
      </Button>
      <Button variant="ghost" size="sm" onClick={() => openAutomationDialog({ triggerId: trigger.id })}>
        {declared ? "Settings" : "Edit"}
      </Button>
      {canRemove && (
        <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(true)}>
          {declared ? "Remove trigger" : "Delete"}
        </Button>
      )}
      <ConfirmationDialog
        open={confirmingDelete}
        title={declared ? `Remove ${label}?` : `Delete ${label}?`}
        description="It will stop running. Runs it already started are kept."
        confirmLabel={declared ? "Remove trigger" : "Delete trigger"}
        loading={deleteTrigger.isPending}
        onConfirm={onDelete}
        onCancel={() => setConfirmingDelete(false)}
      />
    </div>
  );
}

function TriggerDefinition({ trigger }: { trigger: Trigger }) {
  const schedule = triggerSchedule(trigger);
  const naming = useIntegrationEventNaming();
  const { daemons } = useDaemonStatus();
  const daemon = daemons.find((d) => d.daemonId === trigger.daemonId);
  const inputCount = Object.keys(trigger.presets).length + Object.keys(trigger.params).length;
  const rows: Array<{ label: string; value: React.ReactNode }> = [
    {
      label: "Runs on",
      value: (
        <span>
          {automationMachineLabel(trigger, daemon)}
          <span className="text-muted-foreground">
            {" "}
            ({trigger.noMachine ? "web & integrations only" : daemon ? daemonStatusLabel(daemon.status) : "not in your machine list"})
          </span>
        </span>
      ),
    },
    {
      label: schedule ? "Schedule" : "Fires on",
      value: schedule ? (
        <span>
          {describeSchedule(schedule)}
          <span className="mt-0.5 block font-mono text-xs text-muted-foreground">
            {[...schedule.cron, ...(schedule.interval ? [`every ${schedule.interval}`] : [])].join(" · ")}
            {` · ${schedule.timezone}`}
          </span>
        </span>
      ) : (
        describeTriggerSource(trigger.source, naming)
      ),
    },
    {
      label: "Next run",
      value: !trigger.enabled ? (
        "Off"
      ) : trigger.nextFireAt ? (
        <time dateTime={trigger.nextFireAt}>
          {formatAbsoluteTime(trigger.nextFireAt)} ({formatRelativeTime(trigger.nextFireAt)})
        </time>
      ) : (
        "No upcoming run"
      ),
    },
  ];
  // The overlap policy lives on the schedule source; another kind's is not
  // readable here, so it is not guessed at.
  if (schedule) {
    rows.push({
      label: "If still running",
      value: schedule.overlap === "allow" ? "Start another run anyway" : "Skip — nothing starts while the previous run is active or paused",
    });
  }
  if (inputCount > 0) rows.push({ label: "Workflow inputs", value: `${inputCount} set` });

  return (
    <div className="space-y-3" data-testid="trigger-definition">
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[9rem_1fr]">
        {rows.map((row) => (
          <div key={row.label} className="contents">
            <dt className="text-muted-foreground">{row.label}</dt>
            <dd className="min-w-0 text-foreground">{row.value}</dd>
          </div>
        ))}
      </dl>
      {trigger.message && (
        <div>
          <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Prompt</h3>
          <p className="whitespace-pre-wrap break-words text-sm text-foreground">{trigger.message}</p>
        </div>
      )}
    </div>
  );
}

/**
 * Configuration changes are not firings. ListTriggers does not carry this
 * audit: keep the owner-authorized GetTrigger read used by the retired detail
 * page, and only fetch it while this trigger's detail is expanded. Snapshots
 * remain server-redacted; never reconstruct them from the editable definition.
 */
/** How an agent's change was approved (TriggerChange.approval). */
export function approvalLabel(approval: string): string {
  switch (approval) {
    case "card":
      return "approved by you";
    case "auto:pause":
      return "pause, no approval needed";
    case "auto:never":
      return "auto-approved (approvals: never)";
    case "auto:on_capability_change":
      return "auto-approved (did not widen its power)";
    default:
      return approval;
  }
}

function TriggerConfigurationHistory({ triggerId }: { triggerId: string }) {
  const query = useTrigger(triggerId);

  return (
    <section aria-label="Configuration history" data-testid="trigger-configuration-history">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Configuration history</h3>
      {query.isLoading ? (
        <p className="py-3 text-sm text-muted-foreground" aria-busy="true">Loading configuration history…</p>
      ) : query.isError || !query.data ? (
        <p className="py-3 text-sm text-muted-foreground" role="alert">Configuration history could not be loaded.</p>
      ) : (
        <div className="divide-y divide-border">
          {(query.data.changes ?? []).map((change) => (
            <details key={change.id} className="py-3 text-sm">
              <summary className="cursor-pointer text-foreground">
                {change.operation} · {formatAbsoluteTime(change.createdAt)} · {change.chatId ? `Agent in chat ${change.chatId}` : `User ${change.userId}`}
                {change.approval ? ` · ${approvalLabel(change.approval)}` : ""}
              </summary>
              {change.chatId && <p className="mt-2 text-xs text-muted-foreground">Thread {change.runId} · Call {change.toolCallId}</p>}
              {(change.capabilitiesAdded?.length || change.capabilitiesRemoved?.length) ? (
                <p className="mt-2 text-xs text-muted-foreground">
                  {change.capabilitiesAdded?.length ? `Added: ${change.capabilitiesAdded.join(", ")}` : ""}
                  {change.capabilitiesAdded?.length && change.capabilitiesRemoved?.length ? " · " : ""}
                  {change.capabilitiesRemoved?.length ? `Removed: ${change.capabilitiesRemoved.join(", ")}` : ""}
                </p>
              ) : null}
              <pre className="mt-2 overflow-auto whitespace-pre-wrap text-xs text-muted-foreground">
                {`Before: ${change.beforeState || "(new activation)"}\nAfter: ${change.afterState}`}
              </pre>
            </details>
          ))}
          {!query.data.changes?.length && <p className="py-4 text-sm text-muted-foreground">No configuration changes recorded yet.</p>}
        </div>
      )}
    </section>
  );
}

/**
 * The trigger's history: its recent runs and the trigger events that started
 * none, from the run log — the same rows the Runs tab shows for it.
 */
function TriggerHistory({ trigger }: { trigger: Trigger }) {
  const filters = useMemo(
    () => ({ trigger: trigger.id, range: "all" as const, includeUnlaunched: true }),
    [trigger.id],
  );
  const list = useRunList(filters);
  const runs = list.runs.slice(0, TRIGGER_HISTORY_LIMIT);
  const oldestRun = runs.length === TRIGGER_HISTORY_LIMIT ? runs[runs.length - 1]!.createdAt : 0;
  const events = list.events.filter((e) => e.occurredAt >= oldestRun).slice(0, TRIGGER_HISTORY_LIMIT);

  return (
    <div data-testid="trigger-history">
      <div className="mb-1 flex items-center justify-between gap-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">History</h3>
        <Link
          to="/workflows/runs"
          search={{ trigger: trigger.id, range: "all", allProjects: true }}
          className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          View all in Runs
        </Link>
      </div>
      {list.isLoading ? (
        <p className="text-sm text-muted-foreground" aria-busy="true">
          Loading its history…
        </p>
      ) : list.isError ? (
        <p className="text-sm text-muted-foreground" role="alert">
          Its history could not be loaded.
        </p>
      ) : runs.length === 0 && events.length === 0 ? (
        <p className="text-sm text-muted-foreground">Not fired yet.</p>
      ) : (
        // Already inside the project's inset: a rule, not another well.
        <div className="overflow-hidden rounded-md border border-border/60">
          <RunList runs={runs} events={events} groupRepeats={false} bare />
        </div>
      )}
    </div>
  );
}
