// Copyright (c) 2026 Reliant Labs

/**
 * How a panel's card reads its figure, and its comparison: the one place both
 * are decided, so Needs attention (AttentionPanel), which reads each watched
 * figure again, asks exactly what the figure's card asks. Their reads then
 * share one React Query cache entry, and both judge the same numbers.
 */

import { panelRead, type PanelOffset } from "@/hooks/observability-queries";
import type { Panel } from "@/services/controlPlane/observability/types";
import { rangeForBucket, rangeMs, readRange, type RangePreset } from "@/services/controlPlane/observability/window";

import type { CompareTarget } from "./dashboardModel";

export interface CardCompare {
  /** Where the comparison read's window sits: ending when the release was promoted, or the previous period of the range read. */
  offset: PanelOffset;
  /**
   * How far a chart shifts the comparison series forward to lay it on the
   * current axis: the period the read went back by, so the plot matches the
   * fetch (a traces line on "Last 30 days" reads 7 days, and its previous
   * 7 days move forward 7, not 30). A release comparison shifts by the
   * target's offset, as before.
   */
  shiftMs: number;
}

export interface CardRead {
  /**
   * The range the card reads: the dashboard's, or, for a panel with a fixed
   * bucket (minStep: "1d") that the dashboard's range cannot hold twice, the
   * smallest preset that can.
   */
  range: RangePreset;
  /** Whether range is wider than the dashboard's (the card says so). */
  widened: boolean;
  /** Its comparison read, over the same range; null with no comparison on. */
  compare: CardCompare | null;
}

/**
 * How the card of panel reads it on a dashboard showing rangeOfCard, against
 * target. The previous period is as long as what the panel reads (readRange):
 * a traces panel on "Last 30 days" reads 7 days, so it compares with the 7
 * days before them.
 */
export function cardRead(rangeOfCard: RangePreset, panel: Panel, target: CompareTarget): CardRead {
  const widening = panel.minStep ? rangeForBucket(rangeOfCard, panel.minStep) : null;
  const range = widening?.range ?? rangeOfCard;
  let compare: CardCompare | null = null;
  if (target.mode === "release") {
    compare = { offset: { endsAtMs: target.promotion.promotedAtMs }, shiftMs: target.offsetMs };
  } else if (target.mode === "period") {
    const periodMs = rangeMs(readRange(range, panelRead(panel)));
    compare = { offset: { endsAgoMs: periodMs }, shiftMs: periodMs };
  }
  return { range, widened: widening?.widened ?? false, compare };
}
