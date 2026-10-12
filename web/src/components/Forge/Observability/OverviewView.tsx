// Copyright (c) 2025 Reliant Labs

/**
 * Overview: every service in the environment with its rate, errors and p95 —
 * the RED numbers — and a sparkline of the rate and the error rate, so a
 * regression is a shape you see before a number you read.
 *
 * Clicking a service opens it in place: the same trends drawn full size, its
 * latency percentiles and error-log count, and the ways out to its logs,
 * traces and dashboard. It needs no second request; the overview already
 * carries each service's trends.
 */

import { ArrowLeft, FileText, GitBranch, LayoutDashboard } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useServiceOverview } from "@/hooks/observability-queries";
import type { EnvScope, ServiceHealth } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart } from "./charts/UPlotChart";
import { formatCount, formatDuration, formatPercent, formatRate } from "./format";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows, ObsNoData, OperatorBadge } from "./ObsStates";

/** An error rate at or above this reads as a problem, and is drawn in the destructive tone. */
export const ERROR_RATE_ALERT = 0.05;

export function OverviewView({
  scope,
  envName,
  range,
  service,
  navigate,
}: {
  scope: EnvScope;
  envName: string;
  range: RangePreset;
  service: string | undefined;
  navigate: ObsNavigate;
}) {
  const overview = useServiceOverview(scope, range);

  if (overview.isLoading) return <ObsLoadingRows testId="obs-overview-loading" />;
  if (overview.error && !overview.data) {
    return (
      <ObsError
        error={overview.error}
        envName={envName}
        onRetry={() => void overview.refetch()}
        retrying={overview.isFetching}
      />
    );
  }

  const services = overview.data?.services ?? [];
  const selected = service ? services.find((candidate) => candidate.name === service) : undefined;
  if (service && selected) {
    return <ServiceDetail health={selected} navigate={navigate} />;
  }
  if (services.length === 0) {
    return <ObsNoData what="telemetry" range={range} onWiden={() => navigate({ range: "7d" })} />;
  }

  return (
    <Card padding="none" data-testid="obs-overview">
      <table className="w-full text-sm">
        <caption className="sr-only">
          Services in {envName}: request rate, error rate and 95th-percentile latency over the selected window
        </caption>
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-4 py-2 font-medium">
              Service
            </th>
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
            <ServiceRow key={health.name} health={health} onOpen={() => navigate({ service: health.name })} />
          ))}
        </tbody>
      </table>
    </Card>
  );
}

function ServiceRow({ health, onOpen }: { health: ServiceHealth; onOpen: () => void }) {
  const erroring = health.errorRate >= ERROR_RATE_ALERT;
  const served = health.requestCount > 0;
  return (
    <tr className="border-b border-border/60 last:border-b-0 hover:bg-muted/50" data-testid={`obs-service-${health.name}`}>
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

function ServiceDetail({ health, navigate }: { health: ServiceHealth; navigate: ObsNavigate }) {
  const erroring = health.errorRate >= ERROR_RATE_ALERT;
  return (
    <div className="space-y-4" data-testid="obs-service-detail">
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
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ view: "dashboards", service: health.name })}
            leftIcon={<LayoutDashboard className="h-3.5 w-3.5" />}
          >
            Dashboard
          </Button>
        </div>
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <Card padding="md">
          <CardHeader title="Requests" actions={<Headline value={formatRate(health.requestRate)} />} />
          <UPlotChart
            height={140}
            timestamps={health.timestamps}
            series={[{ label: "Requests per second", values: health.rateSeries, tone: "primary" }]}
            unit="rate"
            label={`Request rate for ${health.name}`}
          />
        </Card>
        <Card padding="md">
          <CardHeader
            title="Error rate"
            description={`${formatCount(health.errorCount)} failed of ${formatCount(health.requestCount)}`}
            actions={<Headline value={formatPercent(health.errorRate)} destructive={erroring} />}
          />
          <UPlotChart
            height={140}
            timestamps={health.timestamps}
            series={[{ label: "Error rate", values: health.errorRateSeries, tone: erroring ? "destructive" : "muted" }]}
            unit="percent"
            label={`Error rate for ${health.name}`}
          />
        </Card>
        <Card padding="md">
          <CardHeader title="Latency" description="Server spans, over the window" />
          <dl className="grid grid-cols-3 gap-2" data-testid="obs-service-latency">
            {[
              ["p50", health.p50Ms],
              ["p95", health.p95Ms],
              ["p99", health.p99Ms],
            ].map(([label, value]) => (
              <div key={label as string}>
                <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
                <dd className="text-lg font-semibold tabular-nums text-foreground">{formatDuration(value as number)}</dd>
              </div>
            ))}
          </dl>
          <p className="mt-4 text-xs text-muted-foreground">
            {health.errorLogCount > 0 ? (
              <button
                type="button"
                className="rounded-sm text-destructive underline underline-offset-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                onClick={() => navigate({ view: "logs", service: health.name, severity: "error,fatal" })}
              >
                {formatCount(health.errorLogCount)} error log{health.errorLogCount === 1 ? "" : "s"}
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
