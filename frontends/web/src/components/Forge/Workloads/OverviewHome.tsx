// Copyright (c) 2025 Reliant Labs

/**
 * OVERVIEW — the project home: workloads, health and telemetry in one place
 * (there is no separate Observability home; its strip is this page's).
 *
 *   top, left    a deploy that is in flight or failed, when there is one; one
 *                status cell per environment (its worst workload, the release
 *                live there, who put it there), the health KPIs, and what
 *                needs attention — each item with the release it followed and
 *                the way back.
 *   top, right   recent releases.
 *   below        the Services table (long-running workloads: health, ready,
 *                release, restarts and last exit, CPU and memory against their
 *                limits) and the Jobs table (migrations, provisioning,
 *                scheduled jobs: result, the release that ran it, duration).
 *
 * Every row opens the workload's page. The top-bar scope narrows the tables,
 * the KPIs and "Needs attention"; the environment strip always shows every
 * environment's STATUS. It is not a second selector — the top bar is the one
 * — so each cell is a link to the Overview scoped to that environment.
 *
 * ONE READ FOR THE WORKLOADS. ListWorkloads is asked once for the whole
 * project, and the strip, the tables and "Needs attention" are cut from that
 * one answer, so they agree with each other and with the scope's switch
 * costing no request. Health is the server's verdict (workloadModel.ts).
 *
 * This component knows nothing about routing: the hrefs, the scope and the
 * URL's search state come in as props, so the preview and the tests mount it
 * on fixtures.
 */

import { useMemo, type CSSProperties, type ReactNode } from "react";
import { ArrowRight, Boxes, RotateCcw, Search } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import PageHeader from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useAlerts, useObservabilityOrg, useWorkloads } from "@/hooks/observability-queries";
import { useProjectReleases } from "@/hooks/release-queries";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import type { ObsScope, Workload } from "@/services/controlPlane/observability/types";
import type { DomainsState } from "@/hooks/forge-domain-queries";
import { frontendsInScope } from "@/services/forge/frontends";
import type { ForgeHostedWorkload } from "@/services/forge/topology";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../Console/envScope";
import { consolePath, withSearch } from "../Console/paths";
import { VariableSelect } from "../Observability/Dashboards/parts";
import { formatAgo, formatBytes, formatCores } from "../Observability/format";
import { promotionIsSuspect, type RollbackTarget } from "../Promote/rollback";
import { HealthStrip } from "../Observability/Home/HealthStrip";
import { ObsAsOf, ObsError, ObsNotSetUp } from "../Observability/ObsStates";
import { Ago, INK_BUTTON, INK_LINK, Panel } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import {
  HEALTH_STATUS,
  JOB_STATUS,
  KIND_LABEL,
  attentionItems,
  filterServices,
  formatAge,
  formatSeconds,
  jobRows,
  kindsOf,
  lastExitOf,
  needsAttention,
  previewsCell,
  replicasShort,
  replicasText,
  serviceRows,
  stripCell,
  worstUsage,
  type AttentionItem,
  type JobRow,
  type KindFilter,
  type ServiceRow,
  type StatusFilter,
  type StripCell,
  type StripEnv,
} from "./workloadModel";
import { exitPhrase, jobFailurePhrase } from "./plainHealth";
import { Chip, ChipMenu, HealthStatus, SkeletonRows, UsageMeter, WorkloadRelease } from "./workloadParts";
import { FrontendList, FrontendsEmpty, FrontendsLoading } from "../Frontends/FrontendList";
import { LiveLinks } from "./LiveLinks";
import { ProcessHealth } from "./ProcessHealth";
import { StackedTable } from "@/components/Forge/StackedTable";

export interface OverviewSearch {
  q?: string;
  status?: StatusFilter;
  kind?: KindFilter;
  range?: RangePreset;
}

export interface OverviewHrefs {
  workload: (name: string, env: string) => string;
  /** A release's page; `tab`/`env` open one of its tabs for one environment (its Health, after a bad promotion). */
  release: (version: string, open?: { tab?: string; env?: string }) => string;
  releases: string;
  alerts: string;
  /** The Overview scoped to one environment: what an environment cell links to. */
  scoped: (env: string) => string;
}

/** Rolling an environment back from "Needs attention": the targets, and the confirm flow that carries one out. */
export interface OverviewRollback {
  /** By environment name: what it runs and what it ran before. */
  targets: ReadonlyMap<string, RollbackTarget>;
  onRollback: (target: RollbackTarget) => void;
  /** Set when rolling back is impossible from here, with the reason. */
  blockedReason: string | null;
}

