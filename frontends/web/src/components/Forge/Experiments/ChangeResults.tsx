// Copyright (c) 2025 Reliant Labs

/**
 * Results of the change's experiment run (ChangesService GetChangeReadout):
 * the ReleaseComparison model. The control plane computes everything — rates
 * and their intervals, lift, the sample-ratio check and the verdict (forge
 * pkg/experimentstats) — and this panel only renders it, so it can never
 * disagree with the verdict printed in it.
 *
 * Colour follows the brand roles. Only "the new version is better" is a
 * success, and only its words and icon are green; "worse" is danger; no data
 * and inconclusive are neutral, never an "almost green". A failed sample-ratio
 * check is a warning that the numbers cannot be trusted, and the lift is not
 * shown as a result at all. The p-value is a small detail, never the headline:
 * a fixed-horizon test is decided once, at the pre-registered sample size.
 *
 * Every read is a ClickHouse query on the server, so the panel never polls;
 * "Refresh" reads again on request.
 */

import { useId } from "react";
import { AlertTriangle, CheckCircle2, CircleDashed, RefreshCw, XCircle } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useChangeReadout } from "@/hooks/changes-queries";
import type { Change, ChangeReadout, ChangeReadoutArm, ChangeReadoutVerdict } from "@/services/controlPlane/changes/changesTypes";
import { describeChangeError, readoutErrorKind, type ReadoutErrorKind } from "@/services/controlPlane/changes/errors";

import { tokenColor } from "../Observability/charts/chartPalette";

const VERDICTS: Record<ChangeReadoutVerdict, { label: string; icon: typeof CheckCircle2; iconClass: string; textClass: string }> = {
  treatment_better: { label: "The new version is better", icon: CheckCircle2, iconClass: "text-success", textClass: "text-success-ink" },
  treatment_worse: { label: "The new version is worse", icon: XCircle, iconClass: "text-danger", textClass: "text-danger-ink" },
  inconclusive: { label: "Not enough evidence yet", icon: CircleDashed, iconClass: "text-ink-muted", textClass: "text-ink" },
  no_data: { label: "No one has been counted yet", icon: CircleDashed, iconClass: "text-ink-muted", textClass: "text-ink" },
  srm_failed: { label: "These results can't be trusted", icon: AlertTriangle, iconClass: "text-warning", textClass: "text-warning-ink" },
};

const REASONS: Record<string, string> = {
  below_min_sample: "Some versions have fewer people than the minimum you set. Keep the experiment running.",
  window_not_complete: "Enough people are in, but too few have had the full time window to act yet.",
  interval_spans_zero: "There's no clear difference between the versions.",
};

const END_REASONS: Record<string, string> = {
  now: "now",
  left_experiment: "when the change stopped being an experiment",
  run_ended: "when the run ended",
};

/** What the panel says when there is no readout to draw. */
const REFUSALS: Record<Exclude<ReadoutErrorKind, "other">, string> = {
  "not-deployed": "Results appear once the readout service ships. People are already being counted from the moment an experiment launches, so nothing is lost meanwhile.",
  "no-experiment": "Results appear here once you launch an experiment.",
  "not-experiment": "This run is a rollout, so there's nothing to compare. Launch an experiment to measure a difference.",
  "no-metric": "This experiment was launched without a success metric, so there's nothing to measure. Pick one under \u201cWho gets it?\u201d and relaunch.",
  "plan-invalid": "This experiment's split doesn't have a control and a new version to compare. Fix the split and relaunch.",
  "no-observability": "Results need observability, which isn't set up for this organization yet or didn't answer. Try again in a moment.",
  denied: "You don't have access to results here. Reading them needs the obs:read permission.",
};

const pct = (rate: number) => `${(rate * 100).toFixed(1)}%`;
const points = (v: number) => `${v >= 0 ? "+" : "\u2212"}${Math.abs(v * 100).toFixed(1)}`;
const when = (d: Date | null) => (d ? d.toLocaleString() : "the start of the run");

