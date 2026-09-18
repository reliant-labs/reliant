// Copyright (c) 2025 Reliant Labs

/**
 * One cell of the environments × images matrix.
 *
 * Two kinds render here, and keeping them apart is the whole job:
 *
 *   `absent`  — this image is not in THIS env's release at all. The report's
 *               `images` array is the union across environments, so the grid has
 *               a column for images a given release never contained. Rendered as
 *               an explicit middot with a label, matching forge's own text
 *               renderer, because a BLANK cell invites the reader to see
 *               "missing" or "zero" — claims forge never made.
 *   `state`   — a real image row, painted by CERTAINTY, not by pass/fail.
 *
 * A `data-certainty` attribute is emitted alongside the classes. It is what the
 * contract test asserts on, so the three-level distinction is pinned to
 * something stable rather than to a particular Tailwind class list that a
 * restyle would churn.
 */

import { cn } from "@/lib/utils";
import { Tooltip } from "@/components/ui/Tooltip";
import {
  shortDigest,
  stateExplanation,
  stateLabel,
  type TopologyCell as Cell,
} from "@/services/forge/topology";

import { CERTAINTY_STYLES, iconForState } from "./stateVocabulary";

export interface TopologyCellProps {
  cell: Cell;
  env: string;
}

export function TopologyCell({ cell, env }: TopologyCellProps) {
  if (cell.kind === "absent") {
    // Explicitly "not in this release" — never an empty cell.
    return (
      <td className="px-2 py-1.5 text-center align-middle" data-testid={`cell-${env}-${cell.image}`} data-absent="true">
        <Tooltip content={`${cell.image} is not in this environment's release`}>
          <span
            className="inline-flex h-7 w-full items-center justify-center rounded-md text-muted-foreground/60"
            aria-label={`${cell.image}: not in this release`}
          >
            <span aria-hidden="true">·</span>
            <span className="sr-only">Not in this release</span>
          </span>
        </Tooltip>
      </td>
    );
  }

  const style = CERTAINTY_STYLES[cell.certainty];
  const Icon = iconForState(cell.state);
  const label = stateLabel(cell.state);

  const tooltip = [
    `${cell.image} — ${label}`,
    stateExplanation(cell.state),
    cell.digest ? `Declared: ${shortDigest(cell.digest)}` : null,
    cell.running ? `Running: ${shortDigest(cell.running)}` : null,
    cell.detail ?? null,
  ]
    .filter(Boolean)
    .join("\n");

  return (
    <td className="px-2 py-1.5 align-middle" data-testid={`cell-${env}-${cell.image}`}>
      <Tooltip content={tooltip}>
        <span
          // data-certainty is the stable hook the contract test reads. The
          // classes below are the human-visible half of the same fact.
          data-certainty={cell.certainty}
          data-state={cell.state}
          className={cn(
            "inline-flex h-7 w-full items-center justify-center gap-1.5 rounded-md px-2 text-xs",
            style.container,
            style.foreground
          )}
          aria-label={`${cell.image} in ${env}: ${label}`}
        >
          <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span className="truncate font-mono">{label}</span>
        </span>
      </Tooltip>
    </td>
  );
}
