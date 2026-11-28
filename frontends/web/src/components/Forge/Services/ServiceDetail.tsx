// Copyright (c) 2026 Reliant Labs

/**
 * ONE SERVICE (U6b round 3, prototype): /forge/p/:app/services/:name.
 *
 *   header      status, kind, ready/desired, the release, the environment
 *               (one at a time: the scope's, else the first that runs it),
 *               and the ways out: logs and traces filtered to it, the
 *               workload page (restart, config, events).
 *   charts      requests, p95 latency, errors, CPU, memory: one headline
 *               number and one chart per card.
 *   instances   GetWorkload's pods: state, restarts, last exit, age (a lower
 *               bound when the pod is older than the window), node. CPU and
 *               memory are not reported per instance, so they are per
 *               container under the table.
 *   metrics     the app's own metrics (ListMetrics, declared or custom),
 *               one charted (the picked one, else the first).
 *   deploys     the environment's recent promotions.
 *
 * A static site has a page of its own shape (SiteDetail.tsx). A database's page is
 * Data, the database's home (U6e, owner: "the DB page from Services and
 * Data should be the same place"): /services/<database> redirects there,
 * keeping the environment.
 */

import { useEffect, useMemo, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ArrowRight, ScrollText, Waypoints } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useMetricCatalog, usePanelQuery, useWorkload } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import type { MetricCatalogEntry } from "@/services/controlPlane/observability/metricsTypes";
import type { ObsScope, Workload, WorkloadPod } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { Header, Page } from "../Console/consolePageFrame";
import { ENV_ALL } from "../Console/envScope";
import { UPlotChart } from "../Observability/charts/UPlotChart";
import { unionTimestamps, alignTo } from "../Observability/Dashboards/seriesModel";
import { formatBytes, formatCores, formatCount, formatDuration, formatPercent } from "../Observability/format";
import { ratioSeries, redPanel, seriesValues } from "../Observability/Home/homeModel";
import { useExploreHref } from "../Observability/Metrics/pivots";
import { shortVersion } from "../Releases/releaseModel";
import { formatAge, KIND_LABEL, OVERVIEW_RANGE } from "../Workloads/workloadModel";
import { HealthStatus } from "../Workloads/workloadParts";
import type { StripEnv } from "../Workloads/workloadModel";
import { useWorkloadUsage } from "../LiveState/useAppLiveState";
import { useServicesData, type ServicesSearch } from "./useServicesData";
import { RecentDeploys } from "./RecentDeploys";
import { MetricCard, MetricLine, Section } from "./serviceParts";
import { containerFigures, customMetrics, databasesOf, metricPanel, sitesOf } from "./servicesModel";
import { SiteDetail } from "./SiteDetail";

export function ServiceDetail({ name, search }: { name: string; search: ServicesSearch }) {
  const range = search.range ?? OVERVIEW_RANGE;
  const data = useServicesData(range);
  const { env, envs, hosted, listed } = data;
  const databases = databasesOf(hosted, name);
  const sites = sitesOf(hosted, name);
  const isWorkload = (listed.data?.workloads ?? []).some((w) => w.workload === name);
  if (databases.length > 0 && !isWorkload) return <DatabaseRedirect entries={databases} />;
  // Not a workload: wait for GetStatus, which says whether it is the database or a site.
  if (!isWorkload && data.hostedLoading) return <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading" />;
  if (sites.length > 0) return <SiteDetail name={name} entries={sites} envs={envs} />;
  // The environment: the scope's; on All, the first (prod leads) that runs it.
  const runsIn = (candidate: StripEnv) => (listed.data?.workloads ?? []).some((w) => w.workload === name && w.envName === candidate.name);
  const target = env === ENV_ALL ? (envs.find((candidate) => !candidate.preview && runsIn(candidate)) ?? envs.find(runsIn) ?? null) : (envs.find((candidate) => candidate.name === env) ?? null);
  const listedSummary = (listed.data?.workloads ?? []).find((w) => w.workload === name && w.envName === target?.name) ?? null;
  const elsewhere = envs.filter((candidate) => candidate.name !== target?.name && runsIn(candidate));
  if (data.envsLoading || (listed.isLoading && !listed.data)) return <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading" />;
  if (!target) {
    return (
      <Page testId="console-service">
        <Header title={name} />
        <p className="text-ui text-ink-muted">Nothing named {name} has reported from {env === ENV_ALL ? "any environment" : env} in the window.</p>
      </Page>
    );
  }
  return <WorkloadServiceDetail key={target.id} name={name} env={target} elsewhere={elsewhere} listedSummary={listedSummary} range={range} search={search} orgId={data.orgId} />;
}

