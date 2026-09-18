// Copyright (c) 2025 Reliant Labs

/**
 * The legend, and the header tally rolled up into the same three categories.
 *
 * The legend is not decoration. The screen's central claim — that "not checked"
 * is a THIRD thing rather than a soft pass — is only legible if the reader is
 * told the vocabulary has three levels, so the legend states it, with the count
 * of cells in each, and names which of the six states land in each category.
 *
 * The counts are summed from the rendered matrix (see certaintyTally) rather than
 * read from forge's `tally`, so the header can never disagree with the cells
 * beneath it after a per-env verify has been merged in.
 */

import { cn } from "@/lib/utils";
import type { Certainty, ForgeTopologyReport } from "@/services/forge/topology";
import { certaintyTally, stateLabel } from "@/services/forge/topology";

import { CERTAINTY_BLURBS, CERTAINTY_LABELS, CERTAINTY_STYLES, iconForState } from "./stateVocabulary";

/** Which states belong to each category, shown so the mapping is not a mystery. */
const STATES_BY_CERTAINTY: Record<Certainty, string[]> = {
  "known-good": ["match"],
  "known-bad": ["drift", "missing"],
  unknown: ["not_verified", "unreachable", "untagged"],
};

const ORDER: Certainty[] = ["known-good", "known-bad", "unknown"];

export function CertaintyLegend({ report }: { report: ForgeTopologyReport | null }) {
  const totals = certaintyTally(report);

  return (
    <div className="grid gap-3 md:grid-cols-3" data-testid="certainty-legend">
      {ORDER.map((certainty) => {
        const style = CERTAINTY_STYLES[certainty];
        return (
          <div
            key={certainty}
            data-testid={`legend-${certainty}`}
            data-certainty={certainty}
            className={cn("rounded-lg p-3", style.container)}
          >
            <div className={cn("flex items-baseline justify-between gap-2", style.foreground)}>
              <span className="text-sm font-medium">{CERTAINTY_LABELS[certainty]}</span>
              <span className="font-mono text-lg leading-none">{totals[certainty]}</span>
            </div>
            <p className="pt-1.5 text-2xs leading-snug text-muted-foreground">
              {CERTAINTY_BLURBS[certainty]}
            </p>
            <div className="flex flex-wrap gap-x-3 gap-y-1 pt-2">
              {STATES_BY_CERTAINTY[certainty].map((state) => {
                const Icon = iconForState(state);
                return (
                  <span
                    key={state}
                    className={cn("inline-flex items-center gap-1 text-2xs", style.foreground)}
                  >
                    <Icon className="h-3 w-3" aria-hidden="true" />
                    {stateLabel(state)}
                  </span>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}
