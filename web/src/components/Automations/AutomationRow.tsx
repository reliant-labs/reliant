// Copyright (c) 2025 Reliant Labs

/**
 * One automation as a compact row (research/WORKFLOW_UI.md §7.2–7.3): what it
 * is, where it runs, when, whether it is working, how its last run went, when
 * it fires next, and the switch.
 *
 * Every name on the row comes from the trigger itself (`project_name`,
 * `daemon_name`, joined by the server), so a cold load renders real names on
 * the first paint instead of ids that fill in once the stores load.
 *
 * Every status word comes from a vocabulary module: the health from
 * lib/automationHealth.ts, the launched run's status from lib/runStatus.ts,
 * and a firing that never became a run (Skipped, Failed to launch) from
 * runStatus's trigger-event outcomes.
 */

import { Link } from "@tanstack/react-router";
import { AlertOctagon, CalendarClock, CircleHelp, Plug, Webhook, Workflow } from "lucide-react";
import { toast } from "sonner";

import StatusDot from "../forge-ui/status_dot";
import { Toggle } from "../ui/Toggle";
import { Tooltip } from "../ui/Tooltip";
import { RunStatusBadge } from "../ui/RunStatusIndicator";
import { sourceKindLabel, triggerErrorMessage, type Trigger, type TriggerSource } from "@/api/trigger-grpc";
import { useSetTriggerEnabled } from "@/hooks/trigger-queries";
import { automationHealth, type AutomationHealthDisplay } from "@/lib/automationHealth";
import { describeTriggerSource } from "@/lib/cronText";
import { useIntegrationEventNaming } from "@/hooks/connection-queries";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import { OutcomeBadge } from "./OutcomeBadge";
import { automationMachineLabel } from "./daemonChoices";

/** The second line of a row: "Reliant · MacBook", or "Reliant · No machine". */
export function automationLocation(trigger: Trigger): string {
  const project = trigger.projectName ?? "Deleted project";
  return `${project} · ${automationMachineLabel(trigger)}`;
}

/**
 * The automation list's column template, shared by the rows and
 * AutomationListHeader so every cell sits under its label. Fixed widths, not
 * fractions of each row's content: a fraction grid put the same column at a
 * different x on every row. Below lg a row folds to two lines and the header
 * hides.
 */
export const AUTOMATION_ROW_GRID =
  "grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1.5 px-4 lg:grid-cols-[2rem_minmax(0,1fr)_11rem_7.5rem_10rem_6.5rem_2.75rem]";

/** Column labels over the automation rows, on the rows' own grid. */
export function AutomationListHeader() {
  return (
    <div
      aria-hidden="true"
      className={cn(
        AUTOMATION_ROW_GRID,
        "hidden border-b border-border/60 bg-background py-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground lg:grid",
      )}
    >
      <span />
      <span>Automation</span>
      <span>Schedule</span>
      <span>Health</span>
      <span>Last run</span>
      <span>Next run</span>
      <span className="text-right">On</span>
    </div>
  );
}

export function AutomationRow({ trigger }: { trigger: Trigger }) {
  const setEnabled = useSetTriggerEnabled();
  const health = automationHealth(trigger);
  const scheduleText = describeTriggerSource(trigger.source, useIntegrationEventNaming());

  const onToggle = (enabled: boolean) => {
    setEnabled.mutate(
      { id: trigger.id, enabled },
      {
        onError: (error) =>
          toast.error(`Could not ${enabled ? "enable" : "disable"} ${trigger.name}`, {
            description: triggerErrorMessage(error),
          }),
      },
    );
  };

  return (
    <li
      className={cn(AUTOMATION_ROW_GRID, "py-2.5", !trigger.enabled && "text-muted-foreground")}
      data-testid={`automation-row-${trigger.id}`}
      data-health={health.key}
    >
      <KindIcon trigger={trigger} />

      <div className="min-w-0">
        <Link
          to="/workflows/automations/$triggerId"
          params={{ triggerId: trigger.id }}
          className={cn(
            "block truncate rounded-sm text-sm font-medium hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
            trigger.enabled ? "text-foreground" : "text-muted-foreground",
          )}
        >
          {trigger.name}
        </Link>
        <p className="mt-0.5 truncate text-xs text-muted-foreground">{automationLocation(trigger)}</p>
      </div>

      {/* Below lg the row is two lines: identity and switch, then the facts. */}
      <div className="order-last col-span-3 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 lg:contents">
        <p className="min-w-0 truncate text-xs text-muted-foreground" title={scheduleText}>
          <span className="sr-only">Schedule: </span>
          {scheduleText}
        </p>

        <div className="min-w-0">
          <span className="sr-only">Health: </span>
          <HealthIndicator health={health} />
        </div>

        <div className="flex min-w-0 items-center gap-2">
          <span className="sr-only">Last run: </span>
          <LastRun trigger={trigger} />
        </div>

        <div className="min-w-0 truncate text-xs text-muted-foreground">
          <NextFire trigger={trigger} />
        </div>
      </div>

      <div className="flex items-center justify-end">
        <Toggle
          checked={trigger.enabled}
          onChange={onToggle}
          disabled={setEnabled.isPending}
          // A switch's name is stable; aria-checked carries on/off.
          srLabel={`${trigger.name} enabled`}
        />
      </div>
    </li>
  );
}

