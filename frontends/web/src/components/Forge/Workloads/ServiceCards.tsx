// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's SERVICES as one tight list: a row per long-running workload,
 * as tall as what it says and no taller.
 *
 *   Service            Health     req/s  errors  p95   CPU        Memory     Restarts 24h
 *   api  Deployment    ● Degraded 23.1/s  0.3%   78ms  ▬▭ 42%     ▬▬▬ 97%    3 · Ran out of memory
 *   3/3 ready · v1.8.3
 *
 * On a desktop the columns line up under one header row; on a phone a row is
 * the name and its health, then its figures, then its meters, each labelled
 * in place. The RED figures are the workload's own requests, from the same
 * GetServiceOverview answer Process health reads (the caller passes it in, by
 * name and environment); a workload that serves no requests says so once.
 *
 * The whole row opens the workload (its name is the link, stretched over the
 * row); the release link inside stays its own target. A declared-only
 * (unobserved) workload has no page to open: its row is muted and links
 * nowhere.
 */

import { useState, type ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import type { ServiceHealth } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatBytes, formatCores, formatDuration, formatPercent, formatRate } from "../Observability/format";
import { ERROR_RATE_ALERT } from "../Observability/OverviewView";
import type { HostedByEnv, OverviewHrefs } from "./OverviewHome";
import { HEALTH_STATUS, KIND_LABEL, lastExitOf, replicasShort, replicasText, worstUsage, type ServiceRow } from "./workloadModel";
import { exitPhrase } from "./plainHealth";
import { HealthStatus, UsageMeter, WorkloadRelease } from "./workloadParts";

/** Rows shown before "Show all". */
export const CARDS_SHOWN = 6;

/**
 * One grid for the header and every row, so the columns line up. Below `lg`
 * a row is two columns (the name, its health) and the rest wraps under them.
 */
const ROW_GRID =
  "grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1.5 px-4 py-2.5 lg:grid-cols-[minmax(12rem,1fr)_8.5rem_14rem_8rem_8rem_minmax(8rem,13rem)] lg:gap-y-0";

/** The request side of a row: found by workload name and environment, or not instrumented. */
export interface RedLookup {
  /** The process's figures; undefined when it sent no request telemetry. */
  of: (workload: string, env: string) => ServiceHealth | undefined;
  /** The read has not answered yet. */
  loading: boolean;
}

