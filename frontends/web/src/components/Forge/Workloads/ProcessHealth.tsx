// Copyright (c) 2025 Reliant Labs

/**
 * PROCESSES on the Dashboards hub: every process that sends telemetry
 * (service.name), one dense line each — status, the RED figures, CPU, error
 * logs and alerts. (The project Overview folds the same figures into its one
 * Services list, overviewRows.ts, and has no Processes table of its own.)
 *
 * On a phone each process stacks into labelled lines; CPU and Alerts drop out
 * there (Services carries CPU, "Needs attention" carries the firing alerts),
 * so a process is five short lines rather than eight.
 */
import { useMemo } from "react";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useServiceOverview } from "@/hooks/observability-queries";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { ObsScope, Workload } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { ENV_KEY } from "../Observability/attributes";
import { formatCores, formatDuration, formatPercent, formatRate } from "../Observability/format";
import { byAttention, serviceRows } from "../Observability/Home/homeModel";
import { ObsAsOf, ObsError, ObsLoadingRows } from "../Observability/ObsStates";
import { INK_LINK, Panel } from "../Releases/releaseParts";
import { worstUsage } from "./workloadModel";
import { UsageMeter } from "./workloadParts";
import { PartialNote, StaleNote, keepsStaleResult } from "./tileState";

type HealthRow = { key: string; row: ReturnType<typeof serviceRows>[number] };

export function ProcessHealth({ scope, range, workloads, alerts, envNameOf, workloadHref, logsHref }: {
  scope: ObsScope | null;
  range: RangePreset;
  workloads: readonly Workload[];
  alerts: readonly AlertInstance[];
  envNameOf: (id: string) => string | undefined;
  workloadHref: (name: string, env: string | null) => string;
  logsHref: (name: string) => string;
}) {
  const showEnv = scope?.envIds.length === 0;
  const groupBy = useMemo(() => showEnv ? [ENV_KEY] : [], [showEnv]);
  const overview = useServiceOverview(scope, range, [], groupBy);
  // Its own read, state and Retry (tileState.tsx): never hidden by the
  // workload read, and its rows stay through a failed refresh.
  const failed = !!overview.error && !keepsStaleResult(overview.error, overview.data);
  const stale = !!overview.error && !failed;
  const retry = () => void overview.refetch({ cancelRefetch: false });
  const rows = useMemo(() => {
    // An ungrouped single-env health row carries no env label. Constrain both
    // joins by immutable environment IDs before the name-based model sees it.
    const ids = new Set(scope?.envIds ?? []);
    const scopedWorkloads = ids.size ? workloads.filter(workload => ids.has(workload.envId)) : workloads;
    const scopedAlerts = ids.size ? alerts.filter(alert => ids.has(alert.envId)) : alerts;
    return serviceRows(overview.data?.services ?? [], scopedWorkloads, scopedAlerts, envNameOf).sort(byAttention);
  }, [overview.data, workloads, alerts, envNameOf, scope]);
  const columns: DataTableColumn<HealthRow>[] = [
    { key: "name", header: "Name", className: "font-medium", render: (_, { row }) => row.name },
    ...(showEnv ? [{ key: "env", header: "Environment", render: (_: unknown, { row }: HealthRow) => <Tag>{row.env ?? "—"}</Tag> }] : []),
    {
      key: "status",
      header: "Status",
      render: (_, { row }) => <span title={row.reason ?? undefined}><Status tone={row.tone} label={row.status} /></span>,
    },
    { key: "rate", header: "Req/s", numeric: true, render: (_, { row }) => formatRate(row.health.requestRate) },
    { key: "errors", header: "Errors", numeric: true, render: (_, { row }) => (row.health.requestCount ? formatPercent(row.health.errorRate) : "—") },
    { key: "p95", header: "p95", numeric: true, render: (_, { row }) => (row.health.requestCount ? formatDuration(row.health.p95Ms) : "—") },
    {
      key: "cpu",
      header: "CPU",
      width: "10rem",
      // `!`: the stacked layout's own max-md display would otherwise win.
      className: "max-md:!hidden",
      render: (_, { row }) => (row.workload ? <UsageMeter usage={worstUsage(row.workload, "cpu")} format={formatCores} what="CPU" /> : "—"),
    },
    {
      key: "errorLogs",
      header: "Error logs",
      numeric: true,
      interactive: true,
      render: (_, { row }) => (
        <Link href={logsHref(row.name)} className={INK_LINK} title={`Error logs from ${row.name}`}>
          {row.health.errorLogCount.toLocaleString()}
        </Link>
      ),
    },
    {
      key: "alerts",
      header: "Alerts",
      numeric: true,
      className: "max-md:!hidden",
      render: (_, { row }) =>
        row.alertsFiring > 0 ? <span className="text-warning-ink">{row.alertsFiring} firing</span> : <span className="text-ink-muted">·</span>,
    },
  ];
  return <Panel title="Processes" meta="What each deployed process (service.name) served, worst first" actions={overview.data ? <ObsAsOf updatedAt={overview.dataUpdatedAt} /> : undefined} flush testId="observability-services">
    {overview.isLoading ? <ObsLoadingRows /> : failed ?
      <div className="p-4" data-testid="process-health-error"><ObsError error={overview.error} subject={scope?.projectId ?? ""} onRetry={retry} retrying={overview.isFetching} /></div> : rows.length === 0 ?
      <p className="px-4 py-6 text-sm text-ink-muted" data-testid="observability-no-services">No process sent telemetry in this window.</p> :
      <DataTable<HealthRow>
        flush
        stacked
        compact
        ariaLabel="Services, sorted by what needs attention"
        columns={columns}
        data={rows.map((row) => ({ key: `${row.name}/${row.env ?? ""}`, row }))}
        getRowKey={(r) => r.key}
        getRowHref={({ row }) => workloadHref(row.name, row.env)}
        getRowProps={({ row }) => ({
          "data-testid": `observability-service-${row.name}${row.env ? `-${row.env}` : ""}`,
          "data-tone": row.tone,
        })}
      />}
    {!failed && <PartialNote sections={overview.data?.unavailableSections ?? []} onRetry={retry} retrying={overview.isFetching} testId="process-health-partial" />}
    {stale && <StaleNote onRetry={retry} retrying={overview.isFetching} testId="process-health-stale" />}
  </Panel>;
}
