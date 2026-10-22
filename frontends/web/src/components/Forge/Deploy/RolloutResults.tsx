// Copyright (c) 2025 Reliant Labs

/**
 * Per-resource rollout outcomes, in the three-level certainty vocabulary.
 *
 * THIS COMPONENT EXISTS TO STOP ONE SPECIFIC LIE: rendering `timed_out` or
 * `not_waited` as though the resource were fine. Those two are the ABSENCE of an
 * answer — a Deployment that did not report ready inside its budget may be
 * mid-pull on a cold node or may be crash-looping, and forge does not know which
 * — so they take the unfilled, DASHED treatment that reads as provisional at a
 * glance, and they keep their own icons and sentences so an operator can tell
 * which kind of "we do not know" they are looking at.
 *
 * The mode is consulted for the empty case and the copy, never for the states
 * themselves. Under `rollout.mode: skip` every resource is legitimately
 * not_waited, and that is said in words rather than being allowed to look like
 * either a clean bill of health or a wall of failures.
 */

import { cn } from "@/lib/utils";
import { Tooltip } from "@/components/ui/Tooltip";
import {
  rolloutModeOf,
  rolloutStateOf,
  rolloutTally,
  type ForgeDeployReport,
} from "@/services/forge/deploy";

import { ROLLOUT_STATE_STYLES } from "./deployVocabulary";

export function RolloutResults({ plan }: { plan: ForgeDeployReport }) {
  const results = plan.rollout?.results ?? [];
  const mode = rolloutModeOf(plan.rollout?.mode);
  const tally = rolloutTally(plan);

  if (results.length === 0) {
    return (
      <p data-testid="deploy-rollout-empty" className="text-xs text-muted-foreground">
        {mode === "skip"
          ? "Forge was asked not to wait, so no resource was observed converging."
          : "No rollout results — nothing has been applied yet."}
      </p>
    );
  }

  return (
    <div className="space-y-2">
      {/* The tally, with the three unknown kinds counted separately from ready
          and failed. Separate numbers rather than a health percentage, because a
          single figure can only express two outcomes. */}
      <div className="flex flex-wrap items-center gap-1.5" data-testid="deploy-rollout-tally">
        <TallyChip state="ready" count={tally.ready} />
        <TallyChip state="failed" count={tally.failed} />
        <TallyChip state="timed_out" count={tally.timedOut} />
        <TallyChip state="not_waited" count={tally.notWaited} />
        <TallyChip state="unknown" count={tally.unknown} />
      </div>

      <ul className="space-y-1">
        {results.map((result) => {
          const state = rolloutStateOf(result.state);
          const style = ROLLOUT_STATE_STYLES[state];
          const Icon = style.icon;
          return (
            <li
              key={`${result.kind}/${result.name}`}
              data-testid={`deploy-rollout-${result.name}`}
              data-state={state}
              data-certainty={certaintyAttr(state)}
              className={cn(
                "flex flex-wrap items-center gap-x-2 gap-y-0.5 rounded-md px-3 py-1.5",
                style.container
              )}
            >
              <Tooltip content={style.blurb}>
                <span className={cn("flex items-center gap-1 text-2xs font-medium", style.foreground)}>
                  <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
                  {style.label}
                </span>
              </Tooltip>
              <span className="font-mono text-2xs text-foreground">
                {result.kind}/{result.name}
              </span>
              {result.detail && (
                <span className="text-2xs text-muted-foreground">{result.detail}</span>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/**
 * One tally chip, rendered only when non-zero.
 *
 * Zeroes are omitted rather than shown, so the chips that ARE on screen each
 * describe something that happened — a row of four numbers, three of them zero,
 * trains the eye to skip the row entirely.
 */
function TallyChip({
  state,
  count,
}: {
  state: keyof typeof ROLLOUT_STATE_STYLES;
  count: number;
}) {
  if (count <= 0) return null;
  const style = ROLLOUT_STATE_STYLES[state];
  return (
    <span
      data-testid={`deploy-tally-${state}`}
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-2xs",
        style.container,
        style.foreground
      )}
    >
      {count} {style.label.toLowerCase()}
    </span>
  );
}

/** The certainty category, exposed as an attribute so a test can assert the grouping. */
function certaintyAttr(state: ReturnType<typeof rolloutStateOf>): string {
  return state === "ready" ? "known-good" : state === "failed" ? "known-bad" : "unknown";
}
