// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's SERVICES: one tight list of everything the app runs, a row
 * each, as tall as what it says (the model is overviewRows.ts).
 *
 *   Service                  Status                  req/s  errors  p95   Error logs  Alerts
 *   api  Deployment          ● Degraded              23.1/s  0.3%   78ms  8,259       2 firing
 *   3/3 ready · v1.8.3       3 restarts · Ran out of memory
 *   search-reindex  CronJob  ● Failed
 *   ran 1h ago · v1.8.3      Failed after exhausting its retries
 *   payments  Process        ● No workload data      5/s     0.4%   910ms 2,373       ·
 *
 * Services, jobs and telemetry-only processes share one set of columns; the
 * kind is a word beside the name. A crash loop, an out-of-memory exit or a
 * failed run is the row's status and the line under it, so nothing the old
 * Processes table showed is lost. CPU, memory and pods are on the workload
 * page, which the whole row opens. A telemetry-only process has no workload
 * page: its row opens its traces.
 *
 * On a desktop the columns line up under one header row; on a phone a row is
 * the name and its status, then its figures, labelled in place.
 */

import { useState, type ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { cn } from "@/lib/utils";

import { formatDuration, formatPercent, formatRate } from "../Observability/format";
import { ERROR_RATE_ALERT } from "../Observability/OverviewView";
import { Ago, INK_LINK } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import type { HostedByEnv, OverviewHrefs } from "./OverviewHome";
import type { OverviewRow } from "./overviewRows";
import { lastExitOf, replicasShort, replicasText } from "./workloadModel";
import { exitPhrase, jobFailurePhrase } from "./plainHealth";
import { HealthStatus, WorkloadRelease } from "./workloadParts";

/** Rows shown before "Show all". */
export const CARDS_SHOWN = 10;

/**
 * One grid for the header and every row, so the columns line up. Below `lg`
 * a row is two columns (the name, its status) and the rest wraps under them.
 */
const ROW_GRID =
  "grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1.5 px-4 py-2.5 lg:grid-cols-[minmax(12rem,1fr)_minmax(12rem,1fr)_14rem_6rem_5.5rem] lg:gap-y-0";

/** Where a row's links go, beyond the Overview's own hrefs. */
export interface RowHrefs {
  /** A process's error logs. */
  logs: (name: string) => string;
  /** A telemetry-only process's traces: it has no workload page. */
  traces: (name: string) => string;
}

export function ServiceCards({
  rows,
  loading,
  showEnv,
  hosted,
  hrefs,
  rowHrefs,
  redLoading,
  empty,
}: {
  rows: OverviewRow[];
  loading: boolean;
  showEnv: boolean;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  rowHrefs: RowHrefs;
  /** The request telemetry has not answered yet. */
  redLoading: boolean;
  empty: ReactNode;
}) {
  const [all, setAll] = useState(false);
  if (loading) {
    return (
      <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface" aria-label="Services" aria-busy="true">
        {Array.from({ length: 3 }).map((_, i) => (
          <li key={i} className="flex h-14 items-center gap-4 px-4">
            <span className="sr-only">Loading…</span>
            <span aria-hidden="true" className="h-3 w-32 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
            <span aria-hidden="true" className="h-3 flex-1 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
          </li>
        ))}
      </ul>
    );
  }
  if (rows.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-3 text-sm text-ink-muted" data-testid="overview-services-empty">
        {empty}
      </p>
    );
  }
  const shown = all ? rows : rows.slice(0, CARDS_SHOWN);
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-surface">
      {/* The column names, once, for a desktop; a phone labels each figure in place. */}
      <div aria-hidden="true" className={cn(ROW_GRID, "hidden border-b border-border py-2 text-xs text-ink-muted lg:grid")}>
        <span>Name</span>
        <span>Status</span>
        <span className="grid grid-cols-3 gap-2 text-right">
          <span>req/s</span>
          <span>errors</span>
          <span>p95</span>
        </span>
        <span className="text-right">Error logs</span>
        <span className="text-right">Alerts</span>
      </div>
      <ul className="divide-y divide-border" aria-label="Services">
        {shown.map((row) => (
          <Row key={row.key} row={row} showEnv={showEnv} hosted={hosted} hrefs={hrefs} rowHrefs={rowHrefs} redLoading={redLoading} />
        ))}
      </ul>
      {rows.length > CARDS_SHOWN && (
        <div className="border-t border-border px-2 py-1">
          <button
            type="button"
            onClick={() => setAll((prev) => !prev)}
            className="inline-flex h-7 items-center rounded-md px-2 text-xs font-medium text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="overview-services-more"
          >
            {all ? "Show fewer" : `Show all ${rows.length}`}
          </button>
        </div>
      )}
    </div>
  );
}

