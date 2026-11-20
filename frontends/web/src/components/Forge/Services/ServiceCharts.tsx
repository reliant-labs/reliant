// Copyright (c) 2026 Reliant Labs

/**
 * SERVICES › CHARTS (U6e, owner 2026-11-20: Services and Live state were
 * "kind of similar", so they are one live view). `?view=charts` on
 * /forge/p/:app/services: one section per workload with its charts —
 * instances, requests, latency, errors, CPU, memory — then the jobs, sites
 * and databases as rows (a database row opens Data, the database's home).
 *
 * This is what the retired built-in Live state dashboard showed; its URL
 * (/observability/dashboards/live-state) now redirects here. Read from
 * useAppLiveState, the hook the table view and the Overview's strip read, so
 * the figures agree everywhere.
 */

import { useMemo } from "react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { useWorkloadUsage, type AppLiveState } from "../LiveState/useAppLiveState";
import type { LiveStateRow } from "../LiveState/liveStateModel";
import { RedCharts } from "./ServiceDetail";

type WorkloadRow = Extract<LiveStateRow, { type: "workload" }>;
/** An environment as the charts address it: its id for the reads, its name for the rows. */
type ChartsEnv = { id: string; name: string };
/** What the charts read of the live state (the Services page's envs are the strip's, a superset). */
export type ChartsLiveState = Pick<AppLiveState, "rows" | "scopedEnv" | "orgId"> & { envs: readonly ChartsEnv[] };

export function ServiceCharts({ live, loading, range }: { live: ChartsLiveState; loading: boolean; range: RangePreset }) {
  const { forgeProject, href } = useForgeConsole();
  const workloads = live.rows.filter((row): row is WorkloadRow => row.type === "workload");
  const others = live.rows.filter((row) => row.type !== "workload");
  const showEnv = live.scopedEnv === null;
  const rowHref = (row: LiveStateRow) =>
    row.type === "database" ? href({ kind: "data" }, { env: row.envName }) : href({ kind: "service", name: row.name }, showEnv ? { env: row.envName } : {});

  if (loading) {
    return <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading charts" />;
  }
  return (
    <div className="space-y-8" data-testid="services-charts">
      {workloads.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted">Nothing has reported in this scope yet. Services appear once Reliant sees their pods.</p>
      ) : (
        workloads.map((row) => (
          <ServiceSection
            key={row.key}
            row={row}
            env={live.envs.find((candidate) => candidate.name === row.envName)}
            showEnv={showEnv}
            orgId={live.orgId}
            app={forgeProject}
            range={range}
            href={rowHref(row)}
          />
        ))
      )}
      {others.length > 0 && (
        <section className="space-y-2" data-testid="services-charts-others">
          <h2 className="text-section text-ink">Jobs, sites and databases</h2>
          <ul className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
            {others.map((row) => (
              <li key={row.key} className="flex items-center gap-3 px-4 py-2.5 text-ui">
                <Link href={rowHref(row)} className="no-color font-medium text-ink hover:underline">
                  {row.name}
                </Link>
                <span className="text-meta text-ink-muted">
                  {row.kindLabel}
                  {showEnv ? ` · ${row.envName}` : ""}
                </span>
                <span className="flex-1" />
                {row.statusNote ? <span className="text-ink-muted">{row.statusNote}</span> : <Status tone={row.tone} label={row.status} />}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function ServiceSection({
  row,
  env,
  showEnv,
  orgId,
  app,
  range,
  href,
}: {
  row: WorkloadRow;
  env: ChartsEnv | undefined;
  showEnv: boolean;
  orgId: string | null;
  app: string;
  range: RangePreset;
  href: string;
}) {
  const scope = useMemo<ObsScope | null>(() => (orgId && env ? { orgId, projectId: app, envIds: [env.id], filters: [] } : null), [orgId, app, env]);
  const usage = useWorkloadUsage(scope, range, row.name, env?.id);
  return (
    <section className="space-y-3" data-testid={`services-charts-${row.key}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className="text-section text-ink">
          <Link href={href} className="no-color hover:underline">
            {row.name}
          </Link>
        </h2>
        <Status tone={row.tone} label={row.status} />
        <span className="text-meta text-ink-muted">
          {row.kindLabel}
          {showEnv ? ` · ${row.envName}` : ""}
        </span>
      </div>
      <RedCharts
        scope={scope}
        range={range}
        name={row.name}
        cpu={usage.cpu}
        memory={usage.memory}
        cpuNow={row.cpu}
        memNow={row.memory}
        loadingUsage={usage.loading}
        instances={{ ready: row.ready, pods: usage.pods.length ? usage.pods : (row.workload.pods ?? []) }}
      />
    </section>
  );
}
