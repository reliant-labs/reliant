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

import { ArrowLeft, FileText, Flame, GitBranch } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { useServiceOverview } from "@/hooks/observability-queries";
import type { ObsScope, ServiceHealth, ServiceSignal } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart } from "./charts/UPlotChart";
import { attributeLabel } from "./attributes";
import { formatCount, formatDuration, formatPercent, formatRate } from "./format";
import { groupLabel } from "./obsFilters";
import type { ObsNavigate } from "./obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows, ObsNoData, OperatorBadge } from "./ObsStates";
import { signalState, useSignalsIndex, type SignalsIndex } from "./serviceSignals";

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
    return <><ObsAsOf updatedAt={overview.dataUpdatedAt} /><ServiceDetail rows={selected} groupBy={groupBy} navigate={navigate} scope={scope} range={range} /></>;
  }
  if (services.length === 0) {
    return <ObsNoData what="telemetry" range={range} onWiden={() => navigate({ range: "7d" })} />;
  }

  return (
    <Card padding="none" className="overflow-hidden" data-testid="obs-overview">
      <ObsAsOf updatedAt={overview.dataUpdatedAt} />
      <DataTable<ServiceRow>
        flush
        stacked
        ariaLabel={`Services in ${subject}: request rate, error rate and 95th-percentile latency over the selected window`}
        columns={serviceColumns(groupBy)}
        data={services.map((health) => ({ health, group: groupBy.length ? groupLabel(health.group, groupBy) : "" }))}
        getRowKey={({ health, group }) => `${health.name}|${group}`}
        getRowAction={({ health }) => () => navigate({ service: health.name })}
        getRowLabel={({ health }) => health.name}
        getRowProps={({ health, group }) => ({ "data-testid": group ? `obs-service-${health.name}-${group}` : `obs-service-${health.name}` })}
      />
    </Card>
  );
}

type ServiceRow = { health: ServiceHealth; group: string };

/** A service that served no requests sent logs or metrics only: its request figures say so once. */
const NO_REQUESTS = <span className="text-xs text-ink-muted">—</span>;

function serviceColumns(groupBy: string[]): DataTableColumn<ServiceRow>[] {
  return [
    {
      key: "service",
      header: "Service",
      render: (_, { health }) => (
        <span className="flex items-center gap-2">
          <span className="font-mono">{health.name}</span>
          <OperatorBadge provenance={health} />
        </span>
      ),
    },
    ...(groupBy.length > 0
      ? [{ key: "group", header: groupBy.map(attributeLabel).join(" · "), render: (_: unknown, { group }: ServiceRow) => <span className="font-mono text-xs">{group}</span> }]
      : []),
    {
      key: "requests",
      header: "Requests",
      render: (_, { health }) =>
        health.requestCount > 0 ? (
          <MetricCell
            value={formatRate(health.requestRate)}
            timestamps={health.timestamps}
            values={health.rateSeries}
            label={`Request rate for ${health.name}`}
            seriesLabel="Requests per second"
            unit="rate"
          />
        ) : (
          <span className="text-xs text-ink-muted">No requests served — this service sent logs or metrics only.</span>
        ),
    },
    {
      key: "errors",
      header: "Errors",
      render: (_, { health }) => {
        if (health.requestCount === 0) return NO_REQUESTS;
        const erroring = health.errorRate >= ERROR_RATE_ALERT;
        return (
          <MetricCell
            value={formatPercent(health.errorRate)}
            valueClassName={erroring ? "text-danger-ink" : undefined}
            timestamps={health.timestamps}
            values={health.errorRateSeries}
            label={`Error rate for ${health.name}`}
            seriesLabel="Error rate"
            unit="percent"
            tone={erroring ? "destructive" : "muted"}
          />
        );
      },
    },
    {
      key: "latency",
      header: "Latency",
      render: (_, { health }) =>
        health.requestCount > 0 ? (
          <span className="tabular-nums">
            {formatDuration(health.p95Ms)}
            <span className="ml-1.5 text-xs text-ink-muted">p95 · {formatDuration(health.p99Ms)} p99</span>
          </span>
        ) : (
          NO_REQUESTS
        ),
    },
    {
      key: "errorLogs",
      header: "Error logs",
      render: (_, { health }) => (
        <span className={cn("tabular-nums", health.errorLogCount > 0 ? "text-danger-ink" : "text-ink-muted")}>{formatCount(health.errorLogCount)}</span>
      ),
    },
  ];
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
    <div className="flex items-center gap-3">
      <span className={cn("w-16 shrink-0 tabular-nums text-ink", valueClassName)}>{value}</span>
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
  );
}

