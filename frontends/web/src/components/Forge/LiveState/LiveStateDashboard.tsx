// Copyright (c) 2026 Reliant Labs

/**
 * The built-in LIVE STATE dashboard (U6c, owner 2026-11-20):
 * /forge/p/:app/observability/dashboards/live-state. Listed on Dashboards
 * beside the custom ones; one section per workload with its charts —
 * instances, requests, latency, errors, CPU, memory — then the database and
 * the sites as rows. Read from useAppLiveState, the hook the Services tab and
 * the Overview's strip read, so the figures agree everywhere.
 */

import { useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { Header, NothingDeployed, Page } from "../Console/consolePageFrame";
import { VariableSelect } from "../Observability/Dashboards/parts";
import { RedCharts } from "../Services/ServiceDetail";
import { OVERVIEW_RANGE } from "../Workloads/workloadModel";
import type { LiveStateEnv, LiveStateRow } from "./liveStateModel";
import { useAppLiveState, useWorkloadUsage } from "./useAppLiveState";

export function LiveStateDashboardPage() {
  const { forgeProject, env, href } = useForgeConsole();
  const search = useSearch({ strict: false }) as { range?: RangePreset };
  const navigate = useNavigate();
  const range = search.range ?? OVERVIEW_RANGE;
  const live = useAppLiveState(forgeProject, env, range);
  const loading = live.envsLoading || (live.listed.isLoading && !live.listed.data);
  const workloads = live.rows.filter((row): row is Extract<LiveStateRow, { type: "workload" }> => row.type === "workload");
  const others = live.rows.filter((row) => row.type !== "workload");
  const showEnv = live.scopedEnv === null;

  if (!live.envsLoading && live.envs.length === 0) {
    return (
      <Page testId="console-live-state">
        <Header title="Live state" />
        <NothingDeployed what="Live state" about="Live state charts every service's instances, CPU, memory, requests, latency and errors." />
      </Page>
    );
  }

  return (
    <Page testId="console-live-state">
      <PageHeader
        density="compact"
        className="mb-0"
        title="Live state"
        subtitle="Built in · every service's instances, requests, latency, errors, CPU and memory"
        meta={loading ? undefined : <span>{workloads.length} services{live.attention > 0 ? ` · ${live.attention} need attention` : ""}</span>}
        controls={
          <VariableSelect
            id="live-state-range"
            label="time"
            value={range}
            options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
            onChange={(value) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, range: value === OVERVIEW_RANGE ? undefined : (value as RangePreset) }), replace: true } as never)}
          />
        }
      />
      {loading ? (
        <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading live state" />
      ) : workloads.length === 0 ? (
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
            href={href({ kind: "service", name: row.name }, showEnv ? { env: row.envName } : {})}
          />
        ))
      )}
      {others.length > 0 && (
        <section className="space-y-2" data-testid="live-state-others">
          <h2 className="text-section text-ink">Jobs, sites and databases</h2>
          <ul className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
            {others.map((row) => (
              <li key={row.key} className="flex items-center gap-3 px-4 py-2.5 text-ui">
                <Link href={href({ kind: "service", name: row.name }, showEnv ? { env: row.envName } : {})} className="no-color font-medium text-ink hover:underline">
                  {row.name}
                </Link>
                <span className="text-meta text-ink-muted">{row.kindLabel}{showEnv ? ` · ${row.envName}` : ""}</span>
                <span className="flex-1" />
                {row.statusNote ? <span className="text-ink-muted">{row.statusNote}</span> : <Status tone={row.tone} label={row.status} />}
              </li>
            ))}
          </ul>
        </section>
      )}
    </Page>
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
  row: Extract<LiveStateRow, { type: "workload" }>;
  env: LiveStateEnv | undefined;
  showEnv: boolean;
  orgId: string | null;
  app: string;
  range: RangePreset;
  href: string;
}) {
  const scope = useMemo<ObsScope | null>(() => (orgId && env ? { orgId, projectId: app, envIds: [env.id], filters: [] } : null), [orgId, app, env]);
  const usage = useWorkloadUsage(scope, range, row.name, env?.id);
  return (
    <section className="space-y-3" data-testid={`live-state-service-${row.key}`}>
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