export interface OverviewHomeProps {
  project: string;
  /** The top-bar scope: an environment name or "all". */
  env: string;
  /** The recorded, deployed environments: long-lived first (prod leading), previews apart. */
  envs: StripEnv[];
  envsLoading: boolean;
  search: OverviewSearch;
  onSearch: (next: Partial<OverviewSearch>) => void;
  hrefs: OverviewHrefs;
  navigateTo: (href: string) => void;
  rollback?: OverviewRollback;
  /** Above the environment strip: a deploy in flight or failed (DeployAttemptsBanner), when there is one. */
  deploys?: ReactNode;
  /**
   * What the platform observes of each HOSTED environment's workloads
   * (DeployService.GetStatus), by environment name then workload name: the
   * drift call ("wrong version") the telemetry cannot make. Empty for an
   * environment Reliant does not place.
   */
  hosted?: HostedByEnv;
  /** GetStatus has not answered for every hosted environment yet. */
  hostedLoading?: boolean;
  /** The org's domains, to show which custom domains serve each frontend. Absent: not read (no domain column shown). */
  domains?: DomainsState;
  /** The Domains screen: where "Add a domain" lands. */
  domainsHref?: string;
}

export type HostedByEnv = ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>;
const NO_HOSTED: HostedByEnv = new Map();

/** The Overview's window when the URL names none: a day, so "Restarts" reads as restarts today. */
export const OVERVIEW_RANGE: RangePreset = "24h";

