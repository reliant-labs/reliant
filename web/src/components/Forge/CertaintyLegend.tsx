// Copyright (c) 2025 Reliant Labs

/**
 * The certainty tally — three counts on one line.
 *
 * WHAT CHANGED AND WHY. This was three large prose cards, each carrying a
 * heading, a two-line explanation and a row of state icons, permanently
 * occupying the top of the screen in order to show three numbers (typically
 * "0 / 0 / 4"). The information-to-space ratio was the complaint, and it was a
 * fair one: the explanation is read ONCE, by a user meeting the vocabulary for
 * the first time, while the counts are read every single visit. Sizing the
 * page for the thing read once pushed the matrix — the actual content — below
 * the fold.
 *
 * So the counts stay on the page and the explanation moves into a popover.
 * That is the standard trade for reference material: available on demand, not
 * occupying permanent real estate.
 *
 * WHAT MUST NOT CHANGE, and did not. The three-level vocabulary is
 * load-bearing — `unknown` is a THIRD thing, not a soft pass, and the whole
 * screen's honesty depends on a reader not mistaking "nobody looked" for
 * "looked and it was fine". That distinction is preserved in full:
 *
 *   - three separate counts, never summed into a pass/fail pair;
 *   - the dot treatments still come from CERTAINTY_STYLES, so `unknown` keeps
 *     its hollow/dashed treatment and does not read as a green tick;
 *   - the popover still names which of the six states lands in each category,
 *     and still carries the sentence that says unverified is neither healthy
 *     nor broken.
 *
 * The counts remain summed from the rendered matrix (certaintyTally) rather
 * than read from forge's `tally`, so this strip can never disagree with the
 * cells beneath it after a per-env verify has been merged in.
 *
 * The tally digits are deliberately NOT monospace: a count is not an
 * identifier. `tabular-nums` keeps them from jittering as they update, which
 * is the only thing a code face would have bought here.
 */

import { cn } from "@/lib/utils";
import { HelpPopover } from "@/components/ui/HelpPopover";
import type { Certainty, ForgeTopologyReport } from "@/services/forge/topology";
import { certaintyTally, stateLabel } from "@/services/forge/topology";

import {
  CERTAINTY_BLURBS,
  CERTAINTY_LABELS,
  CERTAINTY_STYLES,
  iconForState,
} from "./stateVocabulary";

/** Which states belong to each category, shown so the mapping is not a mystery. */
const STATES_BY_CERTAINTY: Record<Certainty, string[]> = {
  "known-good": ["match"],
  "known-bad": ["drift", "missing"],
  unknown: ["not_verified", "unreachable", "untagged"],
};

const ORDER: Certainty[] = ["known-good", "known-bad", "unknown"];

/** The vocabulary, in full. Shown on demand rather than permanently. */
function CertaintyHelp() {
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">
        Cells below fall into three levels of certainty, not two.
      </p>
      {ORDER.map((certainty) => (
        <div key={certainty}>
          <p
            className={cn(
              "text-xs font-medium",
              CERTAINTY_STYLES[certainty].foreground
            )}
          >
            {CERTAINTY_LABELS[certainty]}
          </p>
          <p className="mt-0.5 text-xs leading-snug text-muted-foreground">
            {CERTAINTY_BLURBS[certainty]}
          </p>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
            {STATES_BY_CERTAINTY[certainty].map((state) => {
              const Icon = iconForState(state);
              return (
                <span
                  key={state}
                  className="inline-flex items-center gap-1 text-xs text-muted-foreground"
                >
                  <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
                  {stateLabel(state)}
                </span>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

export function CertaintyLegend({ report }: { report: ForgeTopologyReport | null }) {
  const totals = certaintyTally(report);

  return (
    <section
      aria-labelledby="certainty-legend-heading"
      data-testid="certainty-legend"
      className="flex flex-wrap items-center gap-x-6 gap-y-2"
    >
      <h2 id="certainty-legend-heading" className="sr-only">
        Certainty of the cells below — three levels, not two
      </h2>

      {ORDER.map((certainty) => {
        const style = CERTAINTY_STYLES[certainty];
        return (
          <span
            key={certainty}
            data-testid={`legend-${certainty}`}
            data-certainty={certainty}
            className="inline-flex items-baseline gap-2"
          >
            {/*
             * The dot carries the same fill/border axis the cells use, so the
             * uncertain category still reads as provisional at a glance: the
             * certain pair are filled, `unknown` is hollow and dashed. That is
             * the one axis that survives greyscale and colour-vision
             * deficiency, which is why it is not colour alone.
             */}
            <span
              aria-hidden="true"
              className={cn(
                "inline-block h-2 w-2 shrink-0 translate-y-[-1px] rounded-full",
                style.container
              )}
            />
            <span className="text-sm font-medium tabular-nums text-foreground">
              {totals[certainty]}
            </span>
            <span className="text-sm text-muted-foreground">
              {CERTAINTY_LABELS[certainty]}
            </span>
          </span>
        );
      })}

      <HelpPopover
        title="Three levels of certainty"
        content={<CertaintyHelp />}
        iconSize="sm"
      />
    </section>
  );
}