function WorkloadServiceDetail({
  name,
  env,
  elsewhere,
  listedSummary,
  range,
  search,
  orgId,
}: {
  name: string;
  env: StripEnv;
  elsewhere: StripEnv[];
  listedSummary: Workload | null;
  range: RangePreset;
  search: ServicesSearch;
  orgId: string | null | undefined;
}) {
  const { forgeProject, href } = useForgeConsole();
  const scope = useMemo<ObsScope | null>(() => (orgId ? { orgId, projectId: forgeProject, envIds: [env.id], filters: [] } : null), [orgId, forgeProject, env.id]);
  // The live state's per-workload read (LiveState/useAppLiveState.ts): the same entry the Overview's strip and Services' charts read.
  const detail = useWorkload(scope, range, name);
  const usage = useWorkloadUsage(scope, range, name, env.id);
  const summary = detail.data?.workloads.find((w) => w.envId === env.id) ?? listedSummary;
  const pods = usage.pods.length ? usage.pods : (summary?.pods ?? []);
  const containers = containerFigures(summary?.containers ?? []);
  const obs = (view: "logs" | "traces", extra: Record<string, unknown> = {}) => href({ kind: "obs-view", view }, { env: env.name, service: name, range, ...extra });

  return (
    <Page testId="console-service">
      <Header
        title={name}
        meta={
          summary ? (
            <>
              <HealthStatus workload={summary} />
              <span>{KIND_LABEL[summary.kind] || "Workload"}</span>
              <span className="tabular-nums">{summary.desiredReplicas !== null ? `${summary.readyReplicas ?? 0}/${summary.desiredReplicas} ready` : `${summary.readyReplicas ?? 0} ready`}</span>
              {(summary.releaseVersion || env.release) && <span title={summary.releaseVersion || env.release}>{shortVersion(summary.releaseVersion || env.release)}</span>}
              <Tag>{env.name}</Tag>
            </>
          ) : (
            <Tag>{env.name}</Tag>
          )
        }
      />
      <nav aria-label={`${name} links`} className="flex flex-wrap items-center gap-x-4 gap-y-1 text-ui" data-testid="service-links">
        <IconLink href={obs("logs")} icon={<ScrollText className="size-3.5" />}>
          Logs
        </IconLink>
        <IconLink href={obs("traces")} icon={<Waypoints className="size-3.5" />}>
          Traces
        </IconLink>
        <IconLink href={withEnv(href({ kind: "workload", env: env.name, name }), env.name)} icon={<ArrowRight className="size-3.5" />}>
          Restart, config and events
        </IconLink>
        {elsewhere.length > 0 && (
          <span className="text-ink-muted">
            Also in{" "}
            {elsewhere.map((candidate, i) => (
              <span key={candidate.id}>
                {i > 0 && ", "}
                <Link href={href({ kind: "service", name }, { env: candidate.name })} className="no-color text-ink hover:underline">
                  {candidate.name}
                </Link>
              </span>
            ))}
          </span>
        )}
      </nav>
      {summary?.healthReason && summary.health !== "healthy" && <p className="text-ui text-ink">{summary.healthReason}</p>}

      <RedCharts scope={scope} range={range} name={name} cpu={usage.cpu} memory={usage.memory} cpuNow={sum(containers.map((c) => c.cpu))} memNow={sum(containers.map((c) => c.memory))} loadingUsage={usage.loading} />

      <Section title="Instances" meta={pods.length ? `${pods.length} in the ${RANGE_LABELS[range].toLowerCase()}` : undefined} testId="service-instances">
        {detail.isLoading ? (
          <div className="h-24 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" />
        ) : pods.length === 0 ? (
          <p className="text-ui text-ink-muted">No instance reported in the window.</p>
        ) : (
          <InstancesTable pods={pods} />
        )}
        {containers.length > 0 && (
          <div className="space-y-2 pt-2" data-testid="service-containers">
            <h3 className="text-ui font-medium text-ink">Per container (all instances)</h3>
            <p className="text-meta text-ink-muted">CPU and memory are not reported per instance yet: each figure is the busiest instance of that container.</p>
            <ContainersTable workload={summary!} />
          </div>
        )}
      </Section>

      <CustomMetrics scope={scope} range={range} name={name} picked={search.metric} env={env.name} />

      <RecentDeploys env={env} name={name} />
    </Page>
  );
}

function withEnv(path: string, env: string): string {
  return `${path}${path.includes("?") ? "&" : "?"}env=${encodeURIComponent(env)}`;
}

function sum(values: readonly (number | null)[]): number | null {
  return values.some((v) => v !== null) ? values.reduce<number>((acc, v) => acc + (v ?? 0), 0) : null;
}

