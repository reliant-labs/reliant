// Copyright (c) 2025 Reliant Labs

/**
 * Usage: this month's stored telemetry against the org's included allowance,
 * what it costs beyond it, and what the gateway is doing about it.
 *
 * It is per ORGANIZATION, not per environment (the allowance is per org), and a
 * plain read of what the hourly meter wrote: it works when the tenant store is
 * unreachable and lags real time by up to about an hour, which it says.
 *
 * The same panel is rendered in the observability console and in Settings →
 * Billing, so the notice text "Settings → Billing" lands on the same figures.
 */

import { AlertTriangle, Info } from "lucide-react";

import Card, { CardHeader, CardInset } from "@/components/forge-ui/card";
import { useObservabilityUsage } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import type { ObservabilityUsage, UsageSignal } from "@/services/controlPlane/observability/types";

import { formatBytes, formatPercent } from "./format";
import { ObsError, ObsLoadingRows } from "./ObsStates";
import {
  browserCapHit,
  formatDollars,
  formatRate,
  measuredThroughLabel,
  usageNotice,
  usedFraction,
  type NoticeTone,
} from "./usageModel";

const SIGNAL_LABELS: Record<UsageSignal, string> = { logs: "Logs", traces: "Traces", metrics: "Metrics", profiles: "Profiles" };

const NOTICE_TONES: Record<NoticeTone, { frame: string; icon: string }> = {
  info: { frame: "border-border/60 bg-background", icon: "text-muted-foreground" },
  warning: { frame: "border-warning/40 bg-background", icon: "text-warning-ink" },
  destructive: { frame: "border-destructive/40 bg-background", icon: "text-destructive-ink" },
};

function barTone(u: ObservabilityUsage): string {
  if (u.state === "over_150" || u.state === "over_100") return "bg-destructive";
  if (usedFraction(u) >= 0.8 || u.usageState === "over_100" || u.usageState === "over_150") return "bg-warning";
  return "bg-primary";
}

export function UsageView({ orgId, envName = "" }: { orgId: string | null; envName?: string }) {
  const usage = useObservabilityUsage(orgId);

  if (!orgId || usage.isLoading) return <ObsLoadingRows rows={4} testId="obs-usage-loading" />;
  if (usage.error && !usage.data) {
    return <ObsError error={usage.error} envName={envName} onRetry={() => void usage.refetch()} retrying={usage.isFetching} />;
  }
  const u = usage.data;
  if (!u) return null;
  return <UsagePanel usage={u} />;
}

