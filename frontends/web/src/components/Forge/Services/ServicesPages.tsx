// Copyright (c) 2026 Reliant Labs

/**
 * SERVICES (U6b round 3, prototype): everything the app runs, live —
 * /forge/p/:app/services and /forge/p/:app/services/:name.
 *
 *   list     one row per workload (Deployment, StatefulSet, CronJob, Job),
 *            the static site and the managed database: status, ready/desired,
 *            CPU and memory (each container's worst replica, summed), req/s,
 *            p95, error rate, and the release it runs.
 *   detail   ServiceDetail.tsx: instances, charts, custom metrics, logs and
 *            traces, recent deploys; the database's own page.
 *
 * Read from what the Overview already reads (ListWorkloads, GetServiceOverview,
 * GetStatus), so the two pages agree. The Overview keeps a compact strip of
 * the same rows and links here.
 */

import type { ReactNode } from "react";
import { useParams, useSearch } from "@tanstack/react-router";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { Header, NothingDeployed, Page } from "../Console/consolePageFrame";
import { ENV_ALL } from "../Console/envScope";
import { PAGE_SUBTITLES } from "../Console/groupedNav";
import { formatBytes, formatCores, formatDuration, formatPercent, formatRate } from "../Observability/format";
import { ObsAsOf, ObsError } from "../Observability/ObsStates";
import { shortVersion } from "../Releases/releaseModel";
import { OVERVIEW_RANGE } from "../Workloads/workloadModel";
import { ServiceDetail } from "./ServiceDetail";
import type { ServicesRow } from "./servicesModel";
import { useServicesData, type ServicesSearch } from "./useServicesData";

export function ServicesPage() {
  const { forgeProject, href } = useForgeConsole();
  const search = useSearch({ strict: false }) as ServicesSearch;
  const range = search.range ?? OVERVIEW_RANGE;
  const data = useServicesData(range);
  const { env, envs, envsLoading, listed, red, rows } = data;
  const all = env === ENV_ALL;
  if (!envsLoading && envs.length === 0) {
    return (
      <Page testId="console-services">
        <Header title="Services" subtitle={PAGE_SUBTITLES.services} />
        <NothingDeployed what="Services" about="Services lists every service, worker, job, site and database the app runs, with its status, instances, CPU, memory and requests." />
      </Page>
    );
  }
  const loading = envsLoading || listed.isLoading;
  const attention = rows.filter((row) => row.tone === "danger" || row.tone === "warning").length;
  return (
    <Page testId="console-services">
      <Header
        title="Services"
        subtitle={PAGE_SUBTITLES.services}
        meta={
          loading ? undefined : (
            <>
              <span data-testid="services-counts">
                {rows.length} running in {all ? "all environments" : env}
              </span>
              {attention > 0 && <Status tone="warning" label={`${attention} need${attention === 1 ? "s" : ""} attention`} />}
              {listed.data && <ObsAsOf updatedAt={listed.dataUpdatedAt} />}
            </>
          )
        }
      />
      {listed.error && !listed.data ? (
        <ObsError error={listed.error} subject={forgeProject} onRetry={() => void listed.refetch()} retrying={listed.isFetching} />
      ) : (
        <ServicesTable rows={rows} loading={loading} redLoading={red.isLoading} showEnv={all} hrefOf={(row) => href({ kind: "service", name: row.name }, all ? {} : { env: row.envName })} releaseHref={(version) => href({ kind: "release", version })} />
      )}
      <p className="text-meta text-ink-muted">
        CPU and memory are each container&apos;s busiest instance, added up over the containers. Requests, p95 and errors are over the last {range}. A worker shows as a
        Deployment that serves no requests: the kind is Kubernetes&apos;, as the platform reports it.
      </p>
    </Page>
  );
}

const TH = "h-[var(--table-head-h,2.25rem)] px-3 text-left text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-[52px] px-3 align-middle text-ui tabular-nums whitespace-nowrap";

