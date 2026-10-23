// Copyright (c) 2025 Reliant Labs

/**
 * Overview: every service in the environment with its rate, errors and p95 —
 * the RED numbers — and a sparkline of the rate and the error rate, so a
 * regression is a shape you see before a number you read.
 *
 * Clicking a service opens it in place: the same trends drawn full size, its
 * latency percentiles and error-log count, and the ways out to its logs,
 * traces. It needs no second request; the overview already
 * carries each service's trends.
 *
 * With a group-by, every service has one row per group — `api` in prod and
 * `api` in staging, or `api` at v1.8.2 and at v1.8.3 — and its detail draws
 * the groups against each other: one line each, on the same axes.
 */

import { ArrowLeft, FileText, GitBranch } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useServiceOverview } from "@/hooks/observability-queries";
import type { ObsScope, ServiceHealth } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart } from "./charts/UPlotChart";
import { attributeLabel } from "./attributes";
import { formatCount, formatDuration, formatPercent, formatRate } from "./format";
import { groupLabel } from "./obsFilters";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows, ObsNoData, OperatorBadge } from "./ObsStates";
import { StackedTable } from "@/components/Forge/StackedTable";

/** An error rate at or above this reads as a problem, and is drawn in the destructive tone. */
export const ERROR_RATE_ALERT = 0.05;

export function OverviewView({
  scope,
  subject,
  range,
  service,
  groupBy = [],
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  service: string | undefined;
  groupBy?: string[];
  navigate: ObsNavigate;
}) {
  const overview = useServiceOverview(scope, range, [], groupBy);

  if (overview.isLoading) return <ObsLoadingRows testId="obs-overview-loading" />;
  if (overview.error && !overview.data) {
    return (
      <ObsError
        error={overview.error}
        subject={subject}
        onRetry={() => void overview.refetch()}
        retrying={overview.isFetching}
      />
    );
  }

  const services = overview.data?.services ?? [];
  const selected = service ? services.filter((candidate) => candidate.name === service) : [];
  if (service && selected.length > 0) {
    return <ServiceDetail rows={selected} groupBy={groupBy} navigate={navigate} />;
  }
  if (services.length === 0) {
    return <ObsNoData what="telemetry" range={range} onWiden={() => navigate({ range: "7d" })} />;
  }

  return (
    <Card padding="none" data-testid="obs-overview">
      <StackedTable className="w-full text-sm">
        <caption className="sr-only">
          Services in {subject}: request rate, error rate and 95th-percentile latency over the selected window
        </caption>
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-4 py-2 font-medium">
              Service
            </th>
            {groupBy.length > 0 && (
              <th scope="col" className="px-4 py-2 font-medium" data-testid="obs-overview-group-header">
                {groupBy.map(attributeLabel).join(" · ")}
              </th>
            )}
            <th scope="col" className="px-4 py-2 font-medium">
              Requests
            </th>
            <th scope="col" className="px-4 py-2 font-medium">
              Errors
            </th>
            <th scope="col" className="px-4 py-2 font-medium">
              Latency
            </th>
            <th scope="col" className="px-4 py-2 font-medium">
              Error logs
            </th>
          </tr>
        </thead>
        <tbody>
          {services.map((health) => (
            <ServiceRow
              key={`${health.name}|${groupLabel(health.group, groupBy)}`}
              health={health}
              groupBy={groupBy}
              onOpen={() => navigate({ service: health.name })}
            />
          ))}
        </tbody>
      </StackedTable>
    </Card>
  );
}

