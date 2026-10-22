// Copyright (c) 2025 Reliant Labs

/**
 * OBSERVABILITY — the home of the area (PROPOSAL §4.6, decision D9: the
 * views are nav entries, so this page has no tab strip of its own).
 *
 *   strip        requests · error rate · p95 · alerts firing, each with the
 *                change against the previous window and a sparkline carrying
 *                the release markers of the scope.
 *   services     sorted by what needs attention: status, alerts firing, error
 *                rate. Each row opens the workload.
 *   right rail   recent releases into the scope, and the ways into the
 *                explorers and dashboards.
 *
 * Everything reads the console scope (one environment, or all of them) and
 * the window in the URL. Nothing here writes.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useAlerts, useObservabilityOrg, useServiceOverview, useWorkloads } from "@/hooks/observability-queries";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { UsageMeter } from "../../Workloads/workloadParts";
import { worstUsage } from "../../Workloads/workloadModel";
import { ENV_KEY } from "../attributes";
import { DashboardHeader } from "../Dashboards/DashboardHeader";
import { INK_LINK, VariableSelect } from "../Dashboards/parts";
import { describePromotion, useReleaseMarkers } from "../releaseMarkers";
import { formatAgo, formatCores, formatDuration, formatPercent, formatRate } from "../format";
import type { ObsEnvironment } from "../obsPlace";
import type { ObsSearch } from "../obsSearch";
import { ObsError, ObsNotSetUp } from "../ObsStates";
import { HealthStrip } from "./HealthStrip";
import { byAttention, serviceRows, type ServiceRow } from "./homeModel";

export interface ObservabilityHomeHrefs {
  logs: (search?: ObsSearch) => string;
  traces: (search?: ObsSearch) => string;
  profiles: (search?: ObsSearch) => string;
  alerts: string;
  dashboards: string;
  /** The workload's page (the Overview's workload detail), in an environment when one is named. */
  workload: (name: string, env: string | null) => string;
  release: (version: string) => string;
  releases: string;
}

export interface ObservabilityHomeProps {
  project: string;
  /** The console scope: an environment name or "all". */
  env: string;
  environments: readonly ObsEnvironment[];
  range: RangePreset;
  onRange: (range: RangePreset) => void;
  hrefs: ObservabilityHomeHrefs;
  navigateTo: (href: string) => void;
}

const RANGE_WORDS: Record<RangePreset, string> = {
  "15m": "last 15 minutes",
  "1h": "last hour",
  "6h": "last 6 hours",
  "24h": "last 24 hours",
  "7d": "last 7 days",
};

