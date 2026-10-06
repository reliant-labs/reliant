// Copyright (c) 2025 Reliant Labs

/**
 * What a run's launch event lets its "Started by" line say, ready to read:
 * the run page's header and its trigger card both build the line from the
 * same launch-kind vocabulary (lib/runStatus), so they read the event the
 * same way here.
 */

import type { LaunchEvent } from "@/api/run-grpc";
import type { LaunchContext } from "@/lib/runStatus";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

/** "14:02" in the viewer's clock, 24-hour. */
export function formatClock(iso: string): string {
  const time = Date.parse(iso);
  if (Number.isNaN(time)) return iso;
  return new Date(time).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
}

/**
 * The event's facts as a LaunchContext. The automation's name is the
 * caller's to add: the live name when it still exists, which the event does
 * not know.
 */
export function launchContextOf(event: LaunchEvent | null | undefined): LaunchContext {
  if (!event) return {};
  return {
    manual: event.manual,
    at: event.kind === "webhook" && event.occurredAt ? formatClock(event.occurredAt) : undefined,
    providerName: event.integration,
    providerEvent: event.providerEvent,
    sourceWorkflow: event.sourceWorkflow ? getWorkflowDisplayName(event.sourceWorkflow, true) : undefined,
    sourceOutcome: event.sourceOutcome,
  };
}