function IconLink({ href, icon, children }: { href: string; icon: ReactNode; children: ReactNode }) {
  return (
    <Link href={href} className="no-color inline-flex items-center gap-1.5 text-primary-ink hover:underline">
      {icon}
      {children}
    </Link>
  );
}

// ── Charts: one headline number and one chart per card (serviceParts) ────────

export function RedCharts({
  scope,
  range,
  name,
  cpu,
  memory,
  cpuNow,
  memNow,
  loadingUsage,
  instances,
}: {
  /** The instances card: ready of desired, and the instances seen in the window. */
  instances?: { ready: string; pods: readonly WorkloadPod[] };
  scope: ObsScope | null;
  range: RangePreset;
  name: string;
  cpu: { timestamps: number[]; values: (number | null)[] } | null;
  memory: { timestamps: number[]; values: (number | null)[] } | null;
  cpuNow: number | null;
  memNow: number | null;
  loadingUsage: boolean;
}) {
  const linePanel = useMemo(() => redPanel("line"), []);
  const numberPanel = useMemo(() => redPanel("number"), []);
  const services = useMemo(() => [name], [name]);
  const trend = usePanelQuery(scope, range, linePanel, services);
  const totals = usePanelQuery(scope, range, numberPanel, services);
  const series = trend.data?.series ?? [];
  const timestamps = unionTimestamps(series);
  const aligned = (label: string) => {
    const s = series.find((candidate) => candidate.label === label);
    return s ? alignTo(timestamps, s) : seriesValues(series, label);
  };
  const requests = aligned("requests");
  const errors = aligned("errors");
  const p95 = aligned("p95");
  const total = (label: string) => totals.data?.series.find((s) => s.label === label)?.value ?? null;
  const totalRequests = total("requests");
  const totalErrors = total("errors");
  const errorRate = totalRequests ? (totalErrors ?? 0) / totalRequests : null;
  const p95Now = total("p95");
  const noTraffic = !trend.isLoading && (!totalRequests || timestamps.length < 2) ? `${name} served no traced requests in the window.` : null;
  const window = RANGE_LABELS[range].toLowerCase();
  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3" data-testid="service-charts">
      {instances && (
        <MetricCard title="Instances" figure={instances.ready} detail={`ready · ${instances.pods.length} seen in the ${window}`} loading={loadingUsage} empty={instances.pods.length === 0 ? "No instance reported in the window." : null} testId="service-chart-instances">
          <ul className="h-[110px] space-y-1 overflow-y-auto px-2 text-meta">
            {instances.pods.map((pod) => (
              <li key={pod.name} className="flex items-center gap-2">
                <Status tone={POD_STATE[pod.state].tone} label={POD_STATE[pod.state].label} hideLabel />
                <span className="min-w-0 flex-1 truncate font-mono text-ink">{pod.name}</span>
                {pod.restarts > 0 && <span className="shrink-0 tabular-nums text-warning-ink">{pod.restarts} restarts</span>}
              </li>
            ))}
          </ul>
        </MetricCard>
      )}
      <MetricCard title="Requests" figure={totalRequests === null ? "—" : formatCount(totalRequests)} detail={window} loading={totals.isLoading} empty={noTraffic} testId="service-chart-requests">
        <MetricLine timestamps={timestamps} values={requests} unit="count" label={`Requests to ${name}`} />
      </MetricCard>
      <MetricCard title="Latency (p95)" figure={p95Now === null ? "—" : formatDuration(p95Now)} detail={window} loading={totals.isLoading} empty={noTraffic} testId="service-chart-latency">
        <MetricLine timestamps={timestamps} values={p95} unit="ms" label={`p95 latency of ${name}`} />
      </MetricCard>
      <MetricCard
        title="Errors"
        figure={errorRate === null ? "—" : formatPercent(errorRate)}
        detail={totalErrors === null ? window : `${formatCount(totalErrors)} failed · ${window}`}
        tone={errorRate !== null && errorRate >= 0.05 ? "warning" : undefined}
        loading={totals.isLoading}
        empty={noTraffic}
        testId="service-chart-errors"
      >
        <MetricLine timestamps={timestamps} values={ratioSeries(errors, requests)} unit="percent" label={`Error rate of ${name}`} tone="destructive" />
      </MetricCard>
      <MetricCard title="CPU" figure={cpuNow === null ? "—" : formatCores(cpuNow)} detail="now, all containers" loading={loadingUsage} empty={!cpu || cpu.timestamps.length < 2 ? "No CPU samples in the window." : null} testId="service-chart-cpu">
        {cpu && <MetricLine timestamps={cpu.timestamps} values={cpu.values} unit="cores" label={`CPU of ${name}`} />}
      </MetricCard>
      <MetricCard title="Memory" figure={memNow === null ? "—" : formatBytes(memNow)} detail="now, all containers" loading={loadingUsage} empty={!memory || memory.timestamps.length < 2 ? "No memory samples in the window." : null} testId="service-chart-memory">
        {memory && <MetricLine timestamps={memory.timestamps} values={memory.values} unit="bytes" label={`Memory of ${name}`} />}
      </MetricCard>
    </div>
  );
}

