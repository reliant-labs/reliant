// Copyright (c) 2026 Reliant Labs

/**
 * OBSERVABILITY OVERVIEW (U6e, owner): /forge/p/:app/observability, where
 * drilling into Observability lands — "how is it doing, and where do I look
 * first", before any one explorer:
 *
 *   golden signals   requests, error rate and p95 for the scope (HealthStrip,
 *                    the same RED panel the project Overview reads), then the
 *                    same three per service (useAppLiveState: the read the
 *                    Services page and the Overview's strip share)
 *   top errors       the open issues with the most events in the window (ListIssues)
 *   slowest          the RPC methods with the highest p95 (GetServiceGraphCatalog,
 *   endpoints        the RPC explorer's read)
 *   firing alerts    the project's alerts firing now (ListAlerts)
 *   service map      a thumbnail of the service graph (GetServiceGraph) linking to it
 *
 * Each block reads, loads and fails on its own; each links to the explorer
 * that holds the detail. The window is the URL's `range`.
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, BellRing, Bug, Network, Timer } from "lucide-react";

import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import { useAlerts, useIssues, useObservabilityOrg, obsKeys } from "@/hooks/observability-queries";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { cn } from "@/lib/utils";
import { obsRetry, obsRetryDelay } from "@/services/controlPlane/observability/errors";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, RANGE_PRESETS, resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../../Console/consoleContext";
import { NothingDeployed } from "../../Console/consolePageFrame";
import { ENV_ALL } from "../../Console/envScope";
import { ToOrgObservability, useSeveralApps } from "../../Console/severalProjects";
import { useAppLiveState } from "../../LiveState/useAppLiveState";
import { OVERVIEW_RANGE } from "../../Workloads/workloadModel";
import { VariableSelect } from "../Dashboards/parts";
import { UPlotChart } from "../charts/UPlotChart";
import { formatAgo, formatCount, formatDuration, formatPercent, formatRate } from "../format";
import { shortService } from "../Graph/graphStats";
import { HealthStrip } from "../Home/HealthStrip";
import { ObsError } from "../ObsStates";
import { graphThumbnail, slowestEndpoints, THUMB_H, THUMB_W } from "./obsOverviewModel";

const TOP = 5;

/** One block of the page: a heading with its icon, a link to the explorer that holds the rest, then the body. */
function Block({ title, icon, href, linkLabel, testId, children }: { title: string; icon: ReactNode; href: string; linkLabel: string; testId: string; children: ReactNode }) {
  return (
    <section className="min-w-0 rounded-xl border border-border bg-surface" data-testid={testId}>
      <header className="flex min-h-11 items-center justify-between gap-3 border-b border-border-subtle px-4 py-2">
        <h2 className="flex min-w-0 items-center gap-2 text-ui font-medium text-ink">
          <span className="text-ink-muted" aria-hidden="true">
            {icon}
          </span>
          {title}
        </h2>
        <Link href={href} className="no-color inline-flex shrink-0 items-center gap-1 text-meta text-ink-muted hover:text-ink">
          {linkLabel}
          <ArrowRight className="size-3" aria-hidden="true" />
        </Link>
      </header>
      <div className="px-4 py-3">{children}</div>
    </section>
  );
}

function Quiet({ children, testId }: { children: ReactNode; testId?: string }) {
  return (
    <p className="py-2 text-ui text-ink-muted" data-testid={testId}>
      {children}
    </p>
  );
}

function Rows({ count = 3 }: { count?: number }) {
  return (
    <div className="space-y-2 py-1" aria-label="Loading" aria-busy="true">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="h-6 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
      ))}
    </div>
  );
}

export function ObsOverviewPage() {
  // Several projects in the shell: Observability's Overview is the org console's, over all of them.
  const several = useSeveralApps();
  if (several) return <ToOrgObservability />;
  return <ProjectObsOverviewPage />;
}

