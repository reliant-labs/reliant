// Copyright (c) 2025 Reliant Labs

/**
 * THE PLAN CHANGED BETWEEN READING IT AND APPROVING IT, so nothing was
 * deployed.
 *
 * This is the payoff of approving by digest, and it only pays off if the UI
 * does the hard half. Forge refused because what it recomputed is not what was
 * approved — somebody else deployed, a new build landed, drift appeared — and
 * the tempting response is "that didn't work, try again". That would re-approve
 * a change set nobody has read, which is the exact failure the digest exists to
 * prevent.
 *
 * So this component never offers a retry of the old approval. It shows WHAT
 * MOVED, renders the recomputed plan as the thing to approve now, and requires
 * a fresh decision — including fresh acknowledgements, since a newly-appeared
 * stop-class finding was by definition not covered by the previous tick.
 */

import { GitCompareArrows } from "lucide-react";

import { CardInset } from "@/components/forge-ui/card";
import {
  diffPlans,
  planChanged,
  type DeployPlan,
  type DeployPlanFinding,
} from "@/services/forge/deployPlan";

export interface PlanStaleNoticeProps {
  /** The plan that was approved — what the operator read. */
  approved: DeployPlan | null | undefined;
  /** The plan forge recomputed and refused against. */
  current: DeployPlan | null | undefined;
}

export function PlanStaleNotice({ approved, current }: PlanStaleNoticeProps) {
  const change = diffPlans(approved, current);

  return (
    <section
      data-testid="plan-stale"
      className="space-y-2 rounded-lg border border-warning/50 bg-warning/10 px-3 py-2"
    >
      <p className="flex items-center gap-1.5 text-xs font-medium text-foreground">
        <GitCompareArrows className="h-3.5 w-3.5 shrink-0 text-warning" aria-hidden="true" />
        This changed while you were reading it, so nothing was deployed.
      </p>

      {/* The environment is UNTOUCHED. Said first and plainly, because the
          reader's immediate question is whether a half-deploy happened. */}
      <p className="text-2xs text-muted-foreground">
        Your environment is exactly as it was. Something moved underneath this plan —
        another deploy, a new build, or a change made directly to the environment — so the
        changes listed below are not the ones you approved.
      </p>

      {planChanged(change) ? (
        <CardInset data-testid="plan-stale-diff" className="space-y-1.5">
          <p className="text-2xs font-medium text-foreground">What is different now</p>

          {change.releaseChanged && (
            <p className="text-2xs text-muted-foreground">
              It would now ship{" "}
              <span className="font-mono text-foreground">
                {change.releaseChanged.to || "a different release"}
              </span>
              {change.releaseChanged.from && (
                <>
                  {" "}
                  instead of{" "}
                  <span className="font-mono text-foreground">{change.releaseChanged.from}</span>
                </>
              )}
              .
            </p>
          )}

          {change.bundleChanged && (
            <p className="text-2xs text-muted-foreground">
              The build it would deploy is not the one that was planned.
            </p>
          )}

          {change.driftChanged && (
            <p className="text-2xs text-muted-foreground">
              {change.driftChanged.to
                ? "What is running has drifted from what was last deployed since this was planned."
                : "The drift that was there when this was planned is gone."}
            </p>
          )}

          {/* NEW IRREVERSIBLE CHANGES GET THEIR OWN LINE, above the general
              added/removed lists. A deletion that appeared after approval is
              the single most important thing on this screen. */}
          {change.newStopCodes.length > 0 && (
            <p data-testid="plan-stale-new-stop" className="text-2xs font-medium text-destructive">
              It now destroys something it did not before. You will be asked to accept that
              separately.
            </p>
          )}

          <FindingDelta label="Now also" findings={change.added} testId="plan-stale-added" />
          <FindingDelta label="No longer" findings={change.removed} testId="plan-stale-removed" />
        </CardInset>
      ) : (
        // The digest differed but nothing the digest covers looks different to
        // us. Not swallowed: forge's comparison is authoritative and ours is a
        // rendering, so the honest line is "it moved, we cannot show you how".
        <p data-testid="plan-stale-opaque" className="text-2xs text-muted-foreground">
          The details of what moved could not be shown. Read the plan below before approving
          it.
        </p>
      )}

      <p className="text-2xs text-muted-foreground">
        Read the plan below — it is the current one — and approve it if it is what you want.
      </p>
    </section>
  );
}

function FindingDelta({
  label,
  findings,
  testId,
}: {
  label: string;
  findings: DeployPlanFinding[];
  testId: string;
}) {
  if (findings.length === 0) return null;
  return (
    <div data-testid={testId} className="space-y-0.5">
      <p className="text-2xs font-medium text-foreground">{label}</p>
      <ul className="space-y-0.5">
        {findings.map((finding, index) => (
          <li key={index} className="text-2xs text-muted-foreground">
            {finding.subject && <span className="font-mono text-foreground">{finding.subject}</span>}
            {finding.detail && (
              <span>
                {finding.subject ? " — " : ""}
                {finding.detail}
              </span>
            )}
            {!finding.subject && !finding.detail && (
              <span className="font-mono text-foreground">{finding.code ?? "change"}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
