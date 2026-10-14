// Copyright (c) 2025 Reliant Labs

/**
 * Delivery: how often the project ships, how often a release has to be rolled
 * back, and how long that takes — the DORA metrics, computed by control-plane
 * over the release ledger (DeliveryInsightsService.GetDoraMetrics) — and the
 * promotions themselves, each a way into a before/after comparison.
 *
 * ── SAY WHAT IS MEASURED, AND WHAT ISN'T ─────────────────────────────────────
 *
 * Every figure carries the server's own definition, shown verbatim: a metric
 * whose meaning the reader has to guess is not a metric. And a figure the
 * ledger cannot produce is shown as unknown, never as zero. Lead time is the
 * case today: the ledger records the commit a release was built from but not
 * when it was committed, so there is nothing to measure from. The card says
 * so, with how many deployments that leaves unmeasured, rather than hiding
 * the metric or inventing one.
 *
 * Time to restore is measured from rollbacks in the ledger. An alert-based
 * figure (from alert history) is reserved on the response; the card shows it
 * beside the ledger's when it arrives (`DoraMetrics.alertTimeToRestore`).
 */

import { useId, useMemo, type ReactNode } from "react";
import { HelpCircle, Rocket } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useDoraMetrics, useReleases } from "@/hooks/observability-queries";
import type { DoraMetrics, Promotion, TimeToRestore } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import {
  DORA_WINDOW_DAYS,
  DORA_WINDOW_LABELS,
  DORA_WINDOWS,
  FAILURE_HOURS_CHOICES,
  deliveryScopeOf,
  formatPerDay,
  formatSpan,
  type DoraWindow,
} from "./deliveryScope";
import { DeliveryError } from "./DeliveryStates";
import { formatPercent, formatWhen } from "./format";
import type { ObsEnvironment } from "./obsPlace";
import type { ObsNavigate } from "./obsSearch";
import { ObsLoadingRows } from "./ObsStates";
import { PromotionRow } from "./ReleaseStrip";

const SELECT =
  "h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export interface DeliveryViewProps {
  scope: ObsScope;
  environments: readonly ObsEnvironment[];
  subject: string;
  window: DoraWindow;
  failureHours: number;
  /** The project area: drop the environment picker and any environment filter. Absent on an env page. */
  onShowEveryEnvironment?: () => void;
  onCompare: (promotion: Promotion) => void;
  navigate: ObsNavigate;
}

