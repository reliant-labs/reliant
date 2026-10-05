// Copyright (c) 2025 Reliant Labs

/**
 * The lanes of the "Coming up" timeline (research/WORKFLOW_UI.md §7.2), kept
 * pure so what it shows is pinned by tests.
 *
 *   - One lane per ENABLED automation with a schedule. Other kinds have no
 *     future to draw. No lanes at all means the strip is hidden.
 *   - Ticks are every fire in the window, enumerated client-side from the
 *     stored schedule (lib/cronSchedule.ts) and ANCHORED on the server's
 *     `next_fire_at`: nothing is drawn before the server's next fire, and the
 *     server's next fire is always drawn. Where the two disagree, the timeline
 *     shows what will actually happen.
 *   - Lanes are ordered by their soonest tick. Past 8, only the 8 soonest are
 *     drawn and the rest are counted.
 */

import { triggerSchedule, type Trigger } from "@/api/trigger-grpc";
import { nextFireTimes } from "@/lib/cronSchedule";

export const TIMELINE_WINDOW_MS = 24 * 60 * 60 * 1000;
export const MAX_TIMELINE_LANES = 8;
/** Enough for every-15-minutes across a day; denser schedules read as a solid band anyway. */
export const MAX_TICKS_PER_LANE = 96;

export interface TimelineLane {
  trigger: Trigger;
  /** IANA zone the schedule is read in. */
  timezone: string;
  /** Epoch ms, ascending, within (now, now + window]. */
  ticks: number[];
}

export interface TimelineModel {
  lanes: TimelineLane[];
  /** Enabled schedule automations not drawn because of the lane cap. */
  hiddenCount: number;
  start: number;
  end: number;
}

/** The ticks for one trigger, anchored on the server's next fire. */
export function laneTicks(trigger: Trigger, now: number, end: number): number[] {
  const schedule = triggerSchedule(trigger);
  if (!schedule) return [];
  const computed = nextFireTimes(schedule, now, end, MAX_TICKS_PER_LANE);

  const serverNext = trigger.nextFireAt ? Date.parse(trigger.nextFireAt) : Number.NaN;
  if (Number.isNaN(serverNext)) return computed;
  if (serverNext > end) return [];
  // The server's next fire is the truth: drop anything we computed before it
  // and make sure it is drawn even if our arithmetic missed it.
  const anchored = computed.filter((tick) => tick > serverNext);
  return serverNext > now ? [serverNext, ...anchored].slice(0, MAX_TICKS_PER_LANE) : anchored;
}

export function timelineModel(
  triggers: Trigger[],
  now: number,
  windowMs = TIMELINE_WINDOW_MS,
  maxLanes = MAX_TIMELINE_LANES,
): TimelineModel {
  const end = now + windowMs;
  const lanes = triggers
    .flatMap((trigger) => {
      const schedule = triggerSchedule(trigger);
      return trigger.enabled && schedule ? [{ trigger, schedule }] : [];
    })
    .map(({ trigger, schedule }) => ({
      trigger,
      timezone: schedule.timezone || "UTC",
      ticks: laneTicks(trigger, now, end),
    }))
    .sort(
      (a, b) =>
        (a.ticks[0] ?? Number.POSITIVE_INFINITY) - (b.ticks[0] ?? Number.POSITIVE_INFINITY) ||
        a.trigger.name.localeCompare(b.trigger.name),
    );
  return {
    lanes: lanes.slice(0, maxLanes),
    hiddenCount: Math.max(0, lanes.length - maxLanes),
    start: now,
    end,
  };
}