export function ObservabilityHome({ project, env, environments, range, onRange, hrefs, navigateTo }: ObservabilityHomeProps) {
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const envId = env === ENV_ALL ? null : (environments.find((candidate) => candidate.name === env)?.id ?? null);
  const unrecorded = env !== ENV_ALL && !envId;
  const scope = useMemo<ObsScope | null>(
    () => (orgId && !unrecorded ? { orgId, projectId: project, envIds: envId ? [envId] : [], filters: [] } : null),
    [orgId, project, envId, unrecorded]
  );
  const groupBy = useMemo(() => (env === ENV_ALL ? [ENV_KEY] : []), [env]);

  const overview = useServiceOverview(scope, range, [], groupBy);
  const workloads = useWorkloads(scope, range);
  const alerts = useAlerts(orgId, envId ?? undefined);
  const releases = useReleaseMarkers(scope, range, environments, !!scope);

  const rows = useMemo(
    () => serviceRows(overview.data?.services ?? [], workloads.data?.workloads ?? [], alerts.data ?? [], (id) => environments.find((e) => e.id === id)?.name).sort(byAttention),
    [overview.data, workloads.data, alerts.data, environments]
  );

  const meta = (
    <>
      {env === ENV_ALL ? <span>All environments</span> : <Tag>{env}</Tag>}
      <span>{RANGE_WORDS[range]}</span>
      <span>Dashed lines are releases</span>
    </>
  );
  const header = (
    <DashboardHeader
      title="Observability"
      meta={meta}
      actions={
        <VariableSelect
          id="observability-range"
          label="time"
          value={range}
          options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
          onChange={(value) => onRange(value as RangePreset)}
        />
      }
      testId="observability-home-header"
    />
  );

  if (unrecorded) {
    return (
      <div className="space-y-6" data-testid="observability-home">
        {header}
        <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="observability-unrecorded-env">
          Reliant has no record of <span className="font-medium text-ink">{env}</span>, so no telemetry is filed under it. Once it is deployed through Reliant, its logs,
          traces and metrics appear here.
        </p>
      </div>
    );
  }
  if (!scope && !org.isLoading) {
    return (
      <div className="space-y-6" data-testid="observability-home">
        {header}
        {org.error ? <ObsError error={org.error} subject={project} onRetry={() => void org.refetch()} /> : <ObsNotSetUp subject={project} />}
      </div>
    );
  }
  if (overview.error && obsErrorKind(overview.error) === "not-set-up") {
    return (
      <div className="space-y-6" data-testid="observability-home">
        {header}
        <ObsNotSetUp subject={project} />
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="observability-home">
      {header}
      <HealthStrip scope={scope} range={range} environments={environments} envId={envId} alertsHref={hrefs.alerts} />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        <Card padding="none" className="min-w-0 overflow-hidden" data-testid="observability-services">
          <div className="px-4 pt-3">
            <h2 className="text-sm font-medium text-ink">Services</h2>
            <p className="text-xs text-ink-muted">Sorted by what needs attention first.</p>
          </div>
          <ServicesTable
            rows={rows}
            loading={overview.isLoading}
            error={overview.error}
            onRetry={() => void overview.refetch()}
            showEnv={env === ENV_ALL}
            range={range}
            project={project}
            workloadHref={hrefs.workload}
            navigateTo={navigateTo}
          />
        </Card>
        <div className="space-y-4">
          <RailPanel title="Recent releases" testId="observability-recent-releases">
            {releases.status === "loading" ? (
              <div className="h-20 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
            ) : releases.promotions.length === 0 ? (
              <p className="text-xs text-ink-muted">
                Nothing was promoted {env === ENV_ALL ? "" : `to ${env} `}in the {RANGE_WORDS[range]}.{" "}
                <Link href={hrefs.releases} className={INK_LINK}>
                  All releases
                </Link>
              </p>
            ) : (
              <ol className="space-y-3">
                {releases.promotions.slice(0, 4).map((p, index) => (
                  <li key={p.promotionId} className="relative pl-5 text-xs">
                    <span
                      aria-hidden="true"
                      className={cn("absolute left-0.5 top-1 h-2 w-2 rounded-full border", index === 0 ? "border-success bg-success" : "border-ink-muted bg-surface")}
                    />
                    <Link href={hrefs.release(p.version)} className={`${INK_LINK} text-sm`} title={describePromotion(p)}>
                      {p.version || p.releaseId.slice(0, 12)}
                    </Link>
                    <span className="block text-ink-muted">
                      {p.kind === "rollback" ? "rolled back " : ""}
                      {p.envName} · {formatAgo(p.promotedAtMs)}
                      {p.promotedBy ? ` · ${p.promotedBy}` : ""}
                    </span>
                  </li>
                ))}
              </ol>
            )}
          </RailPanel>
          <RailPanel title="Explore" testId="observability-explore">
            <nav className="flex flex-col" aria-label="Explore">
              {(
                [
                  ["Search logs", hrefs.logs({ range })],
                  ["Find slow traces", hrefs.traces({ range, minDuration: "1s" })],
                  ["Compare profiles", hrefs.profiles({ range })],
                  ["Your dashboards", hrefs.dashboards],
                ] as const
              ).map(([label, href]) => (
                <Link key={label} href={href} className="no-color flex h-8 items-center justify-between rounded-md px-2 -mx-2 text-sm text-ink hover:bg-ink/5">
                  {label}
                  <ArrowRight className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
                </Link>
              ))}
            </nav>
          </RailPanel>
        </div>
      </div>
    </div>
  );
}

function RailPanel({ title, children, testId }: { title: string; children: ReactNode; testId: string }) {
  return (
    <Card padding="none" data-testid={testId}>
      <h2 className="px-4 pt-3 text-sm font-medium text-ink">{title}</h2>
      <div className="p-4 pt-3">{children}</div>
    </Card>
  );
}

// ── Services ────────────────────────────────────────────────────────────────

function ServicesTable({
  rows,
  loading,
  error,
  onRetry,
  showEnv,
  range,
  project,
  workloadHref,
  navigateTo,
}: {
  rows: ServiceRow[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  showEnv: boolean;
  range: RangePreset;
  project: string;
  workloadHref: (name: string, env: string | null) => string;
  navigateTo: (href: string) => void;
}) {
  if (loading) {
    return (
      <div className="mt-2 space-y-px" aria-busy="true" aria-label="Loading services">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-10 animate-pulse border-t border-border/70 bg-ink/5 motion-reduce:animate-none" />
        ))}
      </div>
    );
  }
  if (error) return <div className="p-4"><ObsError error={error} subject={project} onRetry={onRetry} /></div>;
  if (rows.length === 0) {
    return (
      <p className="px-4 pb-6 pt-4 text-sm text-ink-muted" data-testid="observability-no-services">
        No service sent telemetry in the {RANGE_WORDS[range]}. Services built with forge send it on their own once they run; an idle one looks exactly like
        this.
      </p>
    );
  }
  return (
    <table className="mt-2 w-full text-sm">
      <caption className="sr-only">Services, sorted by what needs attention</caption>
      <thead>
        <tr className="border-b border-border text-left text-xs text-ink-muted">
          <th scope="col" className="h-9 px-4 font-medium">
            Name
          </th>
          {showEnv && (
            <th scope="col" className="h-9 px-4 font-medium">
              Environment
            </th>
          )}
          <th scope="col" className="h-9 px-4 font-medium">
            Status
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            Req/s
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            Errors
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            p95
          </th>
          <th scope="col" className="hidden h-9 px-4 font-medium lg:table-cell">
            CPU
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            Alerts
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const href = workloadHref(row.name, row.env);
          const cpu = row.workload ? worstUsage(row.workload, "cpu") : null;
          return (
            <tr
              key={`${row.name}/${row.env ?? ""}`}
              className="cursor-pointer border-b border-border/70 last:border-b-0 hover:bg-ink/5"
              onClick={(event) => {
                if ((event.target as HTMLElement).closest("a, button")) return;
                navigateTo(href);
              }}
              data-testid={`observability-service-${row.name}${row.env ? `-${row.env}` : ""}`}
              data-tone={row.tone}
            >
              <td className="h-10 px-4">
                <Link href={href} className="no-color font-medium text-ink">
                  {row.name}
                </Link>
              </td>
              {showEnv && (
                <td className="h-10 px-4">
                  <Tag>{row.env ?? "—"}</Tag>
                </td>
              )}
              <td className="h-10 px-4">
                <span className="inline-flex items-center gap-2" title={row.reason ?? undefined}>
                  <Status tone={row.tone} label={row.status} />
                  {row.reason && <span className="hidden truncate text-xs text-ink-muted 2xl:inline">{row.reason}</span>}
                </span>
              </td>
              <td className="h-10 px-4 text-right tabular-nums text-ink">{formatRate(row.health.requestRate)}</td>
              <td className={cn("h-10 px-4 text-right tabular-nums", row.health.errorRate >= 0.05 ? "text-warning-ink" : "text-ink")}>
                {row.health.requestCount ? formatPercent(row.health.errorRate) : "—"}
              </td>
              <td className="h-10 px-4 text-right tabular-nums text-ink">{row.health.requestCount ? formatDuration(row.health.p95Ms) : "—"}</td>
              <td className="hidden h-10 w-40 px-4 lg:table-cell">
                {cpu ? <UsageMeter usage={cpu} format={formatCores} what="CPU" /> : <span className="text-xs text-ink-muted">—</span>}
              </td>
              <td className="h-10 px-4 text-right tabular-nums">
                {row.alertsFiring > 0 ? <span className="text-warning-ink">{row.alertsFiring} firing</span> : <span className="text-ink-muted">·</span>}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