/** A row's test id: services keep the id the tour and the tests know; jobs and processes get their own. */
function testIdOf(row: OverviewRow, showEnv: boolean): string {
  const env = showEnv && row.envName ? `-${row.envName}` : "";
  if (row.type === "service") return `overview-service-${row.name}${env}`;
  if (row.type === "job") return `overview-service-job-${row.name}${env}`;
  return `overview-service-process-${row.name}${env}`;
}

const TONE_INK = { danger: "text-danger-ink", warning: "text-warning-ink", success: "text-ink-muted", neutral: "text-ink-muted" } as const;

function Row({
  row,
  showEnv,
  hosted,
  hrefs,
  rowHrefs,
  redLoading,
}: {
  row: OverviewRow;
  showEnv: boolean;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  rowHrefs: RowHrefs;
  redLoading: boolean;
}) {
  const unobserved = row.type === "service" && !!row.service.unobserved;
  const href = unobserved ? null : row.type === "process" ? rowHrefs.traces(row.name) : hrefs.workload(row.name, row.envName);
  return (
    <li
      className={cn(ROW_GRID, "relative min-w-0 transition-colors", href && "hover:bg-ink/5")}
      data-testid={testIdOf(row, showEnv)}
      data-kind={row.kind}
      data-tone={row.tone}
      data-health={row.type === "service" ? (unobserved ? "unobserved" : row.service.health) : undefined}
    >
      {/* Name, kind, environment; then what the row runs. */}
      <div className="min-w-0">
        <div className="flex min-w-0 items-baseline gap-x-2">
          {href ? (
            <Link
              href={href}
              title={row.type === "process" ? `${row.name}: no workload reports under this name, so this opens its traces` : undefined}
              className="no-color truncate text-sm font-medium text-ink after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-ring"
            >
              {row.name}
            </Link>
          ) : (
            <span className="truncate text-sm font-medium text-ink-muted">{row.name}</span>
          )}
          {row.kindLabel && <span className="shrink-0 text-xs text-ink-muted" data-testid="overview-row-kind">{row.kindLabel}</span>}
          {showEnv && row.envName && <Tag>{row.envName}</Tag>}
        </div>
        <Subject row={row} hosted={hosted} hrefs={hrefs} />
      </div>

      {/* The status, and the line that says why. */}
      <div className="min-w-0 justify-self-end text-right lg:justify-self-start lg:text-left">
        {row.type === "service" && !unobserved ? (
          <HealthStatus workload={row.service} />
        ) : (
          <span title={row.type === "process" ? (row.process.reason ?? undefined) : unobserved ? row.service.healthReason : undefined}>
            <Status tone={row.tone} label={row.status} />
          </span>
        )}
        <Why row={row} />
      </div>

      {unobserved ? (
        <p className="col-span-2 text-xs text-ink-muted lg:col-span-3">Declared by {row.envName}&apos;s last recorded render; nothing reports from it.</p>
      ) : (
        <>
          <div className="col-span-2 lg:col-span-1">
            <Red row={row} loading={redLoading} />
          </div>
          {/* A phone puts error logs and alerts on one line; a desktop gives each its column. */}
          <div className={cn("col-span-2 flex gap-4 text-xs lg:contents", !row.health && !row.alertsFiring && "max-lg:hidden")}>
            <span className="flex items-baseline gap-1 lg:justify-end lg:text-sm">
              <span className="text-ink-muted lg:sr-only">Error logs</span>
              {row.health ? (
                <Link href={rowHrefs.logs(row.name)} className={cn(INK_LINK, "relative z-10 tabular-nums")} title={`Error logs from ${row.name}`}>
                  {row.health.errorLogCount.toLocaleString()}
                </Link>
              ) : (
                <span className="text-ink-muted">—</span>
              )}
            </span>
            <span className="flex items-baseline gap-1 lg:justify-end lg:text-sm">
              <span className="text-ink-muted lg:sr-only">Alerts</span>
              {row.alertsFiring > 0 ? (
                <span className="text-warning-ink" data-testid="overview-row-alerts">{row.alertsFiring} firing</span>
              ) : (
                <span className="text-ink-muted">·</span>
              )}
            </span>
          </div>
        </>
      )}
    </li>
  );
}