export function ServiceCards({
  rows,
  loading,
  showEnv,
  windowShort,
  hosted,
  hrefs,
  red,
  empty,
}: {
  rows: ServiceRow[];
  loading: boolean;
  showEnv: boolean;
  windowShort: string;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  red: RedLookup;
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
        <span>Service</span>
        <span>Health</span>
        <span className="grid grid-cols-3 gap-2 text-right">
          <span>req/s</span>
          <span>errors</span>
          <span>p95</span>
        </span>
        <span>CPU</span>
        <span>Memory</span>
        <span>Restarts {windowShort}</span>
      </div>
      <ul className="divide-y divide-border" aria-label="Services">
        {shown.map((w) => (
          <ServiceCard
            key={`${w.envName}/${w.workload}`}
            w={w}
            showEnv={showEnv}
            windowShort={windowShort}
            hosted={hosted}
            hrefs={hrefs}
            health={red.of(w.workload, w.envName)}
            redLoading={red.loading}
          />
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
            {all ? "Show fewer" : `Show all ${rows.length} services`}
          </button>
        </div>
      )}
    </div>
  );
}

function ServiceCard({
  w,
  showEnv,
  windowShort,
  hosted,
  hrefs,
  health,
  redLoading,
}: {
  w: ServiceRow;
  showEnv: boolean;
  windowShort: string;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  health: ServiceHealth | undefined;
  redLoading: boolean;
}) {
  const tone: StatusTone = w.unobserved ? "neutral" : HEALTH_STATUS[w.health].tone;
  const exit = w.unobserved ? null : lastExitOf(w);
  return (
    <li
      className={cn(ROW_GRID, "relative min-w-0 transition-colors", !w.unobserved && "hover:bg-ink/5")}
      data-testid={`overview-service-${w.workload}${showEnv ? `-${w.envName}` : ""}`}
      data-health={w.unobserved ? "unobserved" : w.health}
    >
      <div className="min-w-0">
        <div className="flex min-w-0 items-baseline gap-x-2">
          {w.unobserved ? (
            <span className="truncate text-sm font-medium text-ink-muted">{w.workload}</span>
          ) : (
            <Link
              href={hrefs.workload(w.workload, w.envName)}
              className="no-color truncate text-sm font-medium text-ink after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-ring"
            >
              {w.workload}
            </Link>
          )}
          {!w.unobserved && KIND_LABEL[w.kind] && <span className="shrink-0 text-xs text-ink-muted">{KIND_LABEL[w.kind]}</span>}
          {showEnv && <Tag>{w.envName}</Tag>}
        </div>
        {!w.unobserved && (
          <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs text-ink-muted">
            <span className={cn("tabular-nums", replicasShort(w) && "text-warning-ink")}>{replicasText(w)} ready</span>
            <span aria-hidden="true">·</span>
            <span className="relative z-10">
              <WorkloadRelease workload={w} releaseHref={hrefs.release} hosted={hosted.get(w.envName)?.get(w.workload) ?? null} />
            </span>
          </div>
        )}
      </div>

      <span className="justify-self-end lg:justify-self-start">
        {w.unobserved ? (
          <span title={w.healthReason}>
            <Status tone="neutral" label="Unobserved" />
          </span>
        ) : (
          <HealthStatus workload={w} />
        )}
      </span>

      {w.unobserved ? (
        <p className="col-span-2 text-xs text-ink-muted lg:col-span-4">Declared by {w.envName}&apos;s last recorded render; nothing reports from it.</p>
      ) : (
        <>
          <div className="col-span-2 lg:col-span-1">
            <Red health={health} loading={redLoading} />
          </div>
          {/* A phone puts the two meters side by side; a desktop gives each its column. */}
          <div className="col-span-2 grid grid-cols-2 gap-4 text-xs lg:contents">
            <span className="flex min-w-0 items-center gap-2">
              <span className="text-ink-muted lg:sr-only">CPU</span>
              <UsageMeter usage={worstUsage(w, "cpu")} format={formatCores} what="CPU" />
            </span>
            <span className="flex min-w-0 items-center gap-2">
              <span className="text-ink-muted lg:sr-only">Memory</span>
              <UsageMeter usage={worstUsage(w, "memory")} format={formatBytes} what="Memory" />
            </span>
          </div>
          <div className={cn("col-span-2 min-w-0 truncate text-xs text-ink-muted lg:col-span-1", !w.restartsInWindow && "max-lg:hidden")}>
            <span className="lg:sr-only">Restarts {windowShort}</span>{" "}
            {w.restartsInWindow ? (
              <>
                <span className="tabular-nums text-ink">{w.restartsInWindow}</span>
                {exit && (
                  <span
                    className={cn("ml-1", tone === "danger" ? "text-danger-ink" : tone === "warning" ? "text-warning-ink" : "text-ink-muted")}
                    title={exit.atMs ? `Last exited ${new Date(exit.atMs).toLocaleString()}` : undefined}
                  >
                    · {exitPhrase(exit.reason)}
                  </span>
                )}
              </>
            ) : (
              <span className="tabular-nums">0</span>
            )}
          </div>
        </>
      )}
    </li>
  );
}

/** Rate, errors and p95: three right-aligned figures on a desktop, labelled in place on a phone. */
function Red({ health, loading }: { health: ServiceHealth | undefined; loading: boolean }) {
  if (loading && !health) return <div className="h-4 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-hidden="true" />;
  if (!health || health.requestCount === 0) {
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
