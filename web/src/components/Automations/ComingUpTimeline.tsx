// Copyright (c) 2025 Reliant Labs

/**
 * "Coming up": the next 24 hours as a horizontal strip, one lane per enabled
 * schedule automation, a tick at each fire (research/WORKFLOW_UI.md §7.2). It
 * answers "what happens overnight", which a list sorted by next fire cannot.
 *
 * What is drawn is decided in comingUp.ts. Ticks are decoration for sighted
 * users; the same fires are listed in a visually hidden ordered list.
 */

import { useEffect, useId, useState } from "react";
import { Link } from "@tanstack/react-router";

import Card from "../forge-ui/card";
import type { Trigger } from "@/api/trigger-grpc";
import { formatZonedClock } from "@/lib/cronSchedule";
import { timelineModel, type TimelineLane } from "./comingUp";

/** Re-render on the minute so the strip slides with time. */
function useMinuteClock(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(id);
  }, []);
  return now;
}

/** "Nightly triage · 09:00 Europe/London". */
export function tickLabel(name: string, tick: number, timezone: string): string {
  return `${name} · ${formatZonedClock(tick, timezone)} ${timezone}`;
}

/** Hour marks in the viewer's own clock, every 6 hours across the window. */
function hourMarks(start: number, end: number): Array<{ at: number; label: string }> {
  const SIX_HOURS = 6 * 60 * 60 * 1000;
  const first = new Date(start);
  first.setMinutes(0, 0, 0);
  first.setHours(first.getHours() + 1);
  while (first.getHours() % 6 !== 0) first.setHours(first.getHours() + 1);
  const marks: Array<{ at: number; label: string }> = [];
  for (let at = first.getTime(); at < end; at += SIX_HOURS) {
    marks.push({
      at,
      label: new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }),
    });
  }
  return marks;
}

const SR_TICKS_PER_LANE = 3;

export function ComingUpTimeline({ triggers, now: nowOverride }: { triggers: Trigger[]; now?: number }) {
  const clock = useMinuteClock();
  const now = nowOverride ?? clock;
  const headingId = useId();
  const model = timelineModel(triggers, now);

  if (model.lanes.length === 0) return null;

  const span = model.end - model.start;
  const position = (at: number) => `${((at - model.start) / span) * 100}%`;
  const marks = hourMarks(model.start, model.end);

  return (
    <section aria-labelledby={headingId} className="mb-6" data-testid="coming-up-timeline">
      <Card padding="none">
        <div className="flex items-baseline justify-between gap-4 border-b border-border/60 px-5 py-3">
          <h2 id={headingId} className="text-sm font-semibold text-foreground">
            Coming up
          </h2>
          <span className="text-xs text-muted-foreground">Next 24 hours</span>
        </div>

        <div className="px-5 py-4" aria-hidden="true">
          <div className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)] gap-x-4 gap-y-1.5">
            <div />
            <div className="relative h-4 text-2xs text-muted-foreground">
              <span className="absolute left-0">Now</span>
              {marks.map((mark) => (
                <span key={mark.at} className="absolute -translate-x-1/2" style={{ left: position(mark.at) }}>
                  {mark.label}
                </span>
              ))}
            </div>

            {model.lanes.map((lane) => (
              <Lane key={lane.trigger.id} lane={lane} position={position} marks={marks.map((m) => m.at)} />
            ))}
          </div>
          {model.hiddenCount > 0 && (
            <p className="mt-3 text-xs text-muted-foreground" data-testid="coming-up-more">
              +{model.hiddenCount} more
            </p>
          )}
        </div>

        <ol className="sr-only" aria-label="Upcoming runs in the next 24 hours">
          {model.lanes.map((lane) => (
            <li key={lane.trigger.id}>
              {lane.ticks.length === 0
                ? `${lane.trigger.name}: nothing in the next 24 hours`
                : [
                    ...lane.ticks.slice(0, SR_TICKS_PER_LANE).map((tick) => tickLabel(lane.trigger.name, tick, lane.timezone)),
                    ...(lane.ticks.length > SR_TICKS_PER_LANE
                      ? [`and ${lane.ticks.length - SR_TICKS_PER_LANE} more`]
                      : []),
                  ].join("; ")}
            </li>
          ))}
          {model.hiddenCount > 0 && <li>{model.hiddenCount} more automations not shown</li>}
        </ol>
      </Card>
    </section>
  );
}

function Lane({
  lane,
  position,
  marks,
}: {
  lane: TimelineLane;
  position: (at: number) => string;
  marks: number[];
}) {
  return (
    <>
      <Link
        to="/automations/$triggerId"
        params={{ triggerId: lane.trigger.id }}
        tabIndex={-1}
        className="truncate text-xs text-foreground hover:underline"
      >
        {lane.trigger.name}
      </Link>
      <div
        className="relative h-5 rounded-sm border border-border/60 bg-background"
        data-testid={`coming-up-lane-${lane.trigger.id}`}
      >
        {marks.map((at) => (
          <span key={at} className="absolute inset-y-0 w-px bg-border/60" style={{ left: position(at) }} />
        ))}
        {lane.ticks.map((tick) => (
          <span
            key={tick}
            title={tickLabel(lane.trigger.name, tick, lane.timezone)}
            data-testid="coming-up-tick"
            className="absolute inset-y-0.5 w-1 -translate-x-1/2 rounded-full bg-primary"
            style={{ left: position(tick) }}
          />
        ))}
      </div>
    </>
  );
}
