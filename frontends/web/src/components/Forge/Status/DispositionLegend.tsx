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
 * Each entry is the same DispositionChip the rows use, so the legend looks like
 * the thing it explains. In particular the undetermined entry keeps its full-opacity ringed
 * treatment here too.
 *
 * The count is NOT monospaced. It is a quantity, not an identifier, and the
 * earlier `font-mono text-lg` rendering made the digits the loudest thing in the
 * legend. `tabular-nums` keeps them from jittering as the tally changes, which
 * is the only thing the mono font was actually buying.
 */

import {
  dispositionTally,
  type CheckDisposition,
  type ForgeEnvStatusReport,
} from "@/services/forge/status";

import { DISPOSITION_BLURBS, DISPOSITION_LABELS, ICON_BY_STATUS } from "./checkVocabulary";
import { DispositionChip } from "./DispositionChip";

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
    <div data-testid="forge-status-legend" className="flex flex-wrap items-center gap-2">
      {ORDER.map(({ disposition, status }) => {
        const Icon = ICON_BY_STATUS[status];
        return (
          <DispositionChip
            key={disposition}
            data-legend-disposition={disposition}
            title={DISPOSITION_BLURBS[disposition]}
            disposition={disposition}
          >
            <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>{DISPOSITION_LABELS[disposition]}</span>
            <span className="tabular-nums">{tally[disposition]}</span>
          </DispositionChip>
        );
      })}
    </div>
  );
}