export function ServicesTable({
  rows,
  loading,
  redLoading,
  showEnv,
  hrefOf,
  releaseHref,
}: {
  rows: ServicesRow[];
  loading: boolean;
  redLoading: boolean;
  showEnv: boolean;
  hrefOf: (row: ServicesRow) => string;
  releaseHref: (version: string) => string;
}) {
  if (loading && rows.length === 0) {
    return <div className="h-48 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading services" />;
  }
  if (rows.length === 0) {
    return <p className="rounded-xl border border-dashed border-border px-4 py-6 text-ui text-ink-muted">Nothing has reported in this scope yet. Workloads appear once Reliant sees their pods.</p>;
  }
  const dash = <span className="text-ink-muted">—</span>;
  const figure = (value: ReactNode, pending: boolean) => (pending ? <span className="inline-block h-3 w-10 animate-pulse rounded bg-ink/5" /> : value);
  return (
    <div className="overflow-x-auto rounded-xl border border-border bg-surface" data-testid="services-table">
      <table className="w-full min-w-[60rem] border-collapse">
        <thead>
          <tr className="border-b border-border">
            <th className={TH}>Name</th>
            <th className={TH}>Status</th>
            <th className={cn(TH, "text-right")}>Ready</th>
            <th className={cn(TH, "text-right")} title="Each container's busiest instance, added up over the containers">
              CPU
            </th>
            <th className={cn(TH, "text-right")} title="Working set: each container's busiest instance, added up over the containers">
              Memory
            </th>
            <th className={cn(TH, "text-right")}>Req/s</th>
            <th className={cn(TH, "text-right")}>p95</th>
            <th className={cn(TH, "text-right")}>Errors</th>
            <th className={TH}>Release</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const health = row.type === "workload" ? row.health : undefined;
            return (
              <tr key={row.key} className="border-b border-border-subtle last:border-b-0 hover:bg-ink/5" data-testid={`services-row-${row.name}`} data-type={row.type}>
                <td className={cn(TD, "max-w-0 min-w-56")}>
                  <div className="flex min-w-0 items-center gap-2">
                    <Link href={hrefOf(row)} className="no-color truncate font-medium text-ink hover:underline">
                      {row.name}
                    </Link>
                    <span className="shrink-0 text-meta text-ink-muted">{row.kindLabel}</span>
                    {showEnv && <Tag>{row.envName}</Tag>}
                  </div>
                  {row.type === "site" && row.url && (
                    <a href={row.url} target="_blank" rel="noreferrer" className="block truncate text-meta text-ink-muted hover:underline">
                      {row.url.replace(/^https?:\/\//, "")}
                    </a>
                  )}
                  {row.type === "workload" && row.workload.restartsInWindow > 0 && (
                    <span className="block truncate text-meta text-ink-muted" title={row.workload.healthReason || undefined}>
                      {row.workload.restartsInWindow} restarts in the window
                    </span>
                  )}
                </td>
                <td className={TD}>
                  <Status tone={row.tone} label={row.status} />
                </td>
                <td className={cn(TD, "text-right", row.type === "workload" && row.short && "text-warning-ink")}>{row.type === "workload" ? row.ready : dash}</td>
                <td className={cn(TD, "text-right")} title={row.type === "workload" ? perContainer(row, "cpu") : undefined}>
                  {row.type === "workload" && row.cpu !== null ? formatCores(row.cpu) : dash}
                  {row.type === "workload" && row.containers.length > 1 && <span className="block text-meta text-ink-muted">{row.containers.length} containers</span>}
                </td>
                <td className={cn(TD, "text-right")} title={row.type === "workload" ? perContainer(row, "memory") : undefined}>
                  {row.type === "workload" && row.memory !== null ? formatBytes(row.memory) : dash}
                </td>
                <td className={cn(TD, "text-right")}>{row.type === "workload" ? figure(health ? formatRate(health.requestRate) : dash, redLoading) : dash}</td>
                <td className={cn(TD, "text-right")}>{row.type === "workload" ? figure(health ? formatDuration(health.p95Ms) : dash, redLoading) : dash}</td>
                <td className={cn(TD, "text-right", health && health.errorRate >= 0.05 && "text-warning-ink")}>
                  {row.type === "workload" ? figure(health ? formatPercent(health.errorRate) : dash, redLoading) : dash}
                </td>
                <td className={TD}>
                  {row.release ? (
                    <Link href={releaseHref(row.release)} className="no-color text-ink-muted hover:text-ink hover:underline" title={row.release}>
                      {shortVersion(row.release)}
                    </Link>
                  ) : (
                    dash
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function perContainer(row: Extract<ServicesRow, { type: "workload" }>, what: "cpu" | "memory"): string {
  const parts = row.containers.map((c) => `${c.container}: ${c[what] === null ? "—" : what === "cpu" ? formatCores(c[what]!) : formatBytes(c[what]!)}`);
  return `Per container (busiest instance): ${parts.join(" · ")}`;
}

/** /services/:name. */
export function ServicePage() {
  const { name = "" } = useParams({ strict: false }) as { name?: string };
  const search = useSearch({ strict: false }) as ServicesSearch;
  return <ServiceDetail key={name} name={name} search={search} />;
}
