// Copyright (c) 2025 Reliant Labs

/**
 * THE STATE LINE: what this environment should be running, and what was
 * actually observed running there.
 *
 * ── TWO HALVES, AND THE ORDER IS THE MODEL ──────────────────────────────────
 *
 * INTENT comes first, in the present tense, because it is the primary record:
 * "should be running v12, promoted by ci". It is true the moment someone
 * promotes, regardless of what any cluster is doing, and it is the thing the
 * customer decided.
 *
 * OBSERVED comes second and is visibly subordinate, because it is a reading
 * rather than a decision: the platform watched the cluster and this is what it
 * saw. It is also the half that can be missing, and today it usually is.
 *
 * ── ONE COMPONENT FOR EVERY KIND ────────────────────────────────────────────
 *
 * There is NO BRANCH ON ENVIRONMENT KIND here, and its absence is the point of
 * the whole change. Convergence used to be answerable only for environments
 * the platform places: the phase came from per-deployment rows, and a
 * self-managed env had none, so the plan had been to give those kinds a state
 * derived from a forge process's own report — a second derivation with a
 * different meaning, which is why anything showing it had to be labelled
 * "reported by forge" to stay honest.
 *
 * Nothing reports now. The reading is made by the platform watching the
 * cluster converge, it is per environment, and it exists wherever the config
 * is applied. So a customer's own cluster and one we host get the same
 * sentence from the same code, and there is no label to apply. A test asserts
 * this file never branches on kind, because re-introducing that branch is how
 * the honesty problem comes back.
 *
 * ── ABSENCE IS NOT AGREEMENT, AND ABSENCE IS NOT A FAULT ────────────────────
 *
 * "Not confirmed yet" and "can't confirm" are both rendered plainly, in the
 * ordinary quiet register, and neither borrows a word or a colour from the
 * confirmed arm. Only a reported FAILURE is styled as a problem. Nothing
 * observes these environments yet in the general case, so painting that as a
 * warning would put one on every screen and teach people to ignore the real
 * one.
 */

import Badge from "@/components/forge-ui/badge";
import {
  driftLine,
  intentLine,
  observedIsFailure,
  observedLine,
  type LiveEnv,
} from "@/services/forge/live";

import { formatTimestamp } from "../Overview/EnvironmentTable";

export function LiveState({ env }: { env: LiveEnv }) {
  const intent = intentLine(env);
  const observed = observedLine(env.observed);
  const drift = driftLine(env.drift);
  const failed = observedIsFailure(env.observed);

  return (
    <div
      className="space-y-1.5 rounded-lg border border-border/60 bg-background px-4 py-3"
      data-testid="live-state"
      data-observed={env.observed.state}
      data-drift={env.drift.state}
    >
      {/* INTENT. Absent only when nothing has ever been promoted, which the
          header already explains in better words than this line could. */}
      {intent !== "" && (
        <p data-testid="live-state-intent" className="text-sm text-foreground">
          {intent}
        </p>
      )}

      {/* OBSERVED. One size down and in the muted register, because a reading
          about the decision above is not itself a decision. */}
      <p
        data-testid="live-state-observed"
        className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground"
      >
        <span className={failed ? "text-destructive-ink" : undefined}>{observed}</span>
        {env.observed.observedAt && <span>{formatTimestamp(env.observed.observedAt)}</span>}
        {drift !== "" && <Badge label={drift} variant="neutral" size="sm" />}
      </p>

      {/* The platform's own sentence about the verdict. Kept for a failure,
          where the words someone needs are the ones the cluster produced and a
          paraphrase would be worse. Suppressed otherwise: for the states that
          mean "fine" or "nothing yet" it only restates the line above it. */}
      {failed && env.driftDetail !== "" && (
        <p data-testid="live-state-detail" className="font-mono text-2xs text-muted-foreground">
          {env.driftDetail}
        </p>
      )}
    </div>
  );
}