export function OverviewHome({
  project,
  env,
  envs,
  envsLoading,
  search,
  onSearch,
  hrefs,
  navigateTo,
  rollback,
  deploys,
  hosted = NO_HOSTED,
  hostedLoading = false,
  domains,
  domainsHref = "",
}: OverviewHomeProps) {
  const range = search.range ?? OVERVIEW_RANGE;
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const all = env === ENV_ALL;
  const scopedEnv = all ? null : (envs.find((candidate) => candidate.name === env) ?? null);
  const unrecorded = !all && !envsLoading && !scopedEnv;
  const environments = useMemo(() => envs.map((candidate) => ({ id: candidate.id, name: candidate.name, declared: null })), [envs]);

  // The whole project, once: every environment's cell and every table row come from this.
  const projectScope = useMemo<ObsScope | null>(() => (orgId ? { orgId, projectId: project, envIds: [], filters: [] } : null), [orgId, project]);
  const kpiScope = useMemo<ObsScope | null>(
    () => (orgId && !unrecorded ? { orgId, projectId: project, envIds: scopedEnv ? [scopedEnv.id] : [], filters: [] } : null),
    [orgId, project, scopedEnv, unrecorded]
  );
  const listed = useWorkloads(projectScope, range);
  const alerts = useAlerts(orgId);

  const workloads = useMemo(() => listed.data?.workloads ?? [], [listed.data]);
  const allJobs = useMemo(() => jobRows(listed.data?.jobs ?? [], ENV_ALL), [listed.data]);
  const services = useMemo(() => serviceRows(workloads, env, envs), [workloads, env, envs]);
  const jobs = useMemo(() => (all ? allJobs : allJobs.filter((row) => row.envName === env)), [allJobs, all, env]);
  const visible = useMemo(() => filterServices(services, search), [services, search]);

  const projectEnvIds = useMemo(() => new Set(envs.map((candidate) => candidate.id)), [envs]);
  const firing = useMemo(
    () => (alerts.data ?? []).filter((a) => a.state === "firing" && projectEnvIds.has(a.envId) && (all || a.envId === scopedEnv?.id)),
    [alerts.data, projectEnvIds, all, scopedEnv]
  );
  const attention = useMemo(
    () =>
      attentionItems(
        services,
        jobs,
        firing,
        (id) => envs.find((candidate) => candidate.id === id)?.name,
        Date.now()
      ),
    [services, jobs, firing, envs]
  );

  const loading = envsLoading || org.isLoading || listed.isLoading;
  const notSetUp = !!listed.error && obsErrorKind(listed.error) === "not-set-up";
  const where = all ? "all environments" : env;
  const unhealthy = services.filter(needsAttention).length;

  // NOTHING DEPLOYED: the header states no counts. They would be drawn from
  // telemetry about environments the page is about to say do not exist.
  const empty = !envsLoading && envs.length === 0;
  const header = (
    <PageHeader
      title="Overview"
      meta={
        empty ? (
          <span data-testid="overview-counts">Not deployed</span>
        ) : loading ? (
          <span className="inline-flex h-4 w-56 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading" />
        ) : (
          <>
            <span data-testid="overview-counts">
              {services.length} {services.length === 1 ? "service" : "services"} and {jobs.length} {jobs.length === 1 ? "job" : "jobs"} in {where}
            </span>
            {unhealthy > 0 && <Status tone="warning" label={`${unhealthy} need${unhealthy === 1 ? "s" : ""} attention`} />}
          </>
        )
      }
      className="mb-0"
    />
  );
  const rangeControl = (
    <VariableSelect
      id="overview-range"
      label="time"
      value={range}
      options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
      onChange={(value) => onSearch({ range: value === OVERVIEW_RANGE ? undefined : (value as RangePreset) })}
    />
  );

  if (empty) {
    return (
      <div className="space-y-6" data-testid="overview" data-state="empty">
        {header}
        {deploys}
        <GettingStarted
          testId="overview-getting-started"
          icon={<Boxes className="h-4 w-4" />}
          title="Nothing deployed yet"
          about="The overview shows every service, worker and job of each environment: whether it is healthy, its CPU and memory against its limits, restarts, and recent events."
          steps={[
            {
              text: "Deploy an environment through Reliant from your checkout. Its workloads and their health appear here as soon as they start.",
              command: "forge env deploy <env>",
            },
          ]}
          docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
        >
          <p>
            {project} has no deployed environment for Reliant to watch yet. A local environment (forge env up) runs on your machine and
            reports nothing here.
          </p>
        </GettingStarted>
      </div>
    );
  }

  const filtered = !!(search.q || search.status || search.kind);
  const windowWords = RANGE_LABELS[range].toLowerCase();

  return (
    <div className="space-y-8" data-testid="overview" data-state={loading ? "loading" : listed.error ? "error" : "ready"}>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 flex-1">{header}{listed.data && <ObsAsOf updatedAt={listed.dataUpdatedAt} />}</div>
        <div className="pt-1">{rangeControl}</div>
      </div>

      <LiveLinks envs={envs} hosted={hosted} />

      {!unrecorded && (
        <FrontendsPanel
          scope={env}
          envs={envs}
          hosted={hosted}
          loading={envsLoading || hostedLoading}
          domains={domains}
          domainsHref={domainsHref}
        />
      )}

      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0 space-y-4">
          {deploys}
          <EnvStrip envs={envs} env={env} workloads={workloads} jobs={allJobs} loading={loading} failed={!!listed.error && !keepsLastResult(listed.error, listed.data)} scopedHref={hrefs.scoped} />
          {unrecorded ? (
            <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="overview-unrecorded-env">
              Reliant has no record of <span className="font-medium text-ink">{env}</span>, so nothing it runs is reported here. Once it is deployed through Reliant, its
              services and their health appear on this page.
            </p>
          ) : (
            <>
              <HealthStrip scope={kpiScope} range={range} environments={environments} envId={scopedEnv?.id ?? null} alertsHref={hrefs.alerts} />
              <NeedsAttention
                items={attention}
                loading={loading}
                failed={!!listed.error && !keepsLastResult(listed.error, listed.data)}
                where={where}
                showEnv={all}
                hrefOf={(item) => (item.workload ? hrefs.workload(item.workload, item.envName) : hrefs.alerts)}
                navigateTo={navigateTo}
                envs={envs}
                releaseHref={hrefs.release}
                rollback={rollback}
              />
            </>
          )}
        </div>
        <aside className="space-y-4" aria-label="Recent changes">
          <RecentReleases project={project} envs={envs} hrefs={hrefs} />
        </aside>
      </div>

      {notSetUp ? (
        <ObsNotSetUp subject={project} topic="overview" env={all ? undefined : env} />
      ) : listed.error && !keepsLastResult(listed.error, listed.data) ? (
        <ObsError error={listed.error} subject={project} onRetry={() => void listed.refetch()} retrying={listed.isFetching} />
      ) : (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2" data-testid="overview-toolbar">
            <label className="flex h-8 min-w-64 items-center gap-1.5 rounded-md border border-border bg-surface px-2.5 text-ink-muted focus-within:ring-2 focus-within:ring-accent">
              <Search className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              <span className="sr-only">Filter services</span>
              <input
                value={search.q ?? ""}
                onChange={(event) => onSearch({ q: event.target.value || undefined })}
                placeholder="Filter services"
                className="min-w-0 flex-1 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none"
                data-testid="overview-filter"
              />
            </label>
            <ChipMenu
              label="Kind"
              value={search.kind}
              options={kindsOf(services).map((kind) => ({ value: kind, label: KIND_LABEL[kind] }))}
              onChange={(kind) => onSearch({ kind: kind as KindFilter | undefined })}
              testId="overview-chip-kind"
            />
            <Chip
              on={search.status === "attention"}
              onClick={() => onSearch({ status: search.status ? undefined : "attention" })}
              testId="overview-chip-status"
              title={search.status ? "Show every service" : "Show only the services that need attention"}
            >
              Status: {search.status ? "needs attention" : "all"}
            </Chip>
            {filtered && (
              <button
                type="button"
                onClick={() => onSearch({ q: undefined, status: undefined, kind: undefined })}
                className="inline-flex h-7 items-center rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink"
              >
                Clear
              </button>
            )}
          </div>

          <Panel title="Services" meta="Long-running workloads. CPU and memory against their limits." flush testId="overview-services">
            <ServicesTable
              rows={visible}
              loading={loading}
              showEnv={all}
              windowWords={windowWords}
              windowShort={range}
              hosted={hosted}
              hrefs={hrefs}
              empty={
                filtered ? (
                  <>
                    No service matches these filters.{" "}
                    <button type="button" className={INK_BUTTON} onClick={() => onSearch({ q: undefined, status: undefined, kind: undefined })}>
                      Clear them
                    </button>
                  </>
                ) : unrecorded ? (
                  <>Nothing is reported for {env}.</>
                ) : (
                  <>
                    No service has reported from {where} in the {windowWords}. Workloads appear once Reliant sees their pods; an environment
                    that runs only jobs looks like this.
                  </>
                )
              }
            />
            {listed.data?.truncated && (
              <p className="border-t border-border px-4 py-2.5 text-xs text-ink-muted" data-testid="overview-services-truncated">
                Showing the first 500 workloads.
              </p>
            )}
          </Panel>

          {!unrecorded && <ProcessHealth
            scope={kpiScope}
            range={range}
            workloads={workloads}
            alerts={(alerts.data ?? []).filter(alert => projectEnvIds.has(alert.envId))}
            envNameOf={(id) => envs.find(candidate => candidate.id === id)?.name}
            workloadHref={(name, processEnv) => hrefs.workload(name, processEnv ?? env)}
            logsHref={(name) => withSearch(consolePath(project, { kind: "obs-view", view: "logs" }), { env, range, service: name, severity: "error,fatal" })}
            navigateTo={navigateTo}
          />}

          <Panel title="Jobs" meta="Migrations, provisioning and scheduled jobs. The latest run of each." flush testId="overview-jobs">
            <JobsTable
              rows={jobs}
              loading={loading}
              showEnv={all}
              hrefs={hrefs}
              empty={<>No job has run in {where} in the {windowWords}. Migrations and provisioning jobs appear here after the next deploy that runs them.</>}
            />
            {listed.data?.jobsTruncated && (
              <p className="border-t border-border px-4 py-2.5 text-xs text-ink-muted" data-testid="overview-jobs-truncated">
                Built from the newest 200 runs in the {windowWords}; older runs are not counted.
              </p>
            )}
          </Panel>
        </div>
      )}
    </div>
  );
}

