// Copyright (c) 2025 Reliant Labs

import Badge from "../forge-ui/badge";
import type { TriggerOutcome } from "@/api/trigger-grpc";

const OUTCOME_BADGE: Record<
  TriggerOutcome,
  { label: string; variant: "success" | "warning" | "error" | "neutral" }
> = {
  launched: { label: "Launched", variant: "success" },
  skipped: { label: "Skipped", variant: "warning" },
  failed: { label: "Failed", variant: "error" },
  unknown: { label: "Unknown", variant: "neutral" },
};

/** What one firing did. The label carries the meaning; color only reinforces it. */
export function OutcomeBadge({ outcome }: { outcome: TriggerOutcome }) {
  const { label, variant } = OUTCOME_BADGE[outcome];
  return <Badge label={label} variant={variant} size="sm" dot />;
}
