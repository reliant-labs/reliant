// Copyright (c) 2025 Reliant Labs

/**
 * The two shapes a run status renders as, both driven by lib/runStatus.ts:
 * a pill (RunStatusBadge) for rows and headers, a dot (RunStatusDot) for
 * dense lists. Nothing here decides a label or a color; it only draws what
 * the vocabulary module returns.
 *
 * Both wrap forge's primitives in `.forge-ui`. Forge's `info` and `pending`
 * variants resolve through `accent`, which outside that scope is reliant's
 * muted surface tint, so a Queued or Running pill would otherwise render
 * almost colorless (see the accent-collision note in index.css).
 */

import Badge from "../forge-ui/badge";
import StatusDot from "../forge-ui/status_dot";
import { cn } from "../../lib/utils";
import type { RunStatusDisplay } from "../../lib/runStatus";

export function RunStatusBadge({
  status,
  size = "sm",
  className,
}: {
  status: RunStatusDisplay;
  size?: "sm" | "md";
  className?: string;
}) {
  return (
    <span
      className={cn("forge-ui inline-flex", className)}
      data-run-status={status.key}
      data-badge-variant={status.badgeVariant}
    >
      <Badge label={status.label} variant={status.badgeVariant} size={size} dot />
    </span>
  );
}

export function RunStatusDot({
  status,
  size = "md",
  showLabel = false,
  className,
}: {
  status: RunStatusDisplay;
  size?: "sm" | "md" | "lg";
  /**
   * Render the label next to the dot; otherwise it is the accessible name
   * only, and a sighted hover label is the caller's Tooltip.
   */
  showLabel?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cn("forge-ui inline-flex items-center", className)}
      data-run-status={status.key}
      data-dot-variant={status.dotVariant}
      role="img"
      aria-label={status.label}
    >
      <StatusDot
        variant={status.dotVariant}
        size={size}
        pulse={status.pulse}
        label={showLabel ? status.label : undefined}
      />
    </span>
  );
}