/** The icon for a source: an activation is drawn as the kind it declares. */
function sourceIcon(source: TriggerSource | undefined) {
  if (!source) return CircleHelp;
  switch (source.kind) {
    case "schedule":
      return CalendarClock;
    case "activation":
      return sourceIcon(source.declared);
    case "passthrough": {
      const armCase = (source.arm as { case: string }).case;
      if (armCase === "webhook") return Webhook;
      if (armCase === "integration") return Plug;
      if (armCase === "workflowEvent") return Workflow;
      return CircleHelp;
    }
    default:
      return CircleHelp;
  }
}

function KindIcon({ trigger }: { trigger: Trigger }) {
  const Icon = trigger.health.status === "broken" ? AlertOctagon : sourceIcon(trigger.source);
  return (
    <span className="flex h-7 w-7 items-center justify-center rounded-md border border-border/60 bg-background text-muted-foreground">
      <Icon className="h-4 w-4" aria-hidden="true" />
      <span className="sr-only">{sourceKindLabel(trigger.source)}</span>
    </span>
  );
}

/** The health as a dot and its words; the server's reason, when there is one, on hover. */
export function HealthIndicator({ health }: { health: AutomationHealthDisplay }) {
  const indicator = (
    <span className="forge-ui inline-flex items-center" data-health-dot={health.dotVariant}>
      <StatusDot variant={health.dotVariant} size="md" label={health.label} className="text-xs" />
    </span>
  );
  if (!health.detail) return indicator;
  return (
    <Tooltip content={health.detail} placement="top" delay={200} wrapperClassName="inline-flex">
      {indicator}
      <span className="sr-only">: {health.detail}</span>
    </Tooltip>
  );
}

/**
 * The last firing, and when it launched a run, that run's own status: a run
 * that launched and then failed reads Failed, never the neutral "Launched".
 */
function LastRun({ trigger }: { trigger: Trigger }) {
  const event = trigger.lastEvent;
  if (!event) return <span className="text-xs text-muted-foreground">Never run</span>;

  // "Launched" is shown only when the run behind it is gone (its chat was
  // deleted), so there is no status left to report.
  const badge =
    event.outcome === "launched" && event.runDisplayState !== undefined ? (
      <RunStatusBadge status={runStatusFromDisplayState(event.runDisplayState)} size="md" />
    ) : (
      <OutcomeBadge outcome={event.outcome} />
    );

  return (
    <>
      {event.outcomeDetail && event.outcome !== "launched" ? (
        <Tooltip content={event.outcomeDetail} placement="top" delay={200} wrapperClassName="inline-flex">
          {badge}
        </Tooltip>
      ) : (
        badge
      )}
      <time
        dateTime={event.occurredAt}
        title={formatAbsoluteTime(event.occurredAt)}
        className="truncate text-xs text-muted-foreground"
      >
        {formatRelativeTime(event.occurredAt)}
      </time>
    </>
  );
}

function NextFire({ trigger }: { trigger: Trigger }) {
  if (!trigger.enabled || !trigger.nextFireAt) {
    return (
      <span title={trigger.enabled ? "No upcoming run" : "Paused: nothing is scheduled"}>
        <span className="sr-only">Next run: none</span>
        <span aria-hidden="true">—</span>
      </span>
    );
  }
  return (
    <>
      <span className="sr-only">Next run: </span>
      {/* The column header says "Next run" from lg up; below it the row folds and needs the word. */}
      <span aria-hidden="true" className="lg:hidden">Next </span>
      <time dateTime={trigger.nextFireAt} title={formatAbsoluteTime(trigger.nextFireAt)}>
        {formatRelativeTime(trigger.nextFireAt)}
      </time>
    </>
  );
}