// ── Frontends ───────────────────────────────────────────────────────────────

/**
 * The scope's frontends beside its services: the sites people open, each with
 * the address it is live at. Read from the GetStatus observations the page
 * already holds (`hosted`), so it costs no request of its own; omitted while a
 * scope that only self-managed environments covers has nothing Reliant hosts.
 */
function FrontendsPanel({
  scope,
  envs,
  hosted,
  loading,
  domains,
  domainsHref,
}: {
  scope: string;
  envs: StripEnv[];
  hosted: HostedByEnv;
  loading: boolean;
  domains: DomainsState | undefined;
  domainsHref: string;
}) {
  const covered = envs.filter((candidate) => candidate.placed && (scope === ENV_ALL ? !candidate.preview : candidate.name === scope));
  if (covered.length === 0) return null;
  const frontends = frontendsInScope(scope, envs, hosted, domains?.availability === "available" ? domains.domains : null);
  return (
    <Panel title="Frontends" meta="Websites Reliant hosts, and the address each is live at." flush testId="overview-frontends">
      {loading && frontends.length === 0 ? (
        <FrontendsLoading />
      ) : frontends.length === 0 ? (
        <div className="px-4 pb-4">
          <FrontendsEmpty env={scope === ENV_ALL ? undefined : scope} />
        </div>
      ) : (
        <FrontendList frontends={frontends} showEnv={scope === ENV_ALL} domainsHref={domainsHref} />
      )}
    </Panel>
  );
}

// ── Environment strip ───────────────────────────────────────────────────────