function ServiceRow({ health, groupBy, onOpen }: { health: ServiceHealth; groupBy: string[]; onOpen: () => void }) {
  const erroring = health.errorRate >= ERROR_RATE_ALERT;
  const served = health.requestCount > 0;
  const group = groupBy.length ? groupLabel(health.group, groupBy) : "";
  return (
    <tr
      className="border-b border-border/60 last:border-b-0 hover:bg-muted/50"
      data-testid={group ? `obs-service-${health.name}-${group}` : `obs-service-${health.name}`}
    >
      <th scope="row" className="px-4 py-2 text-left font-normal">
        <span className="flex items-center gap-2">
          <button
            type="button"
            onClick={onOpen}
            className="rounded-sm font-mono text-sm text-foreground underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {health.name}
          </button>
          <OperatorBadge provenance={health} />
        </span>
      </th>
      {groupBy.length > 0 && <td className="px-4 py-2 font-mono text-xs text-foreground">{group}</td>}
      {served ? (
        <>
          <MetricCell
            value={formatRate(health.requestRate)}
            timestamps={health.timestamps}
            values={health.rateSeries}
            label={`Request rate for ${health.name}`}
            seriesLabel="Requests per second"
            unit="rate"
          />
          <MetricCell
            value={formatPercent(health.errorRate)}
            valueClassName={erroring ? "text-destructive" : undefined}
            timestamps={health.timestamps}
            values={health.errorRateSeries}
            label={`Error rate for ${health.name}`}
            seriesLabel="Error rate"
            unit="percent"
            tone={erroring ? "destructive" : "muted"}
          />
          <td className="px-4 py-2 tabular-nums">
            <span className="text-foreground">{formatDuration(health.p95Ms)}</span>
            <span className="ml-1.5 text-xs text-muted-foreground">p95 · {formatDuration(health.p99Ms)} p99</span>
          </td>
        </>
      ) : (
        <td colSpan={3} className="px-4 py-2 text-xs text-muted-foreground">
          No requests served — this service sent logs or metrics only.
        </td>
      )}
      <td className={cn("px-4 py-2 tabular-nums", health.errorLogCount > 0 ? "text-destructive" : "text-muted-foreground")}>
        {formatCount(health.errorLogCount)}
      </td>
    </tr>
  );
}

function MetricCell({
  value,
  valueClassName,
  timestamps,
  values,
  label,
  seriesLabel,
  unit,
  tone = "primary",
}: {
  value: string;
  valueClassName?: string;
  timestamps: number[];
  values: (number | null)[];
  label: string;
  seriesLabel: string;
  unit?: "percent" | "ms" | "rate";
  tone?: "primary" | "destructive" | "muted" | "secondary";
}) {
  return (
    <td className="px-4 py-2">
      <div className="flex items-center gap-3">
        <span className={cn("w-16 shrink-0 tabular-nums text-foreground", valueClassName)}>{value}</span>
        <UPlotChart
          compact
          className="h-7 w-28 min-w-0"
          height={28}
          timestamps={timestamps}
          series={[{ label: seriesLabel, values, tone }]}
          unit={unit}
          label={label}
        />
      </div>
    </td>
  );
}

