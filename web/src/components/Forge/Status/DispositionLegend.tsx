// Copyright (c) 2025 Reliant Labs

/**
 * The five-level legend, with live counts.
 *
 * It exists for the same reason the topology screen's certainty legend does: the
 * vocabulary is only self-evident once you already know it, and the reader who
 * most needs to know that grey-dashed is not green is the one seeing the panel
 * for the first time, mid-incident.
 *
 * Counts are summed from the rendered rows (dispositionTally reads the same
 * projection the list does), so the header can never claim a tally the list below
 * it contradicts.
 *
 * The undetermined entry keeps its full-opacity ringed treatment here too, so the
 * legend looks like the rows it explains.
 */

import { cn } from "@/lib/utils";
import {
  dispositionTally,
  type CheckDisposition,
  type ForgeEnvStatusReport,
} from "@/services/forge/status";

import {
  DISPOSITION_BLURBS,
  DISPOSITION_LABELS,
  DISPOSITION_STYLES,
  ICON_BY_STATUS,
} from "./checkVocabulary";

/** The order the legend reads in: worst-first, then the two unmeasured ones. */
const ORDER: Array<{ disposition: CheckDisposition; status: string }> = [
  { disposition: "measured-bad", status: "fail" },
  { disposition: "measured-degraded", status: "warn" },
  { disposition: "measured-good", status: "pass" },
  { disposition: "undetermined", status: "unknown" },
  { disposition: "not-applicable", status: "skip" },
];

export function DispositionLegend({ report }: { report: ForgeEnvStatusReport }) {
  const tally = dispositionTally(report);

  return (
    <div
      data-testid="forge-status-legend"
      className="flex flex-wrap gap-2 rounded-lg border border-border px-3 py-2"
    >
      {ORDER.map(({ disposition, status }) => {
        const style = DISPOSITION_STYLES[disposition];
        const Icon = ICON_BY_STATUS[status];
        return (
          <div
            key={disposition}
            data-legend-disposition={disposition}
            title={DISPOSITION_BLURBS[disposition]}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs",
              style.container,
              style.foreground
            )}
          >
            <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>{DISPOSITION_LABELS[disposition]}</span>
            <span className="font-mono">{tally[disposition]}</span>
          </div>
        );
      })}
    </div>
  );
}