// ── Instances ───────────────────────────────────────────────────────────────

const TH = "h-9 px-3 text-left text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-11 px-3 text-ui tabular-nums whitespace-nowrap";

const POD_STATE: Record<WorkloadPod["state"], { tone: "success" | "neutral"; label: string }> = {
  running: { tone: "success", label: "Running" },
  gone: { tone: "neutral", label: "Gone" },
  unknown: { tone: "neutral", label: "Unknown" },
};

function InstancesTable({ pods }: { pods: WorkloadPod[] }) {
  const now = Date.now();
  return (
    <div className="overflow-x-auto rounded-xl border border-border bg-surface">
      <table className="w-full border-collapse" data-testid="service-instances-table">
        <thead>
          <tr className="border-b border-border">
            <th className={TH}>Instance</th>
            <th className={TH}>State</th>
            <th className={cn(TH, "text-right")}>Restarts</th>
            <th className={TH}>Last exit</th>
            <th className={cn(TH, "text-right")}>Age</th>
            <th className={TH}>Node</th>
          </tr>
        </thead>
        <tbody>
          {pods.map((pod) => {
            const crashing = pod.restarts > 0 && pod.state === "running" && !!pod.latestWarning && /BackOff/i.test(pod.latestWarning.reason);
            const state = crashing ? { tone: "danger" as const, label: "Restarting" } : POD_STATE[pod.state];
            return (
              <tr key={pod.uid || pod.name} className="border-b border-border-subtle last:border-b-0" data-testid={`service-pod-${pod.name}`}>
                <td className={cn(TD, "font-mono text-code text-ink")}>{pod.name}</td>
                <td className={TD}>
                  <Status tone={state.tone} label={state.label} />
                  {crashing && pod.latestWarning?.plainReason && <span className="ml-2 text-meta text-ink-muted">{pod.latestWarning.plainReason}</span>}
                </td>
                <td className={cn(TD, "text-right", pod.restarts > 0 && "text-warning-ink")}>{pod.restarts}</td>
                <td className={cn(TD, "text-ink-muted")}>{pod.lastExitReason ? `${pod.lastExitReason}${pod.lastExitAtMs ? ` · ${formatAge(now - pod.lastExitAtMs)} ago` : ""}` : "—"}</td>
                <td className={cn(TD, "text-right text-ink-muted")} title={pod.ageIsLowerBound ? "Older than the window: at least this old" : undefined}>
                  {pod.firstSeenMs ? `${pod.ageIsLowerBound ? "≥ " : ""}${formatAge(now - pod.firstSeenMs)}` : "—"}
                </td>
                <td className={cn(TD, "font-mono text-code text-ink-muted")}>{pod.node || "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ContainersTable({ workload }: { workload: Workload }) {
  const pct = (fraction: number | null) => (fraction === null ? "" : ` (${formatPercent(fraction)} of limit)`);
  return (
    <div className="overflow-x-auto rounded-xl border border-border bg-surface">
      <table className="w-full border-collapse">
        <thead>
          <tr className="border-b border-border">
            <th className={TH}>Container</th>
            <th className={TH}>Image</th>
            <th className={cn(TH, "text-right")}>CPU now</th>
            <th className={cn(TH, "text-right")}>CPU p95</th>
            <th className={cn(TH, "text-right")}>Memory now</th>
            <th className={cn(TH, "text-right")}>Restarts</th>
            <th className={TH}>Last terminated</th>
          </tr>
        </thead>
        <tbody>
          {workload.containers.map((c) => (
            <tr key={c.container} className="border-b border-border-subtle last:border-b-0" data-testid={`service-container-${c.container}`}>
              <td className={cn(TD, "font-medium text-ink")}>{c.container}</td>
              <td className={cn(TD, "max-w-64 truncate font-mono text-code text-ink-muted")} title={`${c.imageName}:${c.imageTag}`}>
                {c.imageTag || "—"}
              </td>
              <td className={cn(TD, "text-right")}>{c.cpu ? `${formatCores(c.cpu.latest)}${pct(c.cpu.limitUtilizationLatest)}` : "—"}</td>
              <td className={cn(TD, "text-right text-ink-muted")}>{c.cpu ? formatCores(c.cpu.p95) : "—"}</td>
              <td className={cn(TD, "text-right")}>{c.memory ? `${formatBytes(c.memory.latest)}${pct(c.memory.limitUtilizationLatest)}` : "—"}</td>
              <td className={cn(TD, "text-right", c.restartsInWindow > 0 && "text-warning-ink")}>{c.restartsInWindow}</td>
              <td className={cn(TD, "text-ink-muted")}>{c.lastTerminatedReason || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ── Custom metrics ──────────────────────────────────────────────────────────

function CustomMetrics({ scope, range, name, picked, env }: { scope: ObsScope | null; range: RangePreset; name: string; picked?: string; env: string }) {
  const { href } = useForgeConsole();
  const catalog = useMetricCatalog(scope, range);
  const metrics = useMemo(() => customMetrics(catalog.data?.metrics ?? [], name), [catalog.data, name]);
  const chosen = metrics.find((m) => m.name === picked) ?? metrics[0] ?? null;
  const panel = useMemo(() => (chosen ? metricPanel(chosen, name) : null), [chosen, name]);
  const result = usePanelQuery(scope, range, panel ?? metricPanel({ name: "", kind: "gauge" } as MetricCatalogEntry), [], [], { enabled: !!panel });
  const exploreHref = useExploreHref();
  const series = result.data?.series ?? [];
  const timestamps = unionTimestamps(series);
  return (
    <Section title="Custom metrics" meta={metrics.length ? `${metrics.length} this service reports` : undefined} testId="service-custom-metrics">
      {catalog.isLoading ? (
        <div className="h-24 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" />
      ) : metrics.length === 0 ? (
        <p className="text-ui text-ink-muted">
          {name} reports no metrics of its own. Declare one in the app&apos;s telemetry schema, or record one with OpenTelemetry, and it appears here.
        </p>
      ) : (
        <div className="grid gap-4 lg:grid-cols-[16rem_minmax(0,1fr)]">
          <ul className="space-y-0.5" aria-label="Metrics">
            {metrics.map((m) => (
              <li key={m.name}>
                <Link
                  href={href({ kind: "service", name }, { env, metric: m.name })}
                  className={cn("no-color block truncate rounded-md px-2 py-1.5 font-mono text-code hover:bg-ink/5", m.name === chosen?.name ? "bg-ink/5 text-ink" : "text-ink-muted")}
                  title={m.description || m.name}
                  aria-current={m.name === chosen?.name ? "true" : undefined}
                >
                  {m.name}
                </Link>
              </li>
            ))}
          </ul>
          {chosen && (
            <MetricCard
              title={chosen.name}
              figure={<span className="text-ui font-normal text-ink-muted">{chosen.description || `${chosen.kind}${chosen.unit ? ` · ${chosen.unit}` : ""}`}</span>}
              loading={result.isLoading}
              empty={!result.isLoading && timestamps.length < 2 ? "No points in the window." : null}
              testId="service-custom-metric-chart"
            >
              <UPlotChart
                height={160}
                timestamps={timestamps}
                series={series.map((s) => ({ label: Object.values(s.group).join(" ") || s.label, values: alignTo(timestamps, s) }))}
                label={chosen.name}
                legend={series.length > 1}
              />
              {exploreHref && (
                <div className="px-2 pb-1 pt-2 text-right">
                  <Link href={exploreHref({ panel: metricPanel(chosen, name) }, { env, range }) ?? "#"} className="no-color text-meta text-primary-ink hover:underline">
                    Explore {chosen.name} →
                  </Link>
                </div>
              )}
            </MetricCard>
          )}
        </div>
      )}
    </Section>
  );
}

// ── The database ────────────────────────────────────────────────────────────

/**
 * /services/<database> → Data (U6e): the database's home is Data, with its
 * status, backups, metrics and tables. The environment is kept: the scope's
 * when the database runs there, else the one it runs in (prod first).
 */
function DatabaseRedirect({ entries }: { entries: { envName: string }[] }) {
  const { env, href } = useForgeConsole();
  const navigate = useNavigate();
  const target = entries.find((entry) => entry.envName === env)?.envName ?? entries.find((entry) => entry.envName === "prod")?.envName ?? entries[0]?.envName;
  const to = href({ kind: "data" }, target ? { env: target } : {});
  useEffect(() => {
    void navigate({ href: to, replace: true });
  }, [navigate, to]);
  return <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Opening Data" data-testid="console-service-database-redirect" />;
}
