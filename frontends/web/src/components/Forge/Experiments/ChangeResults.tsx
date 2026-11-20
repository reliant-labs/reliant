// Copyright (c) 2025 Reliant Labs

/**
 * Results of the open run: the ReleaseComparison model. The control plane
 * computes everything — rates, intervals, lift, the sample-ratio check and
 * the verdict (forge pkg/experimentstats) — and this panel only renders it.
 * Nothing here is computed from raw counts, so the panel can never disagree
 * with the verdict printed in it.
 *
 * Until the control plane serves GetChangeReadout, ChangesApi has no
 * getChangeReadout and the panel says results are coming, with no numbers.
 *
 * Colour follows the brand roles: only a treatment that is better is a
 * success (green dot); worse is danger; inconclusive and no data are
 * neutral, never a quiet "almost green".
 */

import { useId } from "react";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import { useChangeReadout } from "@/hooks/changes-queries";
import type { Change, ChangeReadout, ChangeReadoutVerdict } from "@/services/controlPlane/changes/changesTypes";
import { describeChangeError } from "@/services/controlPlane/changes/errors";

const VERDICTS: Record<ChangeReadoutVerdict, { label: string; tone: StatusTone }> = {
  treatment_better: { label: "The new version is better", tone: "success" },
  treatment_worse: { label: "The new version is worse", tone: "danger" },
  inconclusive: { label: "Not enough evidence yet", tone: "neutral" },
  no_data: { label: "No one has been counted yet", tone: "neutral" },
  srm_failed: { label: "The split looks wrong, so these results can't be trusted", tone: "warning" },
};

const INCONCLUSIVE: Record<string, string> = {
  below_min_sample: "Fewer people than the minimum you set have been counted.",
  window_not_complete: "Enough people are in, but most haven't had the full window to act yet.",
  interval_spans_zero: "The difference could still go either way.",
};

const pct = (rate: number | null) => (rate === null ? "—" : `${(rate * 100).toFixed(1)}%`);
const pp = (v: number) => `${v >= 0 ? "+" : ""}${(v * 100).toFixed(1)} pts`;

export function ChangeResults({ change }: { change: Change }) {
  const id = useId();
  const readout = useChangeReadout(change);
  const isExperiment = change.mode === "experiment" || change.currentRun?.kind === "experiment";

  let body;
  if (readout.unavailable) {
    body = (
      <p className="text-sm text-ink-muted" data-testid="results-pending-service">
        Results appear once the readout service ships. People are already being counted from the moment an experiment launches, so nothing is lost meanwhile.
      </p>
    );
  } else if (!change.currentRun || !isExperiment) {
    body = <p className="text-sm text-ink-muted">Results appear here while an experiment is running.</p>;
  } else if (readout.isLoading) {
    body = <SkeletonLoader variant="text" count={3} />;
  } else if (readout.error) {
    body = <p className="text-sm text-ink-muted">{describeChangeError(readout.error)}</p>;
  } else if (readout.data) {
    body = <ReadoutView readout={readout.data} />;
  }

  return (
    <Card padding="md" aria-labelledby={`${id}-title`} className="space-y-3" data-testid="change-results">
      <h2 id={`${id}-title`} className="text-section text-ink">
        Results
      </h2>
      {body}
    </Card>
  );
}

function ReadoutView({ readout }: { readout: ChangeReadout }) {
  const verdict = VERDICTS[readout.verdict] ?? VERDICTS.inconclusive;
  return (
    <div className="space-y-3">
      <div className="space-y-0.5">
        <Status tone={verdict.tone} label={verdict.label} />
        {readout.verdict === "inconclusive" && readout.inconclusiveReason && <p className="text-meta text-ink-muted">{INCONCLUSIVE[readout.inconclusiveReason]}</p>}
        <p className="text-meta text-ink-muted">
          Decided at {Math.round(readout.confidence * 100)}% confidence once each version has
          {readout.minSamplePerArm > 0 ? ` ${readout.minSamplePerArm.toLocaleString()} people` : " enough people"}, counting what people do within {readout.windowHours || 168} hours.
          {readout.conflictedUnits > 0 ? ` ${readout.conflictedUnits.toLocaleString()} people saw more than one version and aren't counted.` : ""}
        </p>
      </div>
      <table className="w-full text-left text-ui" aria-label="Results by version">
        <thead className="text-meta text-ink-muted">
          <tr className="border-b border-border">
            <th scope="col" className="py-1.5 pr-4 font-medium">Version</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">People</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">Counted</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">Success</th>
            <th scope="col" className="py-1.5 font-medium">Range</th>
          </tr>
        </thead>
        <tbody className="tabular-nums">
          {readout.arms.map((arm) => (
            <tr key={arm.variant} className="border-b border-border-subtle last:border-0">
              <td className="py-1.5 pr-4 font-mono text-ink">{arm.variant}</td>
              <td className="py-1.5 pr-4 text-ink">{arm.assigned.toLocaleString()}</td>
              <td className="py-1.5 pr-4 text-ink-muted">{arm.windowComplete.toLocaleString()}</td>
              <td className="py-1.5 pr-4 text-ink">{pct(arm.rate)}</td>
              <td className="py-1.5 text-ink-muted">{arm.rateInterval ? `${pct(arm.rateInterval.low)} – ${pct(arm.rateInterval.high)}` : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {readout.comparisons.map((c) => (
        <p key={c.variant} className="text-meta text-ink-muted">
          {c.variant} vs {c.control}: {c.absoluteLift === null ? "—" : pp(c.absoluteLift)}
          {c.differenceInterval ? ` (between ${pp(c.differenceInterval.low)} and ${pp(c.differenceInterval.high)})` : ""}
        </p>
      ))}
    </div>
  );
}