function ServiceDetail({ rows, groupBy, navigate }: { rows: ServiceHealth[]; groupBy: string[]; navigate: ObsNavigate }) {
  const health = rows[0];
  const compared = groupBy.length > 0 && rows.length > 1;
  // Every group's trend on one shared axis.
  const timestamps = [...new Set(rows.flatMap((row) => row.timestamps))].sort((a, b) => a - b);
  const aligned = (row: ServiceHealth, values: (number | null)[]) => {
    const byTime = new Map(row.timestamps.map((t, i) => [t, values[i] ?? null]));
    return timestamps.map((t) => byTime.get(t) ?? null);
  };
  const requestCount = rows.reduce((sum, row) => sum + row.requestCount, 0);
  const errorCount = rows.reduce((sum, row) => sum + row.errorCount, 0);
  const errorRate = requestCount ? errorCount / requestCount : 0;
  const erroring = errorRate >= ERROR_RATE_ALERT;
  const errorLogCount = rows.reduce((sum, row) => sum + row.errorLogCount, 0);
  const label = (row: ServiceHealth) => groupLabel(row.group, groupBy);

  return (
    <div className="space-y-4" data-testid="obs-service-detail" data-compared={compared || undefined}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => navigate({ service: undefined })}
            leftIcon={<ArrowLeft className="h-3.5 w-3.5" />}
            data-testid="obs-service-back"
          >
            All services
          </Button>
          <h3 className="font-mono text-sm font-semibold text-foreground">{health.name}</h3>
          <OperatorBadge provenance={health} />
          {compared && (
            <span className="text-xs text-muted-foreground">
              by {groupBy.map(attributeLabel).join(" · ")} · {rows.length} groups
            </span>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ view: "logs", service: health.name })}
            leftIcon={<FileText className="h-3.5 w-3.5" />}
          >
            Logs
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ view: "traces", service: health.name })}
            leftIcon={<GitBranch className="h-3.5 w-3.5" />}
          >
            Traces
          </Button>
        </div>
      </div>
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-3">
        <Card padding="md">
          <CardHeader
            title="Requests"
            actions={<Headline value={formatRate(rows.reduce((sum, row) => sum + row.requestRate, 0))} />}
          />
          <UPlotChart
            height={140}
            timestamps={timestamps}
            series={
              compared
                ? rows.map((row) => ({ label: label(row), values: aligned(row, row.rateSeries) }))
                : [{ label: "Requests per second", values: aligned(health, health.rateSeries), tone: "primary" }]
            }
            unit="rate"
            label={`Request rate for ${health.name}`}
          />
        </Card>
        <Card padding="md">
          <CardHeader
            title="Error rate"
            description={`${formatCount(errorCount)} failed of ${formatCount(requestCount)}`}
            actions={<Headline value={formatPercent(errorRate)} destructive={erroring} />}
          />
          <UPlotChart
            height={140}
            timestamps={timestamps}
            series={
              compared
                ? rows.map((row) => ({ label: label(row), values: aligned(row, row.errorRateSeries) }))
                : [{ label: "Error rate", values: aligned(health, health.errorRateSeries), tone: erroring ? "destructive" : "muted" }]
            }
            unit="percent"
            label={`Error rate for ${health.name}`}
          />
        </Card>
        <Card padding="md">
          <CardHeader title="Latency" description="Server spans, over the window" />
          {compared ? (
            <StackedTable className="w-full text-xs" data-testid="obs-service-latency">
              <caption className="sr-only">Latency of {health.name} per group</caption>
              <thead>
                <tr className="text-left text-muted-foreground">
                  <th scope="col" className="pb-1 pr-2 font-medium">
                    {groupBy.map(attributeLabel).join(" · ")}
                  </th>
                  <th scope="col" className="pb-1 pr-2 text-right font-medium">p50</th>
                  <th scope="col" className="pb-1 pr-2 text-right font-medium">p95</th>
                  <th scope="col" className="pb-1 text-right font-medium">p99</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={label(row)} className="border-t border-border/50">
                    <th scope="row" className="max-w-[8rem] truncate py-1 pr-2 text-left font-mono font-normal text-foreground">
                      {label(row)}
                    </th>
                    <td className="py-1 pr-2 text-right tabular-nums">{formatDuration(row.p50Ms)}</td>
                    <td className="py-1 pr-2 text-right tabular-nums">{formatDuration(row.p95Ms)}</td>
                    <td className="py-1 text-right tabular-nums">{formatDuration(row.p99Ms)}</td>
                  </tr>
                ))}
              </tbody>
            </StackedTable>
          ) : (
            <dl className="grid grid-cols-3 gap-2" data-testid="obs-service-latency">
              {[
                ["p50", health.p50Ms],
                ["p95", health.p95Ms],
                ["p99", health.p99Ms],
              ].map(([name, value]) => (
                <div key={name as string}>
                  <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{name}</dt>
                  <dd className="text-lg font-semibold tabular-nums text-foreground">{formatDuration(value as number)}</dd>
                </div>
              ))}
            </dl>
          )}
          <p className="mt-4 text-xs text-muted-foreground">
            {errorLogCount > 0 ? (
              <button
                type="button"
                className="rounded-sm text-destructive underline underline-offset-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                onClick={() => navigate({ view: "logs", service: health.name, severity: "error,fatal" })}
              >
                {formatCount(errorLogCount)} error log{errorLogCount === 1 ? "" : "s"}
              </button>
            ) : (
              "No error logs"
            )}{" "}
            in this window.
          </p>
        </Card>
      </div>
    </div>
  );
}

function Headline({ value, destructive }: { value: string; destructive?: boolean }) {
  return <span className={cn("text-sm font-semibold tabular-nums", destructive ? "text-destructive" : "text-foreground")}>{value}</span>;
}