function ProjectObsOverviewPage() {
  const { forgeProject, env, roster, href } = useForgeConsole();
  const search = useSearch({ strict: false }) as { range?: RangePreset };
  const navigate = useNavigate();
  const range = search.range ?? OVERVIEW_RANGE;
  const org = useObservabilityOrg();
  const orgId = org.orgId ?? null;

  const recorded = useMemo(() => roster.envs.flatMap((candidate) => (candidate.live ? [{ id: candidate.live.id, name: candidate.name }] : [])), [roster.envs]);
  const scopedEnv = env === ENV_ALL ? null : (recorded.find((candidate) => candidate.name === env) ?? null);
  const scope = useMemo<ObsScope | null>(
    () => (orgId ? { orgId, projectId: forgeProject, envIds: scopedEnv ? [scopedEnv.id] : [], filters: [] } : null),
    [orgId, forgeProject, scopedEnv]
  );
  const environments = useMemo(() => recorded.map((candidate) => ({ ...candidate, declared: null })), [recorded]);
  const alertsHref = href({ kind: "obs-view", view: "alerts" });

  if (!roster.isLoading && !roster.resolvingName && recorded.length === 0) {
    return (
      <div className="space-y-4" data-testid="obs-overview">
        <PageHeader density="compact" title="Observability" className="mb-0" />
        <NothingDeployed what="Observability" about="Observability shows requests, errors and latency for every service, the top errors, the slowest endpoints, what is alerting and how the services call each other." />
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="obs-overview">
      <PageHeader
        density="compact"
        className="mb-0"
        title="Observability"
        subtitle="How every service is doing, and where to look first"
        controls={
          <VariableSelect
            id="obs-overview-range"
            label="time"
            value={range}
            options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
            onChange={(value) =>
              void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, range: value === OVERVIEW_RANGE ? undefined : (value as RangePreset) }), replace: true } as never)
            }
          />
        }
      />
      <HealthStrip scope={scope} range={range} environments={environments} envId={scopedEnv?.id ?? null} alertsHref={alertsHref} />
      <ServiceSignals app={forgeProject} env={env} range={range} />
      <div className="grid grid-cols-[minmax(0,1fr)] gap-6 xl:grid-cols-2">
        <TopErrors scope={scope} range={range} />
        <SlowestEndpoints scope={scope} range={range} />
        <FiringAlerts orgId={orgId} envIds={scopedEnv ? [scopedEnv.id] : recorded.map((candidate) => candidate.id)} envName={(id) => recorded.find((candidate) => candidate.id === id)?.name ?? id} />
        <GraphThumbnail scope={scope} range={range} />
      </div>
    </div>
  );
}

// ── Golden signals per service ─────────────────────────────────────────────

const TH = "h-9 px-3 text-left text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-11 px-3 text-ui tabular-nums whitespace-nowrap";