function EnvStrip({
  envs,
  env,
  workloads,
  jobs,
  loading,
  failed,
  scopedHref,
}: {
  envs: StripEnv[];
  env: string;
  workloads: Workload[];
  jobs: JobRow[];
  loading: boolean;
  failed: boolean;
  scopedHref: (env: string) => string;
}) {
  const long = envs.filter((candidate) => !candidate.preview);
  const previews = envs.filter((candidate) => candidate.preview);
  const cells = long.map((candidate) => stripCell(candidate, workloads, jobs));
  const previewCell = previewsCell(previews, workloads, jobs);
  const count = cells.length + (previewCell ? 1 : 0);
  if (count === 0) return null;
  const previewSelected = previews.some((candidate) => candidate.name === env);
  return (
    <div
      // One row of cells on a desktop; two to a row on a phone, where five
      // environments in one row left each ~70px and clipped its status.
      className="grid grid-cols-2 overflow-hidden rounded-lg border border-border bg-surface md:grid-cols-[repeat(var(--env-cells),minmax(0,1fr))] max-md:[&>*]:border-b max-md:[&>*]:border-border"
      style={{ "--env-cells": count } as CSSProperties}
      data-testid="overview-env-strip"
    >
      {cells.map((cell, index) => (
        <EnvCell key={cell.env.name} cell={cell} first={index === 0} selected={cell.env.name === env} loading={loading} failed={failed} href={scopedHref(cell.env.name)} />
      ))}
      {previewCell && (
        <div className={cn("relative min-w-0 px-4 py-3", cells.length > 0 && "border-l border-border")} data-testid="overview-env-previews" data-selected={previewSelected ? "true" : undefined}>
          <div className="flex items-center justify-between gap-2 text-xs">
            <span className="text-ink-muted">previews</span>
            {!loading && !failed && <Status tone={previewCell.tone} label={previewCell.label} size="sm" />}
          </div>
          <div className="mt-1 text-sm font-medium text-ink">{previews.length} running</div>
          <div className="text-xs text-ink-muted">{loading || failed ? " " : `${previewCell.services} ${previewCell.services === 1 ? "service" : "services"}`}</div>
          <div className="flex flex-wrap gap-x-1.5 text-xs text-ink-muted">
            {previews.map((preview, index) => (
              <span key={preview.name}>
                {index > 0 && <span aria-hidden="true">· </span>}
                <Link
                  href={scopedHref(preview.name)}
                  aria-current={preview.name === env ? "page" : undefined}
                  className={cn("no-color rounded text-ink-muted hover:text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent", preview.name === env && "font-medium text-ink")}
                >
                  {preview.name}
                </Link>
              </span>
            ))}
          </div>
          {previewSelected && <span aria-hidden="true" className="absolute inset-x-0 bottom-0 h-0.5 bg-ink" />}
        </div>
      )}
    </div>
  );
}

function EnvCell({
  cell,
  first,
  selected,
  loading,
  failed,
  href,
}: {
  cell: StripCell;
  first: boolean;
  selected: boolean;
  loading: boolean;
  failed: boolean;
  href: string;
}) {
  const { env } = cell;
  return (
    <Link
      href={href}
      aria-current={selected ? "page" : undefined}
      title={selected ? `Showing ${env.name}` : `Show ${env.name}`}
      data-testid={`overview-env-${env.name}`}
      data-tone={loading || failed ? undefined : cell.tone}
      className={cn(
        "no-color relative block min-w-0 px-4 py-3 text-left transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent",
        !first && "border-l border-border"
      )}
    >
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className={selected ? "font-medium text-ink" : "text-ink-muted"}>{env.name}</span>
        {loading ? (
          <span className="h-3 w-14 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-hidden="true" />
        ) : failed ? (
          <span className="text-ink-muted">health unavailable</span>
        ) : (
          <Status tone={cell.tone} label={cell.label} size="sm" />
        )}
      </div>
      <div className="mt-1 truncate text-sm font-medium text-ink" title={env.release || undefined}>
        {env.release ? shortVersion(env.release) : <span className="font-normal text-ink-muted">No release</span>}
      </div>
      <div className="truncate text-xs text-ink-muted">
        {loading || failed ? " " : `${cell.services} ${cell.services === 1 ? "service" : "services"}`}
        {cell.failedJobs > 0 && !loading && <span className="text-danger-ink"> · {cell.failedJobs} job{cell.failedJobs === 1 ? "" : "s"} failed</span>}
      </div>
      <div className="truncate text-xs text-ink-muted">
        {env.release && (
          <>
            {env.promotedBy ? `promoted by ${env.promotedBy}` : "promoted"}
            {env.promotedAt && " · "}
            <Ago at={env.promotedAt} />
          </>
        )}
      </div>
      {selected && <span aria-hidden="true" className="absolute inset-x-0 bottom-0 h-0.5 bg-ink" />}
    </Link>
  );
}

// ── Needs attention ─────────────────────────────────────────────────────────

