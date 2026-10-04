// Copyright (c) 2025 Reliant Labs

import Badge from "../forge-ui/badge";
import type { TriggerOutcome } from "@/api/trigger-grpc";
import { triggerEventOutcomeDisplay } from "@/lib/runStatus";

/**
 * What one firing did: Launched, Skipped, or Failed to launch. This is the
 * EVENT's outcome, never the run's result; a launched run's own status is a
 * RunStatusBadge. The label carries the meaning; color only reinforces it.
 */
export function OutcomeBadge({ outcome }: { outcome: TriggerOutcome }) {
  const { label, badgeVariant } = triggerEventOutcomeDisplay(outcome);
  return (
    <span className="forge-ui inline-flex" data-event-outcome={outcome} data-badge-variant={badgeVariant}>
      <Badge label={label} variant={badgeVariant} size="sm" dot />
    </span>
  );
}