function ServiceSignals({ app, env, range }: { app: string; env: string; range: RangePreset }) {
  const { href } = useForgeConsole();
  const live = useAppLiveState(app, env, range);
  const rows = live.rows.filter((row) => row.type === "workload");
  const serving = rows.filter((row) => row.type === "workload" && row.health);
  const quiet = rows.length - serving.length;
  const showEnv = live.scopedEnv === null;
  const loading = live.envsLoading || (live.listed.isLoading && !live.listed.data) || live.red.isLoading;
  return (
    <Block title="Golden signals per service" icon={<Timer className="size-4" />} href={href({ kind: "services" })} linkLabel="Services" testId="obs-overview-services">
      {loading ? (
        <Rows />
      ) : live.red.error && !live.red.data ? (
        <ObsError error={live.red.error} subject={app} onRetry={() => void live.red.refetch()} retrying={live.red.isFetching} />
      ) : serving.length === 0 ? (
        <Quiet>No service served traced requests in the {RANGE_LABELS[range].toLowerCase()}.</Quiet>
      ) : (
        <div className="-mx-4 overflow-x-auto">
          <table className="w-full min-w-[40rem] border-collapse">
            <thead>
              <tr className="border-b border-border-subtle">
                <th className={TH}>Service</th>
                <th className={cn(TH, "text-right")}>Requests</th>
                <th className={TH} />
                <th className={cn(TH, "text-right")}>Error rate</th>
                <th className={cn(TH, "text-right")}>p95</th>
              </tr>
            </thead>
            <tbody>
              {serving.map((row) => {
                if (row.type !== "workload" || !row.health) return null;
                const h = row.health;
                return (
                  <tr key={row.key} className="border-b border-border-subtle last:border-b-0 hover:bg-ink/5" data-testid={`obs-overview-service-${row.name}`}>
                    <td className={TD}>
                      <span className="flex items-center gap-2">
                        <Status tone={row.tone} label={row.status} hideLabel />
                        <Link href={href({ kind: "service", name: row.name }, showEnv ? { env: row.envName } : {})} className="no-color font-medium text-ink hover:underline">
                          {row.name}
                        </Link>
                        {showEnv && <span className="text-meta text-ink-muted">{row.envName}</span>}
                      </span>
                    </td>
                    <td className={cn(TD, "text-right")}>{formatRate(h.requestRate)}</td>
                    <td className={cn(TD, "w-24")}>
                      <UPlotChart compact className="w-16" height={18} timestamps={h.timestamps} series={[{ label: "Requests", values: h.rateSeries }]} unit="rate" label={`Requests per second to ${row.name}`} />
                    </td>
                    <td className={cn(TD, "text-right", h.errorRate >= 0.05 ? "text-danger-ink" : h.errorRate >= 0.01 ? "text-warning-ink" : "")}>{formatPercent(h.errorRate)}</td>
                    <td className={cn(TD, "text-right")}>{formatDuration(h.p95Ms)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {!loading && quiet > 0 && <p className="pt-2 text-meta text-ink-muted">{quiet} more ran without serving traced requests (workers, jobs).</p>}
    </Block>
  );
}

// ── Top errors ──────────────────────────────────────────────────────────────

function TopErrors({ scope, range }: { scope: ObsScope | null; range: RangePreset }) {
  const { href, forgeProject } = useForgeConsole();
  const issueScope = useMemo(() => (scope ? { orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds } : null), [scope]);
  const issues = useIssues(issueScope, { range, statuses: [], sort: "events", text: "" });
  const top = (issues.data?.pages[0]?.issues ?? []).slice(0, TOP);
  return (
    <Block title="Top errors" icon={<Bug className="size-4" />} href={href({ kind: "obs-view", view: "errors" })} linkLabel="Errors" testId="obs-overview-errors">
      {issues.isLoading ? (
        <Rows />
      ) : issues.error && !issues.data ? (
        <ObsError error={issues.error} subject={forgeProject} onRetry={() => void issues.refetch()} retrying={issues.isFetching} />
      ) : top.length === 0 ? (
        <Quiet testId="obs-overview-errors-none">No open errors in the {RANGE_LABELS[range].toLowerCase()}.</Quiet>
      ) : (
        <ol className="-mx-2 space-y-0.5">
          {top.map((issue) => (
            <li key={issue.id}>
              <Link href={href({ kind: "obs-view", view: "errors" }, { issue: issue.id })} className="no-color flex min-w-0 items-center gap-3 rounded-md px-2 py-1.5 hover:bg-ink/5">
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-ui text-ink" title={issue.title}>
                    {issue.title}
                  </span>
                  <span className="block truncate text-meta text-ink-muted">
                    {issue.service} · {issue.envName} · last {formatAgo(issue.lastSeenMs)}
                  </span>
                </span>
                <span className="shrink-0 text-right text-meta tabular-nums text-ink-muted">
                  <span className="block text-ui font-medium text-ink">{formatCount(issue.events)}</span>
                  events
                </span>
              </Link>
            </li>
          ))}
        </ol>
      )}
    </Block>
  );
}

// ── Slowest endpoints ───────────────────────────────────────────────────────

function useWindow(range: RangePreset) {
  // Fixed for the page's life at a given range: the reads that take a window are keyed by it.
  return useMemo(() => resolveWindow(range), [range]);
}

function SlowestEndpoints({ scope, range }: { scope: ObsScope | null; range: RangePreset }) {
  const { href, forgeProject } = useForgeConsole();
  const api = useObservabilityApi();
  const window = useWindow(range);
  const catalog = useQuery({
    queryKey: [...obsKeys.all(scope), "obs-overview-catalog", window],
    queryFn: ({ signal }) => api.getServiceGraphCatalog({ ...(scope as ObsScope), window, rpcService: "" }, { signal }),
    enabled: !!scope,
    staleTime: 60_000,
    retry: obsRetry,
    retryDelay: obsRetryDelay,
  });
  const slowest = useMemo(() => slowestEndpoints(catalog.data, TOP), [catalog.data]);
  return (
    <Block title="Slowest endpoints" icon={<Timer className="size-4" />} href={href({ kind: "obs-view", view: "rpc" })} linkLabel="RPC explorer" testId="obs-overview-slowest">
      {catalog.isLoading ? (
        <Rows />
      ) : catalog.error && !catalog.data ? (
        <ObsError error={catalog.error} subject={forgeProject} onRetry={() => void catalog.refetch()} retrying={catalog.isFetching} />
      ) : slowest.length === 0 ? (
        <Quiet testId="obs-overview-slowest-none">No endpoint was called enough in the {RANGE_LABELS[range].toLowerCase()} to rank its latency.</Quiet>
      ) : (
        <ol className="-mx-2 space-y-0.5">
          {slowest.map((endpoint) => (
            <li key={endpoint.id}>
              <Link
                href={href({ kind: "obs-view", view: "rpc" }, { rpcId: endpoint.id, rpcService: endpoint.service })}
                className="no-color flex min-w-0 items-center gap-3 rounded-md px-2 py-1.5 hover:bg-ink/5"
                data-testid={`obs-overview-endpoint-${endpoint.method}`}
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono text-code text-ink" title={endpoint.id}>
                    {shortService(endpoint.service)}/{endpoint.method}
                  </span>
                  <span className="block text-meta tabular-nums text-ink-muted">
                    {formatCount(endpoint.calls)} calls{endpoint.errorRate !== null && endpoint.errorRate > 0 ? ` · ${formatPercent(endpoint.errorRate)} failed` : ""}
                  </span>
                </span>
                <span className="shrink-0 text-right text-meta tabular-nums text-ink-muted">
                  <span className="block text-ui font-medium text-ink">{formatDuration(endpoint.p95Ms)}</span>
                  p95
                </span>
              </Link>
            </li>
          ))}
        </ol>
      )}
    </Block>
  );
}

// ── Firing alerts ───────────────────────────────────────────────────────────

function FiringAlerts({ orgId, envIds, envName }: { orgId: string | null; envIds: string[]; envName: (id: string) => string }) {
  const { href } = useForgeConsole();
  const alerts = useAlerts(orgId);
  const mine = new Set(envIds);
  const firing = (alerts.data ?? []).filter((alert) => alert.state === "firing" && mine.has(alert.envId));
  const pending = (alerts.data ?? []).filter((alert) => alert.state === "pending" && mine.has(alert.envId)).length;
  return (
    <Block title="Firing alerts" icon={<BellRing className="size-4" />} href={href({ kind: "obs-view", view: "alerts" })} linkLabel="Alerts" testId="obs-overview-alerts">
      {alerts.isLoading ? (
        <Rows count={2} />
      ) : alerts.error && !alerts.data ? (
        <Quiet>Alerts could not be read right now.</Quiet>
      ) : firing.length === 0 ? (
        <Quiet testId="obs-overview-alerts-none">Nothing is firing{pending ? `; ${pending} pending` : ""}.</Quiet>
      ) : (
        <ul className="-mx-2 space-y-0.5">
          {firing.slice(0, TOP).map((alert) => (
            <li key={`${alert.envId}:${alert.fingerprint}`}>
              <Link href={href({ kind: "obs-view", view: "alerts" }, { rule: alert.rule })} className="no-color flex min-w-0 items-center gap-3 rounded-md px-2 py-1.5 hover:bg-ink/5">
                <span role="img" aria-label="Firing" className="size-2 shrink-0 rounded-full bg-danger" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-ui text-ink">{alert.rule}</span>
                  <span className="block truncate text-meta text-ink-muted">
                    {envName(alert.envId)}
                    {alert.annotations.summary ? ` · ${alert.annotations.summary}` : ""}
                  </span>
                </span>
                {alert.firingAtMs && <span className="shrink-0 text-meta text-ink-muted">since {formatAgo(alert.firingAtMs)}</span>}
              </Link>
            </li>
          ))}
          {firing.length > TOP && <li className="px-2 pt-1 text-meta text-ink-muted">and {firing.length - TOP} more</li>}
        </ul>
      )}
    </Block>
  );
}

// ── The service map, small ──────────────────────────────────────────────────

function GraphThumbnail({ scope, range }: { scope: ObsScope | null; range: RangePreset }) {
  const { href, forgeProject } = useForgeConsole();
  const api = useObservabilityApi();
  const window = useWindow(range);
  const graph = useQuery({
    queryKey: [...obsKeys.all(scope), "obs-overview-graph", window],
    queryFn: ({ signal }) => api.getServiceGraph({ ...(scope as ObsScope), window, rpcIds: [], rpcService: "", services: [], includeInternal: false, includeDeclared: false }, { signal }),
    enabled: !!scope,
    staleTime: 60_000,
    retry: obsRetry,
    retryDelay: obsRetryDelay,
  });
  const thumb = useMemo(() => graphThumbnail(graph.data), [graph.data]);
  const graphHref = href({ kind: "obs-view", view: "graph" });
  return (
    <Block title="Service graph" icon={<Network className="size-4" />} href={graphHref} linkLabel="Open the graph" testId="obs-overview-graph">
      {graph.isLoading ? (
        <div className="h-40 animate-pulse rounded-lg bg-ink/5 motion-reduce:animate-none" aria-label="Loading the service graph" />
      ) : graph.error && !graph.data ? (
        <ObsError error={graph.error} subject={forgeProject} onRetry={() => void graph.refetch()} retrying={graph.isFetching} />
      ) : thumb.nodes.length === 0 ? (
        <Quiet>No calls between services were seen in the {RANGE_LABELS[range].toLowerCase()}.</Quiet>
      ) : (
        <Link href={graphHref} className="no-color block rounded-lg hover:bg-ink/5" aria-label={`Service graph: ${thumb.nodes.length} nodes, ${thumb.links.length} connections. Open the full graph.`}>
          <svg viewBox={`0 0 ${THUMB_W} ${THUMB_H}`} className="h-40 w-full" role="img" aria-hidden="true" data-testid="obs-overview-graph-thumb">
            {thumb.links.map((link) => (
              <line key={link.id} x1={link.x1} y1={link.y1} x2={link.x2} y2={link.y2} className={link.failing ? "stroke-destructive" : "stroke-border-strong"} strokeWidth={1.5} />
            ))}
            {thumb.nodes.map((node) => (
              <g key={node.id}>
                <circle cx={node.x} cy={node.y} r={5} className={cn("stroke-surface", node.kind === "database" || node.kind === "external" ? "fill-ink-muted" : "fill-primary")} strokeWidth={2} />
                <text x={node.x} y={node.y + 15} textAnchor="middle" className="fill-ink-muted" style={{ fontSize: 9 }}>
                  {node.label.length > 14 ? `${node.label.slice(0, 13)}…` : node.label}
                </text>
              </g>
            ))}
          </svg>
        </Link>
      )}
    </Block>
  );
}