/** The muted line under the name: ready replicas and release; a job's last run; nothing for a process. */
function Subject({ row, hosted, hrefs }: { row: OverviewRow; hosted: HostedByEnv; hrefs: OverviewHrefs }) {
  if (row.type === "service") {
    if (row.service.unobserved) return null;
    const w = row.service;
    return (
      <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs text-ink-muted">
        <span className={cn("tabular-nums", replicasShort(w) && "text-warning-ink")}>{replicasText(w)} ready</span>
        <span aria-hidden="true">·</span>
        <span className="relative z-10">
          <WorkloadRelease workload={w} releaseHref={hrefs.release} hosted={hosted.get(w.envName)?.get(w.workload) ?? null} />
        </span>
      </div>
    );
  }
  if (row.type === "job") {
    const { latest } = row.job;
    const at = latest.finishedAtMs ?? latest.startedAtMs;
    return (
      <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs text-ink-muted">
        <span>
          {latest.result === "running" ? "running since " : "ran "}
          {at ? <Ago at={new Date(at).toISOString()} /> : "at an unknown time"}
        </span>
        {latest.releaseVersion && (
          <>
            <span aria-hidden="true">·</span>
            <Link href={hrefs.release(latest.releaseVersion)} className={cn(INK_LINK, "relative z-10")}>
              {shortVersion(latest.releaseVersion)}
            </Link>
          </>
        )}
      </div>
    );
  }
  return <div className="text-xs text-ink-muted">Sends telemetry; no workload reports under this name</div>;
}

/** Why the status is what it is: restarts and the last exit, a job's failure and its run count, a process's signal. */
function Why({ row }: { row: OverviewRow }) {
  let text: ReactNode = null;
  if (row.type === "service" && !row.service.unobserved && row.service.restartsInWindow) {
    const exit = lastExitOf(row.service);
    text = (
      <span title={exit?.atMs ? `Last exited ${new Date(exit.atMs).toLocaleString()}` : undefined}>
        {row.service.restartsInWindow} restart{row.service.restartsInWindow === 1 ? "" : "s"}
        {exit && <> · {exitPhrase(exit.reason)}</>}
      </span>
    );
  } else if (row.type === "job") {
    const { latest, runs, failedRuns } = row.job;
    if (latest.result === "failed") text = jobFailurePhrase(latest.reason);
    else if (failedRuns > 0) text = `${failedRuns} of ${runs} runs failed`;
  } else if (row.type === "process" && row.process.reason) {
    text = row.process.reason;
  }
  if (!text) return null;
  return (
    <div className={cn("truncate text-xs", TONE_INK[row.tone])} data-testid="overview-row-why">
      {text}
    </div>
  );
}

/** Rate, errors and p95: three right-aligned figures on a desktop, labelled in place on a phone. */
function Red({ row, loading }: { row: OverviewRow; loading: boolean }) {
  const health = row.health;
  if (loading && !health) return <div className="h-4 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-hidden="true" />;
  if (!health || health.requestCount === 0) {
    // A job serves no requests: say nothing rather than "no requests traced".
    if (row.type === "job") return <p className="hidden text-right text-xs text-ink-muted lg:block">—</p>;
    return <p className="text-xs text-ink-muted lg:text-right" data-testid="service-card-no-requests">No requests traced in this window.</p>;
  }
  const erroring = health.errorRate >= ERROR_RATE_ALERT;
  return (
    <dl className="flex gap-4 lg:grid lg:grid-cols-3 lg:gap-2" data-testid="service-card-red">
      <Figure name="req/s" value={formatRate(health.requestRate)} />
      <Figure name="errors" value={formatPercent(health.errorRate)} tone={erroring ? "text-danger-ink" : undefined} />
      <Figure name="p95" value={formatDuration(health.p95Ms)} />
    </dl>
  );
}

function Figure({ name, value, tone }: { name: string; value: string; tone?: string }) {
  return (
    // The term is first in the markup, as a description list requires; it is drawn after the figure, and only on a phone.
    <div className="flex min-w-0 flex-row-reverse items-baseline justify-end gap-1 lg:justify-start">
      <dt className="text-xs text-ink-muted lg:sr-only">{name}</dt>
      <dd className={cn("truncate text-sm tabular-nums text-ink lg:w-full lg:text-right", tone)}>{value}</dd>
    </div>
  );
}
