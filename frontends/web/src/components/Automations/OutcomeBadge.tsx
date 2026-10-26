// Copyright (c) 2025 Reliant Labs

import Badge from "../forge-ui/badge";
import type { TriggerOutcome } from "@/hooks/trigger-model";
import { triggerEventOutcomeDisplay } from "@/lib/runStatus";

/**
 * What one trigger event did: Started a run, Skipped, or Couldn't start. This is the
 * EVENT's outcome, never the run's result; a launched run's own status is a
 * RunStatusBadge. The label carries the meaning; color only reinforces it.
 */
export function OutcomeBadge({ outcome }: { outcome: TriggerOutcome }) {
  const { label, badgeVariant } = triggerEventOutcomeDisplay(outcome);
  return (
    <span className="forge-ui inline-flex" data-event-outcome={outcome} data-badge-variant={badgeVariant}>
      <Badge label={label} variant={badgeVariant} size="md" dot />
    </span>
  );
}
