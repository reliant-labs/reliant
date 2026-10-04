// Copyright (c) 2025 Reliant Labs

/**
 * What fired a run nobody started by typing (WORKFLOW_UI.md §4.2, item 2): an
 * inset pinned above the transcript, so a reader knows why a conversation
 * exists before they read it.
 *
 * The facts come from the run's launch event (TriggerService.GetLaunchEvent):
 * the slot a schedule fired for, whether it was a "Run now", and what the run
 * was started with. Collapsed it is one line; expanded it shows the prompt as
 * sent and the workflow, presets and inputs, read-only.
 *
 * Says nothing it cannot back up. A run from before launch events has no
 * event, so the card names its source and stops there; the host does not
 * render the card until the event has loaded, so nothing shown here later
 * changes meaning (§4.4).
 */

import { useId, useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";

import type { LaunchEvent } from "@/api/run-grpc";
import { launchKindDisplay } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import { CardInset } from "../forge-ui/card";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { LaunchKindIcon } from "./LaunchKindIcon";

interface TriggerCardProps {
  launchKind?: string | null;
  triggerId?: string;
  /** The automation's current name; unset when it has been deleted. */
  triggerName?: string;
  /** The automation's schedule timezone, when it still exists. */
  timezone?: string;
  /** The chat whose agent started this run, when it is known and the caller's. */
  parent?: { chatId: string; title: string };
  /** The launch event; null for a run that predates launch events. */
  event?: LaunchEvent | null;
  /** The prompt the run was started with, when known. */
  prompt?: string;
}

const linkClass =
  "font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40";

/**
 * A scheduled slot, as "Tue 6 Oct, 09:00 (Europe/London)". Rendered in the
 * automation's own timezone when known (that is the clock the schedule is
 * written against), else the viewer's. Assembled from parts so the order does
 * not depend on the locale's date pattern.
 */
export function formatScheduleSlot(iso: string, timezone?: string): string {
  const time = Date.parse(iso);
  if (Number.isNaN(time)) return iso;
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat(undefined, {
      weekday: "short",
      day: "numeric",
      month: "short",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
      timeZone: timezone,
    }).formatToParts(new Date(time));
  } catch {
    // An unknown zone name: fall back to the viewer's clock, unlabelled.
    return formatScheduleSlot(iso);
  }
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((p) => p.type === type)?.value ?? "";
  const slot = `${part("weekday")} ${part("day")} ${part("month")}, ${part("hour")}:${part("minute")}`;
  return timezone ? `${slot} (${timezone})` : slot;
}

const LATE_THRESHOLD_MS = 60_000;

/** "4 min" / "2 h 5 min" when the fire ran more than a minute after its slot, else undefined. */
export function lateBy(scheduledFor?: string, firedAt?: string): string | undefined {
  if (!scheduledFor || !firedAt) return undefined;
  const lateMs = Date.parse(firedAt) - Date.parse(scheduledFor);
  if (!(lateMs > LATE_THRESHOLD_MS)) return undefined;
  const minutes = Math.round(lateMs / 60_000);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? `${hours} h ${rest} min` : `${hours} h`;
}

function formatClock(iso: string): string {
  const time = Date.parse(iso);
  if (Number.isNaN(time)) return iso;
  return new Date(time).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
}

