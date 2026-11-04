// Copyright (c) 2025 Reliant Labs

/**
 * The status pieces every surface that shows a trigger shares: its kind's
 * icon, its health, its last trigger event and when it fires next. The
 * Workflows list's rows and the workflow page's Triggers card both render
 * them, so a trigger reads the same wherever it appears.
 *
 * Every status word comes from a vocabulary module: the health from
 * lib/automationHealth.ts, the launched run's status from lib/runStatus.ts,
 * and a trigger event that never became a run (Skipped, Couldn't start) from
 * OutcomeBadge.
 */

import { CalendarClock, CircleHelp, Plug, Webhook, Workflow } from "lucide-react";

import Status from "../forge-ui/status";
import Tooltip from "../forge-ui/tooltip";
import { RunStatusBadge } from "../runs/RunStatusIndicator";
import type { Trigger, TriggerSource } from "@/hooks/trigger-model";
import type { AutomationHealthDisplay } from "@/lib/automationHealth";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { OutcomeBadge } from "../Automations/OutcomeBadge";
import { automationMachineLabel } from "../Automations/daemonChoices";

/** "Reliant · MacBook", or "Reliant · No machine". */
export function automationLocation(trigger: Trigger): string {
  const project = trigger.projectName ?? "Deleted project";
  return `${project} · ${automationMachineLabel(trigger)}`;
}

/** The icon for a source: an activation is drawn as the kind it declares. */
export function sourceIcon(source: TriggerSource | undefined) {
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

/** The health as a dot and its words; the server's reason, when there is one, on hover. */
export function HealthIndicator({ health }: { health: AutomationHealthDisplay }) {
  const indicator = (
    <span className="forge-ui inline-flex items-center" data-health-dot={health.dotVariant}>
      <Status tone={health.dotVariant} size="md" label={health.label} className="text-xs" />
    </span>
  );
  if (!health.detail) return indicator;
  return (
    <Tooltip content={health.detail} side="top" delay={200} triggerClassName="inline-flex">
      {indicator}
      <span className="sr-only">: {health.detail}</span>
    </Tooltip>
  );
}

/**
 * The last trigger event, and when it started a run, that run's own status: a
 * run that started and then failed reads Failed, never the neutral outcome.
 */
export function LastTriggerEvent({ event }: { event: Trigger["lastEvent"] }) {
  if (!event) return <span className="text-xs text-muted-foreground">Not fired yet</span>;

  // The outcome is shown only when the run behind it is gone (its chat was
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
        <Tooltip content={event.outcomeDetail} side="top" delay={200} triggerClassName="inline-flex">
          {badge}
        </Tooltip>
      ) : (
        badge
      )}
      <time dateTime={event.occurredAt} title={formatAbsoluteTime(event.occurredAt)} className="truncate text-xs text-muted-foreground">
        {formatRelativeTime(event.occurredAt)}
      </time>
    </>
  );
}

/** When it fires next, or a dash with the reason on hover. */
export function NextFire({ trigger }: { trigger: Pick<Trigger, "enabled" | "nextFireAt"> }) {
  if (!trigger.enabled || !trigger.nextFireAt) {
    return (
      <span title={trigger.enabled ? "No upcoming run" : "Off: nothing is scheduled"}>
        <span className="sr-only">Next run: none</span>
        <span aria-hidden="true">—</span>
      </span>
    );
  }
  return (
    <>
      <span className="sr-only">Next run: </span>
      <time dateTime={trigger.nextFireAt} title={formatAbsoluteTime(trigger.nextFireAt)}>
        {formatRelativeTime(trigger.nextFireAt)}
      </time>
    </>
  );
}