export function DeliveryView({ scope, environments, subject, window, failureHours, onShowEveryEnvironment, onCompare, navigate }: DeliveryViewProps) {
  const delivery = useMemo(() => deliveryScopeOf(scope, environments), [scope, environments]);
  const days = DORA_WINDOW_DAYS[window];
  const dora = useDoraMetrics(delivery, days, failureHours);
  const releases = useReleases(delivery, `days:${days}`, () => ({ startMs: Date.now() - days * 86_400_000, endMs: Date.now() }));
  const windowId = useId();
  const hoursId = useId();

  const controls = (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm" data-testid="obs-delivery-controls">
      <label htmlFor={windowId} className="sr-only">
        Window
      </label>
      <select
        id={windowId}
        value={window}
        onChange={(e) => navigate({ doraWindow: e.target.value === "30d" ? undefined : (e.target.value as DoraWindow) }, { replace: true })}
        className={SELECT}
        data-testid="obs-delivery-window"
      >
        {DORA_WINDOWS.map((w) => (
          <option key={w} value={w}>
            {DORA_WINDOW_LABELS[w]}
          </option>
        ))}
      </select>
      <span className="inline-flex flex-wrap items-center gap-2 text-muted-foreground">
        <label htmlFor={hoursId}>A deployment failed if it was rolled back within</label>
        <select
          id={hoursId}
          value={failureHours}
          onChange={(e) => {
            const hours = Number(e.target.value);
            navigate({ failureHours: hours === 24 ? undefined : hours }, { replace: true });
          }}
          className={SELECT}
          data-testid="obs-delivery-failure-hours"
        >
          {[...new Set<number>([...FAILURE_HOURS_CHOICES, failureHours])]
            .sort((a, b) => a - b)
            .map((h) => (
              <option key={h} value={h}>
                {h === 168 ? "7 days (168 hours)" : `${h} hour${h === 1 ? "" : "s"}`}
              </option>
            ))}
        </select>
      </span>
    </div>
  );

  if (!delivery) {
    return (
      <div className="space-y-4" data-testid="obs-delivery" data-state="excluded">
        {controls}
        <p role="status" className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground" data-testid="obs-delivery-excluded">
          The filters exclude every environment, so there are no deployments to measure. Remove the environment filter to see them.
        </p>
      </div>
    );
  }
  if (dora.isLoading) {
    return (
      <div className="space-y-4" data-testid="obs-delivery" data-state="loading">
        {controls}
        <ObsLoadingRows testId="obs-delivery-loading" rows={4} />
      </div>
    );
  }
  if (dora.error || !dora.data) {
    return (
      <div className="space-y-4" data-testid="obs-delivery" data-state="error">
        {controls}
        <DeliveryError error={dora.error} subject={subject} onShowEveryEnvironment={onShowEveryEnvironment} onRetry={() => void dora.refetch()} retrying={dora.isFetching} />
      </div>
    );
  }

  const m = dora.data;
  const df = m.deploymentFrequency;
  const promoted = df.deployments + df.rollbacks + df.reapplies;
  const list = releases.data?.promotions ?? [];
  if (promoted === 0 && !releases.isLoading && list.length === 0) {
    return (
      <div className="space-y-4" data-testid="obs-delivery" data-state="empty">
        {controls}
        <NoReleases subject={subject} window={window} environments={m.environmentCount} onWiden={window !== "90d" ? () => navigate({ doraWindow: "90d" }, { replace: true }) : undefined} />
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="obs-delivery" data-state="ready">
      {controls}
      <p className="text-xs text-muted-foreground" data-testid="obs-delivery-span">
        {m.environmentCount} environment{m.environmentCount === 1 ? "" : "s"} · {formatWhen(m.windowStartMs)} – {formatWhen(m.windowEndMs)} · computed from the
        release ledger, per environment, then summed.
      </p>
      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
        <DeploymentFrequencyCard metrics={m} days={days} />
        <LeadTimeCard metrics={m} />
        <ChangeFailureCard metrics={m} />
        <RestoreCard metrics={m} />
      </div>
      <Card padding="none" data-testid="obs-delivery-releases">
        <div className="border-b border-border/60 px-4 py-3">
          <h3 className="text-sm font-medium text-foreground">Promotions in this window</h3>
          <p className="text-xs text-muted-foreground">Newest first. Compare any of them with the window before it.</p>
        </div>
        {releases.isLoading ? (
          <div className="p-4">
            <ObsLoadingRows rows={3} testId="obs-delivery-releases-loading" />
          </div>
        ) : releases.error ? (
          <div className="p-4">
            <DeliveryError error={releases.error} subject={subject} onRetry={() => void releases.refetch()} retrying={releases.isFetching} />
          </div>
        ) : (
          <>
            {releases.data?.truncated && (
              <p className="px-4 pt-2 text-xs text-warning" role="status" data-testid="obs-delivery-releases-truncated">
                More promotions happened in this window than one request returns: only the newest {list.length} are listed. The metrics above
                count all of them.
              </p>
            )}
            <ul className="max-h-[28rem] divide-y divide-border/50 overflow-y-auto text-xs" aria-label="Promotions, newest first">
              {[...list].reverse().map((p) => (
                <PromotionRow key={p.promotionId} promotion={p} onCompare={() => onCompare(p)} />
              ))}
            </ul>
          </>
        )}
      </Card>
    </div>
  );
}

function NoReleases({ subject, window, environments, onWiden }: { subject: string; window: DoraWindow; environments: number; onWiden?: () => void }) {
  return (
    <div data-testid="obs-delivery-empty" role="status" className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-5">
      <Rocket className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-foreground">
          Nothing was promoted to <span className="font-mono">{subject}</span> in the {DORA_WINDOW_LABELS[window].toLowerCase()}
        </p>
        <div className="space-y-1.5 text-xs text-muted-foreground">
          <p>
            Delivery metrics are counted from the release ledger: every <code className="font-mono">forge env deploy &lt;env&gt;</code> and every
            promotion or rollback in Reliant records one. With none in this window there is nothing to measure — not a frequency of zero,
            just no data.
          </p>
          <p>
            {environments} environment{environments === 1 ? " is" : "s are"} in view.
          </p>
        </div>
        {onWiden && (
          <div className="pt-1">
            <Button variant="outline" size="sm" onClick={onWiden} data-testid="obs-delivery-widen">
              Look at the last 90 days
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

// ── The four metrics ─────────────────────────────────────────────────────────

function MetricCard({
  title,
  value,
  state,
  testId,
  definition,
  children,
}: {
  title: string;
  value: string;
  /** "measured", "unknown", "partial", "none": what the figure is, for the reader and the tests. */
  state: string;
  testId: string;
  definition: string;
  children: ReactNode;
}) {
  const definitionId = useId();
  return (
    <Card padding="md" data-testid={testId} data-state={state} aria-describedby={definition ? definitionId : undefined}>
      <div className="flex h-full flex-col gap-2">
        <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</h3>
        <p
          className={cn(
            "text-2xl font-semibold tabular-nums",
            state === "unknown" ? "text-muted-foreground" : state === "none" ? "text-muted-foreground" : "text-foreground"
          )}
          data-testid={`${testId}-value`}
        >
          {value}
        </p>
        <div className="space-y-1 text-xs text-muted-foreground">{children}</div>
        {definition && (
          <div className="mt-auto border-t border-border/60 pt-2">
            <p className="mb-0.5 inline-flex items-center gap-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
              <HelpCircle className="h-3 w-3" aria-hidden="true" />
              How it&apos;s measured
            </p>
            <p id={definitionId} className="text-2xs leading-relaxed text-muted-foreground" data-testid={`${testId}-definition`}>
              {definition}
            </p>
          </div>
        )}
      </div>
    </Card>
  );
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

function DeploymentFrequencyCard({ metrics, days }: { metrics: DoraMetrics; days: number }) {
  const df = metrics.deploymentFrequency;
  const none = df.deployments === 0;
  return (
    <MetricCard title="Deployment frequency" value={formatPerDay(df.perDay)} state={none ? "none" : "measured"} testId="obs-dora-frequency" definition={df.definition}>
      <p>
        {plural(df.deployments, "deployment")} in {days} days
      </p>
      {(df.rollbacks > 0 || df.reapplies > 0) && (
        <p>
          Not counted: {[df.rollbacks ? plural(df.rollbacks, "rollback") : "", df.reapplies ? plural(df.reapplies, "re-apply", "re-applies") : ""].filter(Boolean).join(", ")}.
        </p>
      )}
    </MetricCard>
  );
}

/** The honest lead-time state: unknown with how much is unknown, partial, measured, or no deployments at all. */
export function leadTimeState(lt: DoraMetrics["leadTime"]): "unknown" | "partial" | "measured" | "none" {
  if (lt.measuredChanges === 0) return lt.unknownChanges > 0 ? "unknown" : "none";
  return lt.unknownChanges > 0 ? "partial" : "measured";
}

function LeadTimeCard({ metrics }: { metrics: DoraMetrics }) {
  const lt = metrics.leadTime;
  const state = leadTimeState(lt);
  const value = state === "unknown" ? "Unknown" : state === "none" ? "—" : formatSpan(lt.medianMs);
  return (
    <MetricCard title="Lead time for changes" value={value} state={state} testId="obs-dora-lead-time" definition={lt.definition}>
      {state === "unknown" && (
        <>
          <p className="text-foreground" data-testid="obs-dora-lead-time-unknown">
            No deployment in this window has a recorded commit time: {plural(lt.unknownChanges, "deployment")} across{" "}
            {plural(lt.unknownReleases, "release")}.
          </p>
          <p>
            The release ledger keeps the commit each release was built from, not when that commit was made, so there is nothing to measure
            from. Nothing is estimated: unknown is not zero.
          </p>
          <p>Lead time starts reporting once releases record their commit time.</p>
        </>
      )}
      {state === "none" && <p>No deployments in this window.</p>}
      {(state === "measured" || state === "partial") && (
        <>
          <p>
            Median over {plural(lt.measuredChanges, "deployment")} · mean {formatSpan(lt.meanMs)}
          </p>
          {state === "partial" && (
            <p className="text-foreground" data-testid="obs-dora-lead-time-unknown">
              {plural(lt.unknownChanges, "more deployment")} ({plural(lt.unknownReleases, "release")}) recorded no commit time and are left out.
            </p>
          )}
        </>
      )}
    </MetricCard>
  );
}

function ChangeFailureCard({ metrics }: { metrics: DoraMetrics }) {
  const cfr = metrics.changeFailureRate;
  const n = metrics.failureWindowHours;
  return (
    <MetricCard
      title="Change failure rate"
      value={cfr.rate == null ? "—" : formatPercent(cfr.rate)}
      state={cfr.rate == null ? "none" : "measured"}
      testId="obs-dora-failure-rate"
      definition={cfr.definition}
    >
      {cfr.rate == null ? (
        <p>No deployments, so none could fail.</p>
      ) : (
        <p>
          {cfr.failedChanges} of {plural(cfr.changes, "deployment")} rolled back within {n === 168 ? "7 days" : plural(n, "hour")}
        </p>
      )}
    </MetricCard>
  );
}

function restoreLine(t: TimeToRestore): string {
  return `median over ${plural(t.failures, "failed change")} · mean ${formatSpan(t.meanMs)}`;
}

function RestoreCard({ metrics }: { metrics: DoraMetrics }) {
  const t = metrics.timeToRestore;
  const alerts = metrics.alertTimeToRestore;
  const none = t.failures === 0;
  return (
    <MetricCard title="Time to restore" value={none ? "—" : formatSpan(t.medianMs)} state={none ? "none" : "measured"} testId="obs-dora-restore" definition={t.definition}>
      {none ? <p>No deployment failed in this window, so there was nothing to restore.</p> : <p>{restoreLine(t)}</p>}
      <p data-testid="obs-dora-restore-source">
        {t.source === "release_ledger" || !t.source ? "Measured from rollbacks in the release ledger." : `Source: ${t.source}.`}
      </p>
      {alerts && (
        <div className="space-y-0.5 border-t border-border/60 pt-1.5" data-testid="obs-dora-restore-alerts">
          <p className="font-medium text-foreground">From alerts: {alerts.failures === 0 ? "nothing resolved" : formatSpan(alerts.medianMs)}</p>
          {alerts.failures > 0 && <p>{restoreLine(alerts)}</p>}
          {alerts.definition && <p className="text-2xs">{alerts.definition}</p>}
        </div>
      )}
    </MetricCard>
  );
}