export function TriggerCard({
  launchKind,
  triggerId,
  triggerName,
  timezone,
  parent,
  event,
  prompt,
}: TriggerCardProps) {
  const kind = launchKindDisplay(launchKind).kind;
  const [expanded, setExpanded] = useState(false);
  const detailsId = useId();

  // A run a person started has nothing to explain. The marker keeps the
  // absence observable to tests without rendering anything a user sees.
  if (kind === "chat.start") return <span hidden data-testid="trigger-card-absent" />;

  const unattended = kind === "schedule";
  // The live automation links; one deleted since keeps the name it fired under.
  const automation =
    triggerId && triggerName ? (
      <Link to="/workflows/automations/$triggerId" params={{ triggerId }} className={linkClass}>
        {triggerName}
      </Link>
    ) : event?.triggerName ? (
      <span className="font-medium">{event.triggerName} (since deleted)</span>
    ) : (
      "an automation that has since been deleted"
    );
  const start = event?.start;
  const late = event?.manual ? undefined : lateBy(event?.scheduledFor, event?.firedAt);

  return (
    <CardInset padding="sm" className="px-3 text-xs text-muted-foreground" data-testid="trigger-card">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <LaunchKindIcon kind={kind} />
        <span className="text-foreground">
          {kind === "schedule" ? (
            event?.manual ? (
              <>
                Run now on {automation}
                <span className="text-muted-foreground"> · by you at {formatClock(event.occurredAt)}</span>
              </>
            ) : event?.scheduledFor ? (
              <>
                Scheduled for{" "}
                <time dateTime={event.scheduledFor} className="font-medium">
                  {formatScheduleSlot(event.scheduledFor, timezone)}
                </time>{" "}
                by {automation}
              </>
            ) : (
              <>Scheduled by {automation}</>
            )
          ) : kind === "agent.start_run" ? (
            <>
              Started by an agent
              {parent && (
                <>
                  {" in "}
                  <Link to="/workflows/runs/$runId" params={{ runId: parent.chatId }} className={linkClass}>
                    {parent.title || "another run"}
                  </Link>
                </>
              )}
              <span className="text-muted-foreground">
                {" · tool call "}
                <code className="font-mono">start_run</code>
              </span>
            </>
          ) : (
            launchKindDisplay(launchKind, { triggerName }).startedByLine
          )}
        </span>
        {late && <span data-testid="trigger-card-late">· Fired {late} late (catch-up)</span>}
        {unattended && <span>· Unattended: questions and approvals were answered automatically</span>}
        {start && (
          <button
            type="button"
            onClick={() => setExpanded((open) => !open)}
            aria-expanded={expanded}
            aria-controls={detailsId}
            className="ml-auto inline-flex items-center gap-1 rounded-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {expanded ? "Hide what it ran with" : "Show what it ran with"}
            <ChevronDown
              className={cn("h-3.5 w-3.5 transition-transform motion-reduce:transition-none", expanded && "rotate-180")}
              aria-hidden="true"
            />
          </button>
        )}
      </div>

      {start && expanded && (
        <dl
          id={detailsId}
          data-testid="trigger-card-details"
          className="mt-3 grid grid-cols-[minmax(0,8rem)_minmax(0,1fr)] gap-x-4 gap-y-2 border-t border-border/60 pt-3"
        >
          {prompt !== undefined && (
            <>
              <dt>Prompt</dt>
              <dd className="whitespace-pre-wrap break-words text-foreground">{prompt}</dd>
            </>
          )}
          <dt>Workflow</dt>
          <dd className="text-foreground">{start.workflow ? getWorkflowDisplayName(start.workflow, true) : "Default"}</dd>
          <dt>Presets</dt>
          <dd className="text-foreground">
            <KeyValues entries={Object.entries(start.presets).map(([group, name]) => [group || "Workflow", name])} />
          </dd>
          <dt>Inputs</dt>
          <dd className="text-foreground">
            <KeyValues entries={flattenInputs(start.params)} />
          </dd>
        </dl>
      )}
    </CardInset>
  );
}

/** Nested inputs as dotted keys, so a grouped input reads "review.strictness". */
function flattenInputs(params: Record<string, unknown>, prefix = ""): Array<[string, string]> {
  const out: Array<[string, string]> = [];
  for (const [key, value] of Object.entries(params)) {
    const name = prefix ? `${prefix}.${key}` : key;
    if (typeof value === "object" && value !== null && !Array.isArray(value)) {
      out.push(...flattenInputs(value as Record<string, unknown>, name));
    } else {
      out.push([name, typeof value === "string" ? value : JSON.stringify(value)]);
    }
  }
  return out;
}

function KeyValues({ entries }: { entries: Array<[string, string]> }) {
  if (entries.length === 0) return <span className="text-muted-foreground">None</span>;
  return (
    <ul className="space-y-0.5">
      {entries.map(([key, value]) => (
        <li key={key} className="break-words">
          <span className="font-mono text-muted-foreground">{key}</span>{" "}
          <span className="font-mono">{value}</span>
        </li>
      ))}
    </ul>
  );
}
