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
 * Each entry is the same `Badge` the rows use, with the same variant and the
 * same vocabulary classes layered over it, so the legend looks like the thing it
 * explains. In particular the undetermined entry keeps its full-opacity ringed
 * treatment here too.
 *
 * The count is NOT monospaced. It is a quantity, not an identifier, and the
 * earlier `font-mono text-lg` rendering made the digits the loudest thing in the
 * legend. `tabular-nums` keeps them from jittering as the tally changes, which
 * is the only thing the mono font was actually buying.
 */

import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui";
import {
  dispositionTally,
  type CheckDisposition,
  type ForgeEnvStatusReport,
} from "@/services/forge/status";

import {
  DISPOSITION_BADGE_VARIANT,
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
    <div data-testid="forge-status-legend" className="flex flex-wrap items-center gap-2">
      {ORDER.map(({ disposition, status }) => {
        const style = DISPOSITION_STYLES[disposition];
        const Icon = ICON_BY_STATUS[status];
        return (
          <Badge
            key={disposition}
            data-legend-disposition={disposition}
            title={DISPOSITION_BLURBS[disposition]}
            variant={DISPOSITION_BADGE_VARIANT[disposition]}
            size="sm"
            className={cn(
              "gap-1.5 rounded-md font-sans hover:scale-100",
              style.container,
              style.foreground
            )}
          >
            <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>{DISPOSITION_LABELS[disposition]}</span>
            <span className="tabular-nums">{tally[disposition]}</span>
          </Badge>
        );
      })}
    </div>
  );
}