/** Presentational and prop-driven, so every state is renderable from a fixture. */
export function UsagePanel({ usage: u }: { usage: ObservabilityUsage }) {
  const notice = usageNotice(u);
  const fill = Math.min(100, Math.max(0, usedFraction(u) * 100));
  const periodEnd = new Date(u.periodEndMs - 1).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const projectedOver = !u.exempt && u.projectedOverageCents > 0;
  const signals = u.bySignal;
  const topServices = u.byService.filter((s) => s.billedBytes > 0).slice(0, 8);

  return (
    <div className="space-y-4" data-testid="obs-usage">
      <Card>
        <CardHeader
          title="Observability usage"
          description={`Stored telemetry this month, against your ${formatBytes(u.allowanceBytes)} included. Resets after ${periodEnd}.`}
        />
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <p className="text-2xl font-semibold tabular-nums text-foreground" data-testid="obs-usage-used">
            {formatBytes(u.usedBytes)}
            <span className="ml-1 text-sm font-normal text-muted-foreground">of {formatBytes(u.allowanceBytes)}</span>
          </p>
          <p className="text-sm tabular-nums text-muted-foreground" data-testid="obs-usage-percent">
            {formatPercent(usedFraction(u))} used
          </p>
        </div>
        <div
          role="progressbar"
          aria-label="Observability allowance used"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(fill)}
          className="mt-2 h-2 w-full overflow-hidden rounded-full bg-border/60"
        >
          <div className={cn("h-full rounded-full", barTone(u))} style={{ width: `${fill}%` }} data-testid="obs-usage-bar" />
        </div>

        <dl className="mt-4 grid gap-3 sm:grid-cols-3">
          <Stat label="Overage so far" testId="obs-usage-overage" value={u.exempt ? "Not billed" : formatDollars(u.overageCents)} hint={`${formatRate(u.rateCentsPerGb)} per GB beyond the allowance`} />
          <Stat
            label="Projected this month"
            testId="obs-usage-projected"
            value={formatBytes(u.projectedBytes)}
            hint={u.exempt ? "Not billed" : projectedOver ? `about ${formatDollars(u.projectedOverageCents)} in overage` : "within your allowance"}
          />
          <Stat label="Monthly spend cap" testId="obs-usage-cap" value={u.exempt ? "—" : formatDollars(u.spendCapCents)} hint={u.funded ? "Overage is billed to your payment method" : "No payment method: telemetry degrades instead of billing"} />
        </dl>

        <p className="mt-3 text-xs text-muted-foreground" data-testid="obs-usage-measured">
          {measuredThroughLabel(u)}
        </p>
      </Card>

      {notice && (
        <div
          role={notice.tone === "info" ? "status" : "alert"}
          data-testid="obs-usage-notice"
          data-tone={notice.tone}
          className={cn("flex items-start gap-3 rounded-lg border px-4 py-3", NOTICE_TONES[notice.tone].frame)}
        >
          {notice.tone === "info" ? (
            <Info className={cn("mt-0.5 h-4 w-4 shrink-0", NOTICE_TONES[notice.tone].icon)} aria-hidden="true" />
          ) : (
            <AlertTriangle className={cn("mt-0.5 h-4 w-4 shrink-0", NOTICE_TONES[notice.tone].icon)} aria-hidden="true" />
          )}
          <div className="min-w-0 space-y-0.5">
            <p className="text-sm font-medium text-foreground">{notice.title}</p>
            <p className="text-xs text-muted-foreground">{notice.body}</p>
          </div>
        </div>
      )}

      <Card>
        <CardHeader title="Where it comes from" description="Billed bytes this month." />
        <ul className="divide-y divide-border/60" data-testid="obs-usage-signals">
          {signals.map((s) => (
            <Row key={s.signal} label={SIGNAL_LABELS[s.signal]} bytes={s.billedBytes} testId={`obs-usage-signal-${s.signal}`} />
          ))}
        </ul>
        <CardInset className="mt-3 space-y-2" data-testid="obs-usage-included">
          <p className="text-xs text-muted-foreground">Counted within the signals above:</p>
          <ul className="divide-y divide-border/60">
            <Row
              label="Customer metadata"
              hint="Pod CPU and memory, restarts, events, image and version for your deployments. Billed like any other data."
              bytes={u.metadataBytes}
              testId="obs-usage-metadata"
            />
            <Row
              label="Browser"
              hint={
                browserCapHit(u)
                  ? `Only ${formatBytes(u.browserBilledBytes)} of ${formatBytes(u.browserBytes)} is billed: browser data counts up to ${formatBytes(u.browserCapBytes)} a month.`
                  : `Browser data counts toward the bill up to ${formatBytes(u.browserCapBytes)} a month.`
              }
              bytes={u.browserBilledBytes}
              testId="obs-usage-browser"
            />
          </ul>
        </CardInset>
      </Card>

      {topServices.length > 0 && (
        <Card>
          <CardHeader title="By service" description="The largest contributors, by signal." />
          <ul className="divide-y divide-border/60" data-testid="obs-usage-services">
            {topServices.map((s) => (
              <Row key={`${s.service}/${s.signal}`} label={s.service || "(no service name)"} hint={SIGNAL_LABELS[s.signal]} bytes={s.billedBytes} />
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}

function Stat({ label, value, hint, testId }: { label: string; value: string; hint: string; testId: string }) {
  return (
    <CardInset>
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="mt-1 text-lg font-semibold tabular-nums text-foreground" data-testid={testId}>
        {value}
      </dd>
      <dd className="text-xs text-muted-foreground">{hint}</dd>
    </CardInset>
  );
}

function Row({ label, hint, bytes, testId }: { label: string; hint?: string; bytes: number; testId?: string }) {
  return (
    <li className="flex items-baseline justify-between gap-4 py-2" data-testid={testId}>
      <div className="min-w-0">
        <p className="text-sm text-foreground">{label}</p>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
      <p className="shrink-0 text-sm tabular-nums text-foreground">{formatBytes(bytes)}</p>
    </li>
  );
}