/** The service page's jumps to each signal view, in order. */
const SIGNAL_JUMPS: { signal: ServiceSignal; label: string; icon: typeof FileText }[] = [
  { signal: "logs", label: "Logs", icon: FileText },
  { signal: "traces", label: "Traces", icon: GitBranch },
  { signal: "profiles", label: "Profiles", icon: Flame },
];

function ServiceDetail({
  rows,
  groupBy,
  navigate,
  scope,
  range,
}: {
  rows: ServiceHealth[];
  groupBy: string[];
  navigate: ObsNavigate;
  scope: ObsScope;
  range: RangePreset;
}) {
  const health = rows[0];
  const signals = useSignalsIndex(scope, range);
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
          <h3 className="font-mono text-sm font-semibold text-ink">{health.name}</h3>
          <OperatorBadge provenance={health} />
          {compared && (
            <span className="text-xs text-ink-muted">
              by {groupBy.map(attributeLabel).join(" · ")} · {rows.length} groups
            </span>
          )}
        </div>
        <SignalJumps service={health.name} signals={signals} navigate={navigate} />
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
            <div data-testid="obs-service-latency">
              <DataTable<{ row: ServiceHealth }>
                flush
                stacked
                compact
                ariaLabel={`Latency of ${health.name} per group`}
                columns={[
                  {
                    key: "group",
                    header: groupBy.map(attributeLabel).join(" · "),
                    className: "max-w-[8rem] truncate",
                    render: (_, { row }) => <span className="font-mono text-xs">{label(row)}</span>,
                  },
                  { key: "p50", header: "p50", numeric: true, render: (_, { row }) => formatDuration(row.p50Ms) },
                  { key: "p95", header: "p95", numeric: true, render: (_, { row }) => formatDuration(row.p95Ms) },
                  { key: "p99", header: "p99", numeric: true, render: (_, { row }) => formatDuration(row.p99Ms) },
                ]}
                data={rows.map((row) => ({ row }))}
                getRowKey={({ row }) => label(row)}
              />
            </div>
          ) : (
            <dl className="grid grid-cols-3 gap-2" data-testid="obs-service-latency">
              {[
                ["p50", health.p50Ms],
                ["p95", health.p95Ms],
                ["p99", health.p99Ms],
              ].map(([name, value]) => (
                <div key={name as string}>
                  <dt className="text-xs font-medium uppercase tracking-wide text-ink-muted">{name}</dt>
                  <dd className="text-lg font-semibold tabular-nums text-ink">{formatDuration(value as number)}</dd>
                </div>
              ))}
            </dl>
          )}
          <p className="mt-4 text-xs text-ink-muted">
            {errorLogCount > 0 ? (
              <button
                type="button"
                className="rounded-sm text-danger-ink underline underline-offset-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
  return <span className={cn("text-sm font-semibold tabular-nums", destructive ? "text-danger-ink" : "text-ink")}>{value}</span>;
}

/**
 * Logs, Traces and Profiles for one service: the ones it sends, the idle ones
 * it declares muted with why, and none it neither sends nor declares
 * (serviceSignals.ts).
 */
function SignalJumps({ service, signals, navigate }: { service: string; signals: SignalsIndex | null; navigate: ObsNavigate }) {
  return (
    <div className="flex flex-wrap gap-2" data-testid="obs-service-signals">
      {SIGNAL_JUMPS.map(({ signal, label, icon: Icon }) => {
        const state = signalState(signals, service, signal);
        if (state.kind === "absent") return null;
        const idle = state.kind === "idle" ? state : null;
        return (
          <Button
            key={signal}
            variant="outline"
            size="sm"
            disabled={!!idle}
            title={idle?.hint}
            onClick={() => navigate({ view: signal, service })}
            leftIcon={<Icon className="h-3.5 w-3.5" />}
            data-testid={`obs-service-${signal}`}
          >
            {label}
            {idle && <span className="text-2xs text-ink-muted">{idle.badge}</span>}
          </Button>
        );
      })}
    </div>
  );
}
