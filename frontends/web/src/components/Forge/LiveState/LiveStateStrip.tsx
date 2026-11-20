// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's LIVE STATE (U6c, owner 2026-11-20): one compact row per
 * thing the app runs — status, ready/desired, CPU and memory as sparklines,
 * req/s, p95 and errors — and the database's row, whose live status is not
 * reported yet. Each row opens its page under Services. Read from
 * useAppLiveState, the hook the Services tab and the Live state dashboard
 * read, so the three always agree.
 */

import { useMemo } from "react";
import { ChevronRight } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import { cn } from "@/lib/utils";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

import { formatBytes, formatCores, formatDuration, formatPercent, formatRate } from "../Observability/format";
import type { LiveStateEnv, LiveStateRow } from "./liveStateModel";
import { Sparkline } from "./Sparkline";
import { useAppLiveState, useWorkloadUsage } from "./useAppLiveState";

export interface LiveStateLinks {
  services: string;
  service: (name: string, envName: string) => string;
}

const GRID = "grid grid-cols-[minmax(0,2.2fr)_minmax(0,1.3fr)_4rem_minmax(0,1.4fr)_minmax(0,1.4fr)_4.5rem_4.5rem_4.5rem_1rem] items-center gap-3";

export function LiveStateStrip({ app, env, range, links }: { app: string; env: string; range: RangePreset; links: LiveStateLinks }) {
  const live = useAppLiveState(app, env, range);
  const showEnv = live.scopedEnv === null;
  const loading = live.envsLoading || (live.listed.isLoading && !live.listed.data);
  const windowWords = RANGE_LABELS[range].toLowerCase();
  return (
    <section className="min-w-0 space-y-3" data-testid="overview-live-state">
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="min-w-0 text-section text-ink">
          Live state
          {!loading && <span className="ml-2 text-meta font-normal text-ink-muted">{live.rows.length}{live.attention > 0 ? ` · ${live.attention} need attention` : ""}</span>}
        </h2>
        <Link href={links.services} className="no-color inline-flex items-center gap-1 text-ui text-ink-muted hover:text-ink">
          All services <ChevronRight className="size-3.5" aria-hidden="true" />
        </Link>
      </div>
      {loading ? (
        <div className="h-32 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading live state" />
      ) : live.rows.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted">Nothing has reported in the {windowWords}. Services appear once Reliant sees their pods.</p>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border bg-surface">
          <div className="min-w-[52rem]" role="table" aria-label="Live state">
            <div role="row" className={cn(GRID, "border-b border-border px-4 py-2 text-meta text-ink-muted")}>
              <span role="columnheader">Service</span>
              <span role="columnheader">Status</span>
              <span role="columnheader" className="text-right">Ready</span>
              <span role="columnheader">CPU</span>
              <span role="columnheader">Memory</span>
              <span role="columnheader" className="text-right">Req/s</span>
              <span role="columnheader" className="text-right">p95</span>
              <span role="columnheader" className="text-right">Errors</span>
              <span role="columnheader" />
            </div>
            {live.rows.map((row) => (
              <StripRow key={row.key} row={row} href={links.service(row.name, row.envName)} showEnv={showEnv} env={live.envs.find((candidate) => candidate.name === row.envName)} live={live} />
            ))}
          </div>
        </div>
      )}
      <p className="text-meta text-ink-muted">Over the {windowWords}. CPU and memory are each container&apos;s busiest instance, added up.</p>
    </section>
  );
}

function StripRow({ row, href, showEnv, env, live }: { row: LiveStateRow; href: string; showEnv: boolean; env: LiveStateEnv | undefined; live: { orgId: string | null; app: string; range: RangePreset } }) {
  const dash = <span className="text-ink-muted">—</span>;
  const health = row.type === "workload" ? row.health : undefined;
  return (
    <Link
      href={href}
      role="row"
      className={cn(GRID, "no-color border-b border-border-subtle px-4 py-2.5 text-ui text-ink last:border-b-0 hover:bg-ink/5")}
      data-testid={`live-state-row-${row.key}`}
    >
      <span role="cell" className="flex min-w-0 items-center gap-2">
        <span className="min-w-0 truncate font-medium">{row.name}</span>
        <span className="shrink-0 text-meta text-ink-muted">{row.kindLabel}</span>
        {showEnv && <span className="shrink-0 text-meta text-ink-muted">· {row.envName}</span>}
      </span>
      <span role="cell" className="min-w-0 truncate">
        {row.statusNote ? <span className="text-ink-muted">{row.statusNote}</span> : <Status tone={row.tone} label={row.status} />}
      </span>
      <span role="cell" className={cn("text-right tabular-nums", row.type === "workload" && row.short && "text-warning-ink")}>{row.type === "workload" ? row.ready : dash}</span>
      {row.type === "workload" && env ? (
        <UsageCells name={row.name} env={env} live={live} cpuNow={row.cpu} memNow={row.memory} />
      ) : (
        <>
          <span role="cell">{dash}</span>
          <span role="cell">{dash}</span>
        </>
      )}
      <span role="cell" className="text-right tabular-nums">{health ? formatRate(health.requestRate) : dash}</span>
      <span role="cell" className="text-right tabular-nums">{health ? formatDuration(health.p95Ms) : dash}</span>
      <span role="cell" className={cn("text-right tabular-nums", health && health.errorRate >= 0.05 && "text-warning-ink")}>{health ? formatPercent(health.errorRate) : dash}</span>
      <ChevronRight role="cell" className="size-4 text-ink-muted" aria-hidden="true" />
    </Link>
  );
}

function UsageCells({ name, env, live, cpuNow, memNow }: { name: string; env: LiveStateEnv; live: { orgId: string | null; app: string; range: RangePreset }; cpuNow: number | null; memNow: number | null }) {
  const scope = useMemo<ObsScope | null>(() => (live.orgId ? { orgId: live.orgId, projectId: live.app, envIds: [env.id], filters: [] } : null), [live.orgId, live.app, env.id]);
  const usage = useWorkloadUsage(scope, live.range, name, env.id);
  return (
    <>
      <span role="cell" className="flex min-w-0 items-center gap-2">
        <Sparkline values={usage.cpu?.values ?? []} label={`CPU of ${name}`} />
        <span className="truncate text-meta tabular-nums text-ink-muted">{cpuNow === null ? "—" : formatCores(cpuNow)}</span>
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-2">
        <Sparkline values={usage.memory?.values ?? []} label={`Memory of ${name}`} />
        <span className="truncate text-meta tabular-nums text-ink-muted">{memNow === null ? "—" : formatBytes(memNow)}</span>
      </span>
    </>
  );
}