function NeedsAttention({
  items,
  loading,
  failed,
  where,
  showEnv,
  hrefOf,
  navigateTo,
  envs,
  releaseHref,
  rollback,
}: {
  items: AttentionItem[];
  loading: boolean;
  failed: boolean;
  where: string;
  showEnv: boolean;
  hrefOf: (item: AttentionItem) => string;
  navigateTo: (href: string) => void;
  envs: StripEnv[];
  releaseHref: OverviewHrefs["release"];
  rollback?: OverviewRollback;
}) {
  return (
    <Panel title="Needs attention" flush testId="overview-attention">
      {loading ? (
        <table className="w-full">
          <tbody>
            <SkeletonRows columns={3} rows={2} />
          </tbody>
        </table>
      ) : failed ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted">Workload health could not be read, so this cannot say what needs attention. Alerts still show on the Alerts page.</p>
      ) : items.length === 0 ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted" data-testid="overview-attention-none">
          Nothing needs attention in {where}.
        </p>
      ) : (
        <table className="w-full text-sm" aria-label="Needs attention">
          <tbody>
            {items.map((item) => {
              const href = hrefOf(item);
              const suspect = suspectOf(item, envs);
              const target = rollback?.targets.get(item.envName);
              return (
                <tr
                  key={item.key}
                  className="relative cursor-pointer border-t border-border/70 first:border-t-0 hover:bg-ink/5"
                  onClick={(event) => {
                    if ((event.target as HTMLElement).closest("a, button")) return;
                    navigateTo(href);
                  }}
                  data-testid={`attention-${item.key}`}
                  data-tone={item.tone}
                >
                  <td className="h-10 w-6 pl-4 pr-0">
                    <Status tone={item.tone} />
                  </td>
                  <td className="h-10 max-w-0 px-3">
                    <Link href={href} className="no-color block truncate text-ink">
                      <span className="font-medium">{item.subject}</span> {item.what}
                    </Link>
                  </td>
                  {showEnv && (
                    <td className="h-10 w-px whitespace-nowrap px-2">
                      <Tag>{item.envName}</Tag>
                    </td>
                  )}
                  <td className="h-10 w-px max-w-64 truncate whitespace-nowrap px-4 text-right text-xs text-ink-muted" title={item.detail}>
                    {item.detail}
                    {suspect && (
                      <span className="block" data-testid={`attention-suspect-${item.key}`}>
                        started {formatAge(suspect.afterMs)} after{" "}
                        <Link href={releaseHref(suspect.release, { tab: "health", env: item.envName })} className={INK_LINK} title={`${suspect.release}: its health before and after`}>
                          {shortVersion(suspect.release)}
                        </Link>
                      </span>
                    )}
                  </td>
                  {rollback && (
                    <td className="h-10 w-px whitespace-nowrap pr-4 text-right">
                      {target && (
                        <button
                          type="button"
                          onClick={() => rollback.onRollback(target)}
                          disabled={!!rollback.blockedReason}
                          title={rollback.blockedReason ?? `Plan a rollback of ${target.env} to ${target.previous}`}
                          className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2 text-xs font-medium text-ink hover:bg-ink/5 disabled:cursor-not-allowed disabled:opacity-60"
                          data-testid={`attention-rollback-${item.key}`}
                        >
                          <RotateCcw className="h-3 w-3" aria-hidden="true" />
                          Roll back {target.env} to {shortVersion(target.previous)}
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

/**
 * The promotion an attention item followed, when it followed one closely
 * enough to be the suspect: the environment's current release, promoted
 * within SUSPECT_WINDOW_MS before the trouble was seen (its latest warning or
 * failed run; now when nothing timestamps it), and the release the workload is
 * on (or one it cannot name). Alerts name no release, so they get none.
 */
function suspectOf(item: AttentionItem, envs: readonly StripEnv[]): { release: string; afterMs: number } | null {
  if (item.kind === "alert") return null;
  const env = envs.find((candidate) => candidate.name === item.envName);
  if (!env?.release || !env.promotedAt) return null;
  if (item.release && item.release !== env.release) return null;
  const troubleAt = item.troubleAtMs ?? Date.now();
  if (!promotionIsSuspect(env.promotedAt, troubleAt)) return null;
  return { release: env.release, afterMs: troubleAt - Date.parse(env.promotedAt) };
}

// ── The rail ────────────────────────────────────────────────────────────────

function RailLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link href={href} className="no-color inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink">
      {children}
      <ArrowRight className="h-3 w-3" aria-hidden="true" />
    </Link>
  );
}

function RecentReleases({ project, envs, hrefs }: { project: string; envs: StripEnv[]; hrefs: OverviewHrefs }) {
  const releases = useProjectReleases(project);
  const recent = (releases.data ?? []).slice(0, 3);
  return (
    <Panel title="Recent releases" meta="Latest builds and where they run now" actions={<RailLink href={hrefs.releases}>All</RailLink>} testId="overview-recent-releases">
      {releases.isLoading ? (
        <div className="h-24 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading releases" />
      ) : releases.error ? (
        <p className="text-xs text-ink-muted">Releases could not be read right now.</p>
      ) : recent.length === 0 ? (
        <p className="text-xs text-ink-muted">No release has been cut yet. The first forge env deploy cuts one.</p>
      ) : (
        <ol className="space-y-3.5">
          {recent.map((release, index) => {
            const live = envs.filter((candidate) => candidate.release === release.version).map((candidate) => candidate.name);
            return (
              <li key={release.version} className="relative pl-6 text-xs" data-testid={`overview-release-${release.version}`}>
                <span
                  aria-hidden="true"
                  className={cn("absolute left-1 top-1.5 h-2 w-2 rounded-full border", index === 0 && live.length ? "border-success bg-success" : "border-ink-muted bg-surface")}
                />
                {index < recent.length - 1 && <span aria-hidden="true" className="absolute -bottom-3.5 left-2 top-4 w-px bg-border" />}
                <Link href={hrefs.release(release.version)} className={`${INK_LINK} text-sm`} title={release.version}>
                  {shortVersion(release.version)}
                </Link>
                {release.subject && (
                  <span className="block truncate text-ink-muted" title={release.subject}>
                    {release.subject}
                  </span>
                )}
                <span className="block text-ink-muted">
                  {live.length ? `live in ${live.join(", ")}` : "not live in these environments"}
                  {release.author ? ` · ${release.author}` : ""}
                  {release.createdAt ? " · " : ""}
                  <Ago at={release.createdAt} />
                </span>
              </li>
            );
          })}
        </ol>
      )}
    </Panel>
  );
}

// ── Tables ──────────────────────────────────────────────────────────────────

const TH = "h-9 whitespace-nowrap px-4 text-left text-xs font-medium text-ink-muted";
const TD = "h-10 whitespace-nowrap px-4 text-sm text-ink";
const ROW = "relative border-t border-border/70 first:border-t-0 hover:bg-ink/5";
/** The name cell's link covers the whole row: the row is the link. */
const ROW_LINK =
  "no-color font-medium text-ink after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-accent";

function ServicesTable({
  rows,
  loading,
  showEnv,
  windowWords,
  windowShort,
  hosted,
  hrefs,
  empty,
}: {
  rows: ServiceRow[];
  loading: boolean;
  showEnv: boolean;
  windowWords: string;
  windowShort: string;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  empty: ReactNode;
}) {
  const columns = showEnv ? 8 : 7;
  return (
    <div className="overflow-x-auto">
      <StackedTable className="min-w-full border-collapse" aria-label="Services" data-testid="overview-services-table">
        <thead>
          <tr className="border-b border-border">
            <th scope="col" className={TH}>
              Name
            </th>
            {showEnv && (
              <th scope="col" className={TH}>
                Environment
              </th>
            )}
            <th scope="col" className={TH}>
              Status
            </th>
            <th scope="col" className={cn(TH, "text-right")}>
              Ready
            </th>
            <th scope="col" className={TH}>
              Version
            </th>
            <th scope="col" className={cn(TH, "text-right")} title={`Container restarts in the ${windowWords}, and the last exit reason`}>
              Restarts {windowShort}
            </th>
            <th scope="col" className={TH}>
              CPU
            </th>
            <th scope="col" className={TH}>
              Memory
            </th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <SkeletonRows columns={columns} />
          ) : rows.length === 0 ? (
            <tr>
              <td colSpan={columns} className="px-4 py-10 text-center text-sm text-ink-muted" data-testid="overview-services-empty">
                {empty}
              </td>
            </tr>
          ) : (
            rows.map((w) => {
              if (w.unobserved) return <UnobservedRow key={`${w.envName}/${w.workload}`} row={w} showEnv={showEnv} />;
              const exit = lastExitOf(w);
              const status = HEALTH_STATUS[w.health];
              return (
                <tr key={`${w.envName}/${w.workload}`} className={ROW} data-testid={`overview-service-${w.workload}${showEnv ? `-${w.envName}` : ""}`} data-health={w.health}>
                  <td className={TD}>
                    <Link href={hrefs.workload(w.workload, w.envName)} className={ROW_LINK}>
                      {w.workload}
                    </Link>
                    {KIND_LABEL[w.kind] && <span className="ml-2 text-xs text-ink-muted">{KIND_LABEL[w.kind]}</span>}
                  </td>
                  {showEnv && (
                    <td className={TD}>
                      <Tag>{w.envName}</Tag>
                    </td>
                  )}
                  <td className={TD}>
                    <HealthStatus workload={w} />
                  </td>
                  <td className={cn(TD, "text-right tabular-nums", replicasShort(w) && "text-warning-ink")}>{replicasText(w)}</td>
                  <td className={TD}>
                    <WorkloadRelease workload={w} releaseHref={hrefs.release} hosted={hosted.get(w.envName)?.get(w.workload) ?? null} />
                  </td>
                  <td className={cn(TD, "text-right tabular-nums")}>
                    {w.restartsInWindow ? (
                      <>
                        {w.restartsInWindow}
                        {exit && (
                          <span
                            className={cn("ml-1", status.tone === "danger" ? "text-danger-ink" : status.tone === "warning" ? "text-warning-ink" : "text-ink-muted")}
                            title={exit.atMs ? `Last exited ${new Date(exit.atMs).toLocaleString()}` : undefined}
                          >
                            · {exitPhrase(exit.reason)}
                          </span>
                        )}
                      </>
                    ) : (
                      <span className="text-ink-muted">0</span>
                    )}
                  </td>
                  <td className={TD}>
                    <UsageMeter usage={worstUsage(w, "cpu")} format={formatCores} what="CPU" />
                  </td>
                  <td className={TD}>
                    <UsageMeter usage={worstUsage(w, "memory")} format={formatBytes} what="Memory" />
                  </td>
                </tr>
              );
            })
          )}
        </tbody>
      </StackedTable>
    </div>
  );
}

/**
 * A workload a self-managed environment's render declares and nothing has
 * observed: named, with no verdict and no figures, and no page to open (there
 * is nothing observed to show on one).
 */
function UnobservedRow({ row, showEnv }: { row: ServiceRow; showEnv: boolean }) {
  return (
    <tr className="border-t border-border/70 first:border-t-0" data-testid={`overview-service-${row.workload}${showEnv ? `-${row.envName}` : ""}`} data-health="unobserved">
      <td className={TD}>
        <span className="font-medium text-ink">{row.workload}</span>
      </td>
      {showEnv && (
        <td className={TD}>
          <Tag>{row.envName}</Tag>
        </td>
      )}
      <td className={TD} title={row.healthReason}>
        <Status tone="neutral" label="Unobserved" />
      </td>
      <td colSpan={5} className={cn(TD, "text-xs text-ink-muted")}>
        Declared by {row.envName}&apos;s last recorded render; nothing reports from it.
      </td>
    </tr>
  );
}

function JobsTable({ rows, loading, showEnv, hrefs, empty }: { rows: JobRow[]; loading: boolean; showEnv: boolean; hrefs: OverviewHrefs; empty: ReactNode }) {
  const columns = showEnv ? 6 : 5;
  return (
    <div className="overflow-x-auto">
      <StackedTable className="min-w-full border-collapse" aria-label="Jobs" data-testid="overview-jobs-table">
        <thead>
          <tr className="border-b border-border">
            <th scope="col" className={TH}>
              Name
            </th>
            {showEnv && (
              <th scope="col" className={TH}>
                Environment
              </th>
            )}
            <th scope="col" className={TH}>
              Result
            </th>
            <th scope="col" className={TH}>
              Ran in release
            </th>
            <th scope="col" className={cn(TH, "text-right")}>
              Duration
            </th>
            <th scope="col" className={TH}>
              Finished
            </th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <SkeletonRows columns={columns} rows={2} />
          ) : rows.length === 0 ? (
            <tr>
              <td colSpan={columns} className="px-4 py-10 text-center text-sm text-ink-muted" data-testid="overview-jobs-empty">
                {empty}
              </td>
            </tr>
          ) : (
            rows.map((row) => {
              const job = row.latest;
              const status = JOB_STATUS[job.result];
              return (
                <tr key={row.key} className={ROW} data-testid={`overview-job-${row.workload}${showEnv ? `-${row.envName}` : ""}`} data-result={job.result}>
                  <td className={TD}>
                    <Link href={hrefs.workload(row.workload, row.envName)} className={ROW_LINK} title={job.name}>
                      {row.workload}
                    </Link>
                    {row.runs > 1 && (
                      <span className="ml-2 text-xs text-ink-muted">
                        {row.runs} runs{row.failedRuns ? `, ${row.failedRuns} failed` : ""}
                      </span>
                    )}
                  </td>
                  {showEnv && (
                    <td className={TD}>
                      <Tag>{row.envName}</Tag>
                    </td>
                  )}
                  <td className={TD}>
                    <span className="inline-flex items-center gap-1.5">
                      <Status tone={status.tone} label={status.label} />
                      {job.result === "failed" && job.reason && <span className="text-xs text-danger-ink">· {jobFailurePhrase(job.reason)}</span>}
                    </span>
                  </td>
                  <td className={TD}>
                    {job.releaseVersion ? (
                      <Link href={hrefs.release(job.releaseVersion)} className={cn(INK_LINK, "relative z-10")} title={job.releaseVersion}>
                        {shortVersion(job.releaseVersion)}
                      </Link>
                    ) : (
                      <span className="text-ink-muted">unknown</span>
                    )}
                  </td>
                  <td className={cn(TD, "text-right tabular-nums")}>
                    {job.durationSeconds ? formatSeconds(job.durationSeconds) : <span className="text-ink-muted">{job.result === "running" ? "running" : "—"}</span>}
                  </td>
                  <td className={cn(TD, "text-ink-muted")}>
                    {job.finishedAtMs ? (
                      <time dateTime={new Date(job.finishedAtMs).toISOString()} title={new Date(job.finishedAtMs).toLocaleString()}>
                        {formatAgo(job.finishedAtMs)}
                      </time>
                    ) : job.result === "running" ? (
                      "not yet"
                    ) : (
                      "not observed"
                    )}
                  </td>
                </tr>
              );
            })
          )}
        </tbody>
      </StackedTable>
    </div>
  );
}