export function ChangeResults({ change }: { change: Change }) {
  const id = useId();
  const readout = useChangeReadout(change);

  let body;
  if (readout.isLoading) {
    body = <SkeletonLoader variant="text" count={3} />;
  } else if (readout.error) {
    const kind = readoutErrorKind(readout.error);
    body = (
      <p className="text-sm text-ink-muted" data-testid={`results-${kind}`}>
        {kind === "other" ? describeChangeError(readout.error) : REFUSALS[kind]}
      </p>
    );
  } else if (readout.data) {
    body = <ReadoutView readout={readout.data} />;
  }

  return (
    <Card padding="md" aria-labelledby={`${id}-title`} className="space-y-3" data-testid="change-results">
      <div className="flex items-center gap-2">
        <h2 id={`${id}-title`} className="text-section text-ink">
          Results
        </h2>
        {readout.data && (
          <Button variant="ghost" size="xs" leftIcon={<RefreshCw />} onClick={() => void readout.refetch()} loading={readout.isFetching} className="ml-auto">
            Refresh
          </Button>
        )}
      </div>
      {body}
    </Card>
  );
}

function ReadoutView({ readout }: { readout: ChangeReadout }) {
  const verdict = VERDICTS[readout.verdict];
  const Icon = verdict.icon;
  const srmFailed = readout.verdict === "srm_failed";
  const metric = readout.metric;
  const scaleMax = Math.max(0.01, ...readout.arms.map((arm) => arm.ciHigh)) * 1.1;

  return (
    <div className="space-y-4">
      <div className="space-y-1" data-testid="results-verdict" data-verdict={readout.verdict}>
        <p className={`flex items-center gap-2 text-ui font-medium ${verdict.textClass}`}>
          <Icon className={`h-4 w-4 shrink-0 ${verdict.iconClass}`} aria-hidden="true" />
          {verdict.label}
          {readout.verdict === "treatment_better" && readout.bestVariant && <span className="font-mono">({readout.bestVariant})</span>}
        </p>
        {srmFailed && (
          <p className="text-sm text-ink">
            The number of people in each version doesn't match the planned split, so assignment or its recording is broken. Don't act on any difference below until that is fixed.
            {readout.srm.unplannedVariants.length > 0 && ` People were assigned versions the split doesn't have: ${readout.srm.unplannedVariants.join(", ")}.`}
          </p>
        )}
        {readout.verdict === "inconclusive" && readout.reason && <p className="text-sm text-ink-muted">{REASONS[readout.reason]}</p>}
        <p className="text-meta text-ink-muted" data-testid="results-window">
          {`Counting from ${when(readout.readoutStart)} to ${END_REASONS[readout.readoutEndReason] ?? "now"}`}
          {readout.readoutEndReason && readout.readoutEndReason !== "now" && readout.readoutEnd ? ` (${readout.readoutEnd.toLocaleString()})` : ""}
          {". "}
          <span className="font-medium text-ink">{readout.final ? "Final." : "Still collecting."}</span>
        </p>
        {metric && (
          <p className="text-meta text-ink-muted">
            {`Success: `}
            <span className="font-mono">{metric.event}</span>
            {metric.prop ? ` where ${metric.prop} = ${metric.propValue}` : ""}
            {` within ${metric.windowHours} hours, at least ${metric.minSamplePerArm.toLocaleString()} people per version, ${Math.round((1 - readout.alpha) * 100)}% confidence.`}
          </p>
        )}
      </div>

      <table className="w-full text-left text-ui" aria-label="Results by version">
        <thead className="text-meta text-ink-muted">
          <tr className="border-b border-border">
            <th scope="col" className="py-1.5 pr-4 font-medium">Version</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">Assigned</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">Window complete</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">Converted</th>
            <th scope="col" className="py-1.5 pr-4 font-medium">Rate</th>
            <th scope="col" className="py-1.5 font-medium">Still in window</th>
          </tr>
        </thead>
        <tbody className="tabular-nums">
          {readout.arms.map((arm) => (
            <tr key={arm.variant} className="border-b border-border-subtle last:border-0" data-testid={`results-arm-${arm.variant}`}>
              <td className="py-1.5 pr-4">
                <span className="font-mono text-ink">{arm.variant}</span>
                {arm.weightBp > 0 && <span className="ml-1.5 text-meta text-ink-muted">{`${arm.weightBp / 100}% planned`}</span>}
              </td>
              <td className="py-1.5 pr-4 text-ink">{arm.assigned.toLocaleString()}</td>
              <td className="py-1.5 pr-4 text-ink">{arm.windowComplete.toLocaleString()}</td>
              <td className="py-1.5 pr-4 text-ink">{arm.conversions.toLocaleString()}</td>
              <td className="py-1.5 pr-4">
                <RateCell arm={arm} scaleMax={scaleMax} />
              </td>
              <td className="py-1.5 text-ink-muted">{arm.pending.toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>

      {!srmFailed && readout.comparisons.length > 0 && (
        <ul className="space-y-1" aria-label="Differences" data-testid="results-comparisons">
          {readout.comparisons.map((c) => (
            <li key={c.treatment} className="text-ui text-ink">
              <span className="font-mono">{c.treatment}</span>
              {` vs `}
              <span className="font-mono">{c.control}</span>
              {`: ${points(c.absLift)} points (likely between ${points(c.diffCiLow)} and ${points(c.diffCiHigh)})`}
              {c.relLift !== null && `, ${points(c.relLift)}% relative`}
              <span className="ml-2 text-meta text-ink-muted">{`p = ${c.pValue.toPrecision(2)}`}</span>
            </li>
          ))}
        </ul>
      )}

      <div className="space-y-1 text-meta text-ink-muted">
        {readout.srm.skipped && !srmFailed && <p>The split check hasn't run yet: too few people so far.</p>}
        {readout.conflicted > 0 && (
          <p data-testid="results-conflicted">{`${readout.conflicted.toLocaleString()} ${readout.conflicted === 1 ? "person was" : "people were"} shown more than one version and aren't counted in any.`}</p>
        )}
        {readout.undeclaredVariants.length > 0 && <p>{`Some people were assigned versions the split doesn't have: ${readout.undeclaredVariants.join(", ")}.`}</p>}
        {readout.truncatedByRetention && (
          <p data-testid="results-truncated">Counting starts later than the experiment did: events older than their retention period are gone, so the earliest people aren't included.</p>
        )}
        {readout.horizonNote && <p data-testid="results-horizon">{readout.horizonNote}</p>}
      </div>
    </div>
  );
}

/** The rate, and its interval as a bar on a scale the versions share. */
function RateCell({ arm, scaleMax }: { arm: ChangeReadoutArm; scaleMax: number }) {
  const at = (v: number) => `${Math.min(100, Math.max(0, (v / scaleMax) * 100))}%`;
  const none = arm.windowComplete === 0;
  return (
    <div className="flex items-center gap-2">
      <span className="w-12 text-ink">{none ? "\u2014" : pct(arm.rate)}</span>
      {!none && (
        <span
          className="relative h-1.5 w-24 rounded-full bg-surface-sunken"
          role="img"
          aria-label={`${pct(arm.rate)}, likely between ${pct(arm.ciLow)} and ${pct(arm.ciHigh)}`}
        >
          <span className="absolute inset-y-0 rounded-full" style={{ left: at(arm.ciLow), width: `calc(${at(arm.ciHigh)} - ${at(arm.ciLow)})`, background: tokenColor("--chart-1", 0.45) }} />
          <span className="absolute top-1/2 h-2.5 w-0.5 -translate-y-1/2 rounded-full" style={{ left: at(arm.rate), background: tokenColor("--chart-1") }} />
        </span>
      )}
      {!none && <span className="text-meta text-ink-muted">{`${pct(arm.ciLow)}\u2013${pct(arm.ciHigh)}`}</span>}
    </div>
  );
}
