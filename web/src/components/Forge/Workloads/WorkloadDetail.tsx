// Copyright (c) 2025 Reliant Labs

/**
 * ONE WORKLOAD in one environment (PROPOSAL §4.2; breadcrumb "Overview ›
 * <name>"). The environment is in the path, so the page is pinned to it.
 *
 *   header   status, environment, kind · ready, the release it runs (verified
 *            by digest, or declared by the promotion), the image, and the
 *            actions — every mutating one naming its environment (D1):
 *            "Restart in prod" for a hosted environment (D6); Scale is never a
 *            console mutation (D6): it shows the declared line and "Open PR".
 *   tabs     Overview (requests, errors, CPU and memory against their limits,
 *            each with release markers; pods), Logs / Traces / Profiles (the
 *            existing explorers, pre-filtered to this workload), Events,
 *            History (versions and images over the window), Config (the
 *            declared spec: source, resources, ports, env var NAMES).
 *   rail     provenance, rollout history, recent events, jump-to.
 *
 * A Job or CronJob has no pods to restart or replicas to scale: its page shows
 * its runs instead.
 *
 * Nothing here knows about routing (WorkloadPages adapts it to the console),
 * so the preview and the tests mount it on fixtures.
 */

import { lazy, Suspense, useMemo, useState, type ReactNode } from "react";
import { ArrowRight, Copy, ExternalLink, GitPullRequest, RotateCcw, ScrollText, SlidersHorizontal } from "lucide-react";

import Card from "@/components/forge-ui/card";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Status from "@/components/forge-ui/status";
import Tabs from "@/components/forge-ui/tabs";
import Tag from "@/components/forge-ui/tag";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { useObservabilityOrg, usePanelQuery, useWorkload, useWorkloads } from "@/hooks/observability-queries";
import { useProjectRelease } from "@/hooks/release-queries";
import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";
import { cn } from "@/lib/utils";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import type { ObsScope, Workload, WorkloadDetail as WorkloadDetailData, WorkloadEvent, WorkloadJob, WorkloadSeries } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";
import type { RestartedWorkload } from "@/services/forge/cloudEnvs";

import { UPlotChart, type ChartMarker } from "../Observability/charts/UPlotChart";
import { unionTimestamps } from "../Observability/Dashboards/PanelBody";
import { VariableSelect } from "../Observability/Dashboards/parts";
import { formatAgo, formatBytes, formatCores, formatCount, formatPercent } from "../Observability/format";
import { ratioSeries, redPanel, seriesValues } from "../Observability/Home/homeModel";
import { ObsError, ObsNotSetUp } from "../Observability/ObsStates";
import type { ObsNavigate, ObsSearch, ObsView } from "../Observability/obsSearch";
import { useReleaseMarkers } from "../Observability/releaseMarkers";
import { Ago, BUTTON_PRIMARY, BUTTON_SECONDARY, Facts, INK_LINK, Note, Panel, RowMenu, type MenuItem } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import {
  JOB_STATUS,
  KIND_LABEL,
  declarationLinks,
  formatAge,
  formatMillicores,
  formatQuantityBytes,
  formatSeconds,
  isJobKind,
  lastExitOf,
  podAgeMs,
  replicasText,
  WORKLOAD_TABS,
  type WorkloadTab,
} from "./workloadModel";
import { HealthStatus, WorkloadRelease } from "./workloadParts";
import { OVERVIEW_RANGE } from "./OverviewHome";

// Its own chunk, as on the Observability pages: the explorers load only when a tab shows them.
const ObservabilityPanel = lazy(() =>
  import("../Observability/ObservabilityPanel").catch((error: unknown) => {
    if (!isChunkLoadError(error)) throw error;
    return staleChunkModule(error, "default") as unknown as typeof import("../Observability/ObservabilityPanel");
  })
);

const TAB_LABELS: Record<WorkloadTab, string> = {
  overview: "Overview",
  logs: "Logs",
  traces: "Traces",
  profiles: "Profiles",
  events: "Events",
  history: "History",
  config: "Config",
};
/** The tabs that host an existing explorer, and the panel view each one is. */
const EXPLORER_TABS: Partial<Record<WorkloadTab, ObsView>> = { logs: "logs", traces: "traces", profiles: "profiles" };

/** The URL's state: the tab, the window, and the explorer tabs' own keys (obsSearch.ts). */
export type WorkloadDetailSearch = Omit<ObsSearch, "view" | "envs" | "service" | "workload"> & { tab?: WorkloadTab };

/** What the page lets a reader do to the running workload: restart it, or why not. */
export interface RestartControl {
  onRestart: () => Promise<RestartedWorkload>;
  /** Set when restarting is impossible here, with the reason; the button is then disabled and says so. */
  unavailableReason: string | null;
}

export interface WorkloadDetailHrefs {
  overview: string;
  release: (version: string) => string;
  activity: string;
  dashboards: string;
}

export interface WorkloadDetailProps {
  project: string;
  /** The environment in the path. */
  env: { id: string; name: string };
  name: string;
  search: WorkloadDetailSearch;
  onSearch: (next: WorkloadDetailSearch, options?: { replace?: boolean }) => void;
  hrefs: WorkloadDetailHrefs;
  restart: RestartControl;
}

export function WorkloadDetail({ project, env, name, search, onSearch, hrefs, restart }: WorkloadDetailProps) {
  const range = (search.range as RangePreset | undefined) ?? OVERVIEW_RANGE;
  const tab: WorkloadTab = search.tab ?? "overview";
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const scope = useMemo<ObsScope | null>(() => (orgId ? { orgId, projectId: project, envIds: [env.id], filters: [] } : null), [orgId, project, env.id]);
  // The Overview's own read (the whole project): a cache hit when the reader came from there.
  const projectScope = useMemo<ObsScope | null>(() => (orgId ? { orgId, projectId: project, envIds: [], filters: [] } : null), [orgId, project]);
  const detail = useWorkload(scope, range, name);
  const listed = useWorkloads(projectScope, range);

  const summary: Workload | null =
    detail.data?.workloads.find((w) => w.envId === env.id) ??
    detail.data?.workloads[0] ??
    listed.data?.workloads.find((w) => w.envName === env.name && w.workload === name) ??
    null;
  const runs = useMemo(() => (listed.data?.jobs ?? []).filter((job) => job.envName === env.name && job.workload === name), [listed.data, env.name, name]);
  const isJob = summary ? isJobKind(summary.kind) : runs.length > 0;
  const releaseQuery = useProjectRelease(project, summary?.releaseVersion || null);
  const release = releaseQuery.data?.release ?? null;

  const crumbs = [{ label: "Overview", href: hrefs.overview }, { label: name }];
  const rangeControl = (
    <VariableSelect
      id="workload-range"
      label="time"
      value={range}
      options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
      onChange={(value) => onSearch({ range: value === OVERVIEW_RANGE ? undefined : (value as RangePreset) }, { replace: true })}
    />
  );

  const loading = org.isLoading || detail.isLoading || (listed.isLoading && !detail.data?.workloads.length);
  if (loading) {
    return (
      <div className="space-y-6" data-testid="workload-detail" data-state="loading" aria-busy="true">
        <PageHeader breadcrumbs={crumbs} title={name} meta={<span className="inline-flex h-4 w-72 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />} className="mb-0" />
        <div className="h-9 border-b border-border" />
        <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
          <div className="grid gap-4 md:grid-cols-2">
            {[0, 1, 2, 3].map((i) => (
              <Card key={i} className="h-44 animate-pulse motion-reduce:animate-none" />
            ))}
          </div>
          <Card className="h-64 animate-pulse motion-reduce:animate-none" />
        </div>
      </div>
    );
  }

  const error = detail.error ?? (!detail.data ? listed.error : null);
  if (error) {
    return (
      <div className="space-y-6" data-testid="workload-detail" data-state="error">
        <PageHeader breadcrumbs={crumbs} title={name} className="mb-0" />
        {obsErrorKind(error) === "not-set-up" ? (
          <ObsNotSetUp subject={project} />
        ) : (
          <ObsError error={error} subject={`${name} in ${env.name}`} onRetry={() => void detail.refetch()} retrying={detail.isFetching} />
        )}
      </div>
    );
  }

  if (!summary && runs.length === 0) {
    return (
      <div className="space-y-6" data-testid="workload-detail" data-state="not-found">
        <PageHeader breadcrumbs={crumbs} title={name} titleAccessory={<Tag>{env.name}</Tag>} className="mb-0" />
        <EmptyState
          title={`No workload named ${name} in ${env.name}`}
          description={`Nothing named ${name} reported from ${env.name} in the ${RANGE_LABELS[range].toLowerCase()}, and its recorded bundle does not declare it. It may run in another environment, or it was removed.`}
        />
        <div className="flex items-center justify-center gap-3">
          <Link href={hrefs.overview} className={cn(BUTTON_SECONDARY, "no-color")}>
            Back to Overview
          </Link>
          {rangeControl}
        </div>
      </div>
    );
  }

  const tabs = isJob ? WORKLOAD_TABS.filter((id) => id !== "profiles") : WORKLOAD_TABS;
  const explorer = EXPLORER_TABS[tab];

  return (
    <div className="space-y-6" data-testid="workload-detail" data-state="ready" data-kind={summary?.kind ?? "job"}>
      <WorkloadHeader
        crumbs={crumbs}
        name={name}
        env={env}
        summary={summary}
        runs={runs}
        isJob={isJob}
        branch={release?.branch || null}
        hrefs={hrefs}
        restart={restart}
        // An explorer tab carries its own window control (the same `range` key): one on screen, not two.
        rangeControl={explorer ? null : rangeControl}
        onTab={(next) => onSearch({ tab: next === "overview" ? undefined : next, range: search.range })}
      />

      <Tabs
        tabs={tabs.map((id) => ({ id, label: TAB_LABELS[id] }))}
        activeTab={tab}
        // Leaving an explorer drops its keys (a log query means nothing on Events); the window stays.
        onChange={(id) => onSearch({ tab: id === "overview" ? undefined : (id as WorkloadTab), range: search.range })}
        ariaLabel={`${name} views`}
      />

      {explorer ? (
        <div role="tabpanel" aria-label={TAB_LABELS[tab]}>
          <ExplorerTab project={project} env={env} name={name} view={explorer} search={search} onSearch={onSearch} />
        </div>
      ) : (
        <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
          <div className="min-w-0 space-y-4" role="tabpanel" aria-label={TAB_LABELS[tab]}>
            {tab === "overview" &&
              (isJob ? (
                <RunsPanel runs={runs} hrefs={hrefs} />
              ) : (
                <OverviewTab scope={scope} range={range} env={env} name={name} detail={detail.data ?? null} summary={summary} onLogs={() => onSearch({ tab: "logs", range: search.range })} />
              ))}
            {tab === "events" && <EventsPanel events={detail.data?.events ?? []} range={range} />}
            {tab === "history" && <HistoryTab detail={detail.data ?? null} envId={env.id} runs={runs} hrefs={hrefs} />}
            {tab === "config" && <ConfigTab summary={summary} env={env.name} branch={release?.branch || null} />}
          </div>
          <Rail
            name={name}
            env={env}
            summary={summary}
            latestRun={isJob ? (runs[0] ?? null) : null}
            detail={detail.data ?? null}
            commit={release ? { sha: release.gitCommit, subject: release.subject, author: release.author } : null}
            hrefs={hrefs}
            onTab={(next) => onSearch({ tab: next, range: search.range })}
          />
        </div>
      )}
    </div>
  );
}

// ── Header ──────────────────────────────────────────────────────────────────

function WorkloadHeader({
  crumbs,
  name,
  env,
  summary,
  runs,
  isJob,
  branch,
  hrefs,
  restart,
  rangeControl,
  onTab,
}: {
  crumbs: { label: string; href?: string }[];
  name: string;
  env: { id: string; name: string };
  summary: Workload | null;
  runs: WorkloadJob[];
  isJob: boolean;
  branch: string | null;
  hrefs: WorkloadDetailHrefs;
  restart: RestartControl;
  rangeControl: ReactNode;
  onTab: (tab: WorkloadTab) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [outcome, setOutcome] = useState<{ ok: true; result: RestartedWorkload } | { ok: false; message: string } | null>(null);
  const container = summary?.containers[0];
  const image = summary?.declared?.image || (container ? `${container.imageName}${container.imageTag ? `:${container.imageTag}` : ""}` : "");
  const latestRun = runs[0];

  const runRestart = async () => {
    setPending(true);
    try {
      const result = await restart.onRestart();
      setOutcome({ ok: true, result });
    } catch (err) {
      setOutcome({ ok: false, message: err instanceof Error ? err.message : String(err) });
    } finally {
      setPending(false);
      setConfirming(false);
    }
  };

  const menu: MenuItem[] = [
    { label: "Open in Activity", href: hrefs.activity, testId: "workload-menu-activity" },
    { label: "View declared spec", onSelect: () => onTab("config"), testId: "workload-menu-config" },
  ];

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <PageHeader
          breadcrumbs={crumbs}
          title={name}
          titleAccessory={
            <>
              {summary ? <HealthStatus workload={summary} /> : latestRun ? <Status tone={JOB_STATUS[latestRun.result].tone} label={JOB_STATUS[latestRun.result].label} /> : null}
              <Tag>{env.name}</Tag>
            </>
          }
          meta={
            <>
              <span>
                {(summary && KIND_LABEL[summary.kind]) || (isJob ? "Job" : "Workload")}
                {summary && !isJob && ` · ${replicasText(summary)} ready`}
                {isJob && ` · ${runs.length} ${runs.length === 1 ? "run" : "runs"} in the window`}
              </span>
              {summary?.releaseVersion ? (
                <span className="inline-flex items-center gap-1">
                  Release <WorkloadRelease workload={summary} releaseHref={hrefs.release} />
                </span>
              ) : latestRun?.releaseVersion ? (
                <span>
                  Release{" "}
                  <Link href={hrefs.release(latestRun.releaseVersion)} className={INK_LINK}>
                    {shortVersion(latestRun.releaseVersion)}
                  </Link>
                </span>
              ) : null}
              {image && <Mono value={image} length={40} />}
              {summary?.healthReason && <span>{summary.healthReason}</span>}
            </>
          }
          className="mb-0 min-w-0 flex-1"
        />
        <div className="flex shrink-0 flex-wrap items-center gap-2 pt-1" data-testid="workload-actions">
          {rangeControl}
          <button type="button" className={BUTTON_SECONDARY} onClick={() => onTab("logs")}>
            <ScrollText className="h-3.5 w-3.5" aria-hidden="true" />
            Logs
          </button>
          {!isJob && (
            <>
              <button
                type="button"
                className={BUTTON_SECONDARY}
                onClick={() => setConfirming(true)}
                disabled={!!restart.unavailableReason || pending}
                title={restart.unavailableReason ?? `A rolling restart of ${name} in ${env.name}`}
                data-testid="workload-restart"
              >
                <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                {pending ? `Restarting in ${env.name}…` : `Restart in ${env.name}`}
              </button>
              <ScaleButton name={name} env={env.name} summary={summary} branch={branch} />
            </>
          )}
          <RowMenu label={`More actions for ${name}`} items={menu} testId="workload-menu" />
        </div>
      </div>
      {restart.unavailableReason && !isJob && (
        <p className="text-xs text-ink-muted" data-testid="workload-restart-unavailable">
          {restart.unavailableReason}
        </p>
      )}
      {outcome?.ok && (
        <Note tone="neutral" testId="workload-restart-requested">
          Restart requested in <span className="font-medium text-ink">{outcome.result.environmentName || env.name}</span>
          {outcome.result.requestedAt ? (
            <>
              {" "}
              <Ago at={outcome.result.requestedAt} />
            </>
          ) : null}
          . Pods are replaced one at a time, each gated on readiness; the release and the declared spec do not change. Watch the Pods table below.
        </Note>
      )}
      {outcome && !outcome.ok && (
        <Note tone="danger" testId="workload-restart-failed">
          The restart in {env.name} was not requested: {outcome.message}
        </Note>
      )}
      <ConfirmationDialog
        open={confirming}
        onCancel={() => setConfirming(false)}
        onConfirm={() => void runRestart()}
        title={`Restart ${name} in ${env.name}?`}
        description={`A rolling restart: pods are replaced one at a time and readiness gates each step. The release and the declared spec do not change. Recorded in the audit trail as a restart of ${name} in ${env.name}.`}
        confirmLabel={`Restart in ${env.name}`}
        variant="warning"
        loading={pending}
      />
    </div>
  );
}

/**
 * Scale (D6): replicas are declared in KCL, so the console never changes them.
 * It shows where the declaration lives and opens a pull request against it.
 */
function ScaleButton({ name, env, summary, branch }: { name: string; env: string; summary: Workload | null; branch: string | null }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  const declared = summary?.declared ?? null;
  const links = declared ? declarationLinks(declared, branch) : { edit: null, view: null };
  const replicas = declared?.replicas ?? summary?.desiredReplicas ?? null;
  const path = declared?.sourcePath || `deploy/kcl/${env}/main.k`;
  return (
    <div
      className="relative"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button type="button" className={BUTTON_SECONDARY} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((prev) => !prev)} data-testid="workload-scale">
        <SlidersHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
        Scale…
      </button>
      {open && (
        <div
          role="dialog"
          aria-label={`Scale ${name}`}
          {...escapeLayer}
          className="absolute right-0 top-10 z-50 w-96 rounded-lg border border-border bg-surface p-4 text-sm shadow-lg"
          data-testid="workload-scale-popover"
        >
          <p className="font-medium text-ink">Replicas are declared in code</p>
          <p className="mt-1 text-xs text-ink-muted">
            Changing them here would drift from your repository. Edit the declaration instead; the next deploy converges {env}.
          </p>
          {declared || summary?.desiredReplicas !== null ? (
            <pre className="mt-3 overflow-x-auto rounded-md border border-border/60 bg-background px-3 py-2 font-mono text-xs text-ink">
              <span className="text-ink-muted">{path}</span>
              {"\n"}
              {`  ${name}: replicas = ${replicas ?? "not declared"}`}
            </pre>
          ) : (
            <p className="mt-3 text-xs text-ink-muted">This environment&apos;s recorded bundle does not declare {name}, so there is no declaration to point at.</p>
          )}
          {declared && !declared.sourceRepo && <p className="mt-2 text-xs text-ink-muted">The bundle recorded no repository, so the pull request is yours to open against {path}.</p>}
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              type="button"
              className={BUTTON_SECONDARY}
              onClick={() => {
                void navigator.clipboard?.writeText(path).then(() => setCopied(true));
              }}
            >
              <Copy className="h-3.5 w-3.5" aria-hidden="true" />
              {copied ? "Copied" : "Copy path"}
            </button>
            {links.edit ? (
              <a href={links.edit} target="_blank" rel="noreferrer" className={cn(BUTTON_PRIMARY, "no-color")} data-testid="workload-scale-open-pr">
                <GitPullRequest className="h-3.5 w-3.5" aria-hidden="true" />
                Open PR
              </a>
            ) : (
              <button
                type="button"
                className={BUTTON_PRIMARY}
                disabled
                title={declared?.sourceRepo ? "The release recorded no branch to open a pull request against" : "The bundle recorded no GitHub repository"}
                data-testid="workload-scale-open-pr"
              >
                <GitPullRequest className="h-3.5 w-3.5" aria-hidden="true" />
                Open PR
              </button>
            )}
            {links.view && (
              <a href={links.view} target="_blank" rel="noreferrer" className={cn(INK_LINK, "inline-flex items-center gap-1 text-xs")}>
                View at {declared!.sourceCommit.slice(0, 8)}
                <ExternalLink className="h-3 w-3" aria-hidden="true" />
              </a>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// ── Overview tab ────────────────────────────────────────────────────────────

/** The series of `metric` for this environment: the container closest to its limit when there are several. */
function seriesOf(detail: WorkloadDetailData | null, envId: string, metric: WorkloadSeries["metric"]): WorkloadSeries | null {
  const candidates = (detail?.series ?? []).filter((s) => s.envId === envId && s.metric === metric);
  if (candidates.length <= 1) return candidates[0] ?? null;
  const peak = (s: WorkloadSeries) => Math.max(0, ...s.values.map((v) => v ?? 0));
  return [...candidates].sort((a, b) => peak(b) - peak(a))[0]!;
}

function OverviewTab({
  scope,
  range,
  env,
  name,
  detail,
  summary,
  onLogs,
}: {
  scope: ObsScope | null;
  range: RangePreset;
  env: { id: string; name: string };
  name: string;
  detail: WorkloadDetailData | null;
  summary: Workload | null;
  onLogs: () => void;
}) {
  const environments = useMemo(() => [{ id: env.id, name: env.name, declared: null }], [env.id, env.name]);
  const markers = useReleaseMarkers(scope, range, environments, !!scope).markers;
  const linePanel = useMemo(() => redPanel("line"), []);
  const numberPanel = useMemo(() => redPanel("number"), []);
  const services = useMemo(() => [name], [name]);
  const trend = usePanelQuery(scope, range, linePanel, services);
  const totals = usePanelQuery(scope, range, numberPanel, services);

  const timestamps = trend.data ? unionTimestamps(trend.data.series) : [];
  const requests = trend.data ? seriesValues(trend.data.series, "requests") : [];
  const errors = trend.data ? seriesValues(trend.data.series, "errors") : [];
  const totalRequests = totals.data?.series.find((s) => s.label === "requests")?.value ?? null;
  const totalErrors = totals.data?.series.find((s) => s.label === "errors")?.value ?? null;
  const errorRate = totalRequests ? (totalErrors ?? 0) / totalRequests : null;

  const cpu = seriesOf(detail, env.id, "cpu_usage");
  const memory = seriesOf(detail, env.id, "memory_working_set");
  const cpuNow = summary ? worst(summary, "cpu") : null;
  const memNow = summary ? worst(summary, "memory") : null;

  return (
    <>
      <div className="grid gap-4 md:grid-cols-2">
        <ChartPanel
          title="Requests"
          figure={totals.isLoading ? null : totalRequests === null ? "—" : formatCount(totalRequests)}
          empty={!trend.isLoading && (!totalRequests || timestamps.length < 2) ? `${name} served no traced requests in the window.` : null}
          loading={trend.isLoading}
          testId="workload-chart-requests"
        >
          <UPlotChart height={140} timestamps={timestamps} series={[{ label: "requests", values: requests, tone: "primary" }]} unit="count" label={`Requests to ${name}`} markers={markers} legend={false} />
        </ChartPanel>
        <ChartPanel
          title="Error rate"
          figure={totals.isLoading ? null : errorRate === null ? "—" : formatPercent(errorRate)}
          tone={errorRate !== null && errorRate >= 0.05 ? "warning" : undefined}
          empty={!trend.isLoading && (!totalRequests || timestamps.length < 2) ? "No requests, so no error rate." : null}
          loading={trend.isLoading}
          testId="workload-chart-errors"
        >
          <UPlotChart height={140} timestamps={timestamps} series={[{ label: "error rate", values: ratioSeries(errors, requests), tone: "destructive" }]} unit="percent" label={`Error rate of ${name}`} markers={markers} legend={false} />
        </ChartPanel>
        <UsageChart title="CPU vs limit" series={cpu} now={cpuNow} format={formatCores} unit="cores" markers={markers} name={name} />
        <UsageChart title="Memory vs limit" series={memory} now={memNow} format={formatBytes} unit="bytes" markers={markers} name={name} />
      </div>
      <PodsPanel summary={summary} onLogs={onLogs} />
    </>
  );
}

function worst(summary: Workload, resource: "cpu" | "memory") {
  let best: { latest: number; limit: number | null } | null = null;
  for (const c of summary.containers) {
    const usage = c[resource];
    if (!usage) continue;
    if (!best || usage.latest > best.latest) best = { latest: usage.latest, limit: usage.limit };
  }
  return best;
}

function ChartPanel({
  title,
  figure,
  tone,
  empty,
  loading,
  testId,
  children,
}: {
  title: string;
  figure: ReactNode | null;
  tone?: "warning";
  empty: string | null;
  loading: boolean;
  testId: string;
  children: ReactNode;
}) {
  return (
    <Card padding="none" className="min-w-0" data-testid={testId}>
      <div className="flex items-baseline justify-between gap-3 px-4 pt-3">
        <h2 className="text-sm font-medium text-ink">{title}</h2>
        {figure === null ? (
          <span className="h-4 w-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label={`Loading ${title}`} />
        ) : (
          <span className={cn("text-base font-semibold tabular-nums", tone === "warning" ? "text-warning-ink" : "text-ink")}>{figure}</span>
        )}
      </div>
      <div className="px-4 pb-3 pt-3">
        {loading ? (
          <div className="h-[140px] animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        ) : empty ? (
          <div className="flex h-[140px] items-center justify-center rounded-md border border-dashed border-border px-6 text-center text-xs text-ink-muted">{empty}</div>
        ) : (
          children
        )}
      </div>
    </Card>
  );
}

function UsageChart({
  title,
  series,
  now,
  format,
  unit,
  markers,
  name,
}: {
  title: string;
  series: WorkloadSeries | null;
  now: { latest: number; limit: number | null } | null;
  format: (value: number) => string;
  unit: "cores" | "bytes";
  markers: readonly ChartMarker[];
  name: string;
}) {
  const limit = now?.limit ?? null;
  const figure = now ? (
    <>
      {format(now.latest)}
      <span className="ml-1 text-xs font-normal text-ink-muted">{limit !== null ? `/ ${format(limit)}` : "no limit"}</span>
    </>
  ) : (
    "—"
  );
  const lines = series
    ? [
        { label: title.split(" ")[0]!.toLowerCase(), values: series.values, tone: "primary" as const },
        ...(limit !== null ? [{ label: "limit", values: series.values.map(() => limit), tone: "muted" as const, dashed: true }] : []),
      ]
    : [];
  return (
    <ChartPanel
      title={title}
      figure={figure}
      tone={now && limit !== null && now.latest / limit >= 0.85 ? "warning" : undefined}
      empty={!series || series.timestamps.length < 2 ? `Reliant received no ${unit === "cores" ? "CPU" : "memory"} samples for ${name} in the window.` : null}
      loading={false}
      testId={`workload-chart-${unit === "cores" ? "cpu" : "memory"}`}
    >
      {series && <UPlotChart height={140} timestamps={series.timestamps} series={lines} unit={unit} label={`${title} for ${name}`} markers={markers} legend={false} />}
    </ChartPanel>
  );
}

const TH = "h-9 whitespace-nowrap px-4 text-left text-xs font-medium text-ink-muted";
const TD = "h-10 whitespace-nowrap px-4 text-sm text-ink";

function PodsPanel({ summary, onLogs }: { summary: Workload | null; onLogs: () => void }) {
  const pods = summary?.pods ?? [];
  const now = Date.now();
  return (
    <Panel
      title="Pods"
      meta={pods.length ? `${pods.filter((p) => p.state === "running").length} running` : undefined}
      actions={
        <button type="button" onClick={onLogs} className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink">
          Open in Logs
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </button>
      }
      flush
      testId="workload-pods"
    >
      {pods.length === 0 ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted">No pod reported in the window. A workload scaled to zero, or one Reliant cannot see yet, looks like this.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="min-w-full border-collapse" aria-label="Pods">
            <thead>
              <tr className="border-b border-border">
                <th scope="col" className={TH}>
                  Name
                </th>
                <th scope="col" className={TH}>
                  Status
                </th>
                <th scope="col" className={cn(TH, "text-right")}>
                  Restarts
                </th>
                <th scope="col" className={TH}>
                  Last exit
                </th>
                <th scope="col" className={TH}>
                  Node
                </th>
                <th scope="col" className={cn(TH, "text-right")}>
                  Age
                </th>
              </tr>
            </thead>
            <tbody>
              {pods.map((pod) => {
                const age = podAgeMs(pod, now);
                const warning = pod.latestWarning;
                const status =
                  pod.state === "gone"
                    ? { tone: "neutral" as const, label: "Gone" }
                    : warning
                      ? { tone: "warning" as const, label: warning.reason }
                      : pod.state === "running"
                        ? { tone: "success" as const, label: "Running" }
                        : { tone: "neutral" as const, label: "Unknown" };
                return (
                  <tr key={pod.uid || pod.name} className="border-t border-border/70 first:border-t-0" data-testid={`workload-pod-${pod.name}`} data-state={pod.state}>
                    <td className={TD}>
                      <Mono value={pod.name} length={48} />
                    </td>
                    <td className={TD} title={warning ? warning.message : undefined}>
                      <Status tone={status.tone} label={status.label} />
                    </td>
                    <td className={cn(TD, "text-right tabular-nums", pod.restarts === 0 && "text-ink-muted")}>{pod.restarts}</td>
                    <td className={cn(TD, pod.lastExitReason ? "text-warning-ink" : "text-ink-muted")} title={pod.lastExitAtMs ? new Date(pod.lastExitAtMs).toLocaleString() : undefined}>
                      {pod.lastExitReason || "·"}
                    </td>
                    <td className={cn(TD, !pod.node && "text-ink-muted")} title={pod.node ? undefined : "The cluster agent does not report the node yet"}>
                      {pod.node || "not reported"}
                    </td>
                    <td className={cn(TD, "text-right tabular-nums text-ink-muted")} title={pod.ageIsLowerBound ? "Older than the window" : undefined}>
                      {age === null ? "—" : `${pod.ageIsLowerBound ? "≥" : ""}${formatAge(age)}`}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

function RunsPanel({ runs, hrefs }: { runs: WorkloadJob[]; hrefs: WorkloadDetailHrefs }) {
  return (
    <Panel title="Runs" meta="Newest first, from the Job's own events" flush testId="workload-runs">
      {runs.length === 0 ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted">No run of this job in the window.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="min-w-full border-collapse" aria-label="Runs">
            <thead>
              <tr className="border-b border-border">
                <th scope="col" className={TH}>
                  Run
                </th>
                <th scope="col" className={TH}>
                  Result
                </th>
                <th scope="col" className={TH}>
                  Release
                </th>
                <th scope="col" className={TH}>
                  Started
                </th>
                <th scope="col" className={cn(TH, "text-right")}>
                  Duration
                </th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => {
                const status = JOB_STATUS[run.result];
                return (
                  <tr key={run.name} className="border-t border-border/70 first:border-t-0" data-testid={`workload-run-${run.name}`}>
                    <td className={TD}>
                      <Mono value={run.name} length={48} />
                    </td>
                    <td className={TD}>
                      <span className="inline-flex items-center gap-1.5">
                        <Status tone={status.tone} label={status.label} />
                        {run.reason && <span className="text-xs text-danger-ink">· {run.reason}</span>}
                      </span>
                    </td>
                    <td className={TD}>
                      {run.releaseVersion ? (
                        <Link href={hrefs.release(run.releaseVersion)} className={INK_LINK}>
                          {shortVersion(run.releaseVersion)}
                        </Link>
                      ) : (
                        <span className="text-ink-muted">unknown</span>
                      )}
                    </td>
                    <td className={cn(TD, "text-ink-muted")}>{run.startedAtMs ? formatAgo(run.startedAtMs) : "not observed"}</td>
                    <td className={cn(TD, "text-right tabular-nums")}>{run.durationSeconds ? formatSeconds(run.durationSeconds) : <span className="text-ink-muted">—</span>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

// ── Events, History, Config ─────────────────────────────────────────────────

/** Warnings first, then newest first: what a reader opening Events is looking for. */
export function eventsInOrder(events: readonly WorkloadEvent[]): WorkloadEvent[] {
  return [...events].sort((a, b) => Number(b.type === "Warning") - Number(a.type === "Warning") || b.timeMs - a.timeMs);
}

function EventsPanel({ events, range }: { events: WorkloadEvent[]; range: RangePreset }) {
  const ordered = eventsInOrder(events);
  return (
    <Panel title="Kubernetes events" meta="Warnings first" flush testId="workload-events">
      {ordered.length === 0 ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted">No Kubernetes event in the {RANGE_LABELS[range].toLowerCase()}. A steady workload is quiet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="min-w-full border-collapse" aria-label="Kubernetes events">
            <thead>
              <tr className="border-b border-border">
                <th scope="col" className={TH}>
                  When
                </th>
                <th scope="col" className={TH}>
                  Reason
                </th>
                <th scope="col" className={TH}>
                  Object
                </th>
                <th scope="col" className={cn(TH, "w-full")}>
                  Message
                </th>
                <th scope="col" className={cn(TH, "text-right")}>
                  Count
                </th>
              </tr>
            </thead>
            <tbody>
              {ordered.map((event, index) => (
                <tr key={`${event.timeMs}-${event.reason}-${index}`} className="border-t border-border/70 first:border-t-0" data-type={event.type}>
                  <td className={cn(TD, "text-ink-muted")} title={new Date(event.timeMs).toLocaleString()}>
                    {formatAgo(event.timeMs)}
                  </td>
                  <td className={TD}>
                    <Status tone={event.type === "Warning" ? "warning" : "neutral"} label={event.reason} />
                  </td>
                  <td className={cn(TD, "text-ink-muted")}>{event.objectName ? <Mono value={event.objectName} length={32} copy={false} /> : "—"}</td>
                  <td className="max-w-0 px-4 py-2 text-sm text-ink">
                    <span className="line-clamp-2" title={event.message}>
                      {event.message}
                    </span>
                  </td>
                  <td className={cn(TD, "text-right tabular-nums text-ink-muted")}>{event.count > 1 ? `×${event.count}` : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

function HistoryTab({ detail, envId, runs, hrefs }: { detail: WorkloadDetailData | null; envId: string; runs: WorkloadJob[]; hrefs: WorkloadDetailHrefs }) {
  const versions = [...(detail?.versionChanges ?? [])].filter((v) => v.envId === envId).reverse();
  const images = [...(detail?.imageChanges ?? [])].filter((c) => c.envId === envId).reverse();
  return (
    <>
      <Panel title="Versions" meta="The service.version it reported, newest first" testId="workload-history-versions">
        {versions.length === 0 ? (
          <p className="text-sm text-ink-muted">It reported no version in the window.</p>
        ) : (
          <ol className="space-y-3">
            {versions.map((v, index) => (
              <li key={`${v.serviceVersion}-${v.firstSeenMs}`} className="relative pl-6 text-xs">
                <span aria-hidden="true" className={cn("absolute left-1 top-1.5 h-2 w-2 rounded-full border", index === 0 ? "border-success bg-success" : "border-ink-muted bg-surface")} />
                <span className="block text-sm text-ink">{v.serviceVersion}</span>
                <span className="block text-ink-muted">
                  first seen {formatAgo(v.firstSeenMs)}
                  {index > 0 && ` · last seen ${formatAgo(v.lastSeenMs)}`}
                </span>
              </li>
            ))}
          </ol>
        )}
      </Panel>
      <Panel title="Images" meta="Per container, in order of first sight" flush testId="workload-history-images">
        {images.length === 0 ? (
          <p className="px-4 pb-4 pt-1 text-sm text-ink-muted">No image was observed in the window.</p>
        ) : (
          <table className="min-w-full border-collapse" aria-label="Images">
            <thead>
              <tr className="border-b border-border">
                <th scope="col" className={TH}>
                  Container
                </th>
                <th scope="col" className={TH}>
                  Image
                </th>
                <th scope="col" className={TH}>
                  First seen
                </th>
                <th scope="col" className={TH}>
                  Last seen
                </th>
              </tr>
            </thead>
            <tbody>
              {images.map((c) => (
                <tr key={`${c.container}-${c.imageTag}-${c.firstSeenMs}`} className="border-t border-border/70 first:border-t-0">
                  <td className={TD}>{c.container}</td>
                  <td className={TD}>
                    <Mono value={`${c.imageName}${c.imageTag ? `:${c.imageTag}` : ""}`} length={48} />
                  </td>
                  <td className={cn(TD, "text-ink-muted")}>{formatAgo(c.firstSeenMs)}</td>
                  <td className={cn(TD, "text-ink-muted")}>{formatAgo(c.lastSeenMs)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
      {runs.length > 0 && <RunsPanel runs={runs} hrefs={hrefs} />}
    </>
  );
}

function ConfigTab({ summary, env, branch }: { summary: Workload | null; env: string; branch: string | null }) {
  const declared = summary?.declared ?? null;
  if (!declared) {
    return (
      <Panel title="Declared spec" testId="workload-config">
        <p className="text-sm text-ink-muted" data-testid="workload-config-none">
          {env}&apos;s recorded bundle does not declare this workload, so there is no declared spec to show. A bundle is recorded with each deploy through Reliant.
        </p>
      </Panel>
    );
  }
  const links = declarationLinks(declared, branch);
  const notRecorded = <span className="text-ink-muted">not recorded</span>;
  const notDeclared = <span className="text-ink-muted">not declared</span>;
  const resource = (value: string) => (declared.specRecorded ? value || notDeclared : notRecorded);
  const replicas = summary?.desiredReplicas ?? null;
  return (
    <>
      {!declared.specRecorded && (
        <Note tone="neutral" testId="workload-config-partial">
          This bundle was recorded before specs were: only the name, kind and replicas are known. The next deploy records the rest.
        </Note>
      )}
      <Panel title="Declared spec" meta="From the recorded bundle; names and numbers only" testId="workload-config">
        <Facts
          items={[
            [
              "Declared in",
              <span key="src" className="inline-flex flex-wrap items-center gap-2">
                <Mono value={declared.sourcePath || `deploy/kcl/${env}/main.k`} length={48} />
                {links.view && (
                  <a href={links.view} target="_blank" rel="noreferrer" className={cn(INK_LINK, "inline-flex items-center gap-1")}>
                    view
                    <ExternalLink className="h-3 w-3" aria-hidden="true" />
                  </a>
                )}
              </span>,
            ],
            ["Kind", KIND_LABEL[declared.kind] || notRecorded],
            [
              "Replicas",
              declared.replicas !== null ? (
                <span key="r">
                  {declared.replicas}
                  {replicas !== null && replicas !== declared.replicas && <span className="ml-2 text-warning-ink">{replicas} desired by the rollout</span>}
                </span>
              ) : (
                notDeclared
              ),
            ],
            ["Image", declared.image ? <Mono key="i" value={declared.image} length={48} /> : notRecorded],
            ["CPU", resource([formatMillicores(declared.cpuRequestMillicores) && `request ${formatMillicores(declared.cpuRequestMillicores)}`, formatMillicores(declared.cpuLimitMillicores) && `limit ${formatMillicores(declared.cpuLimitMillicores)}`].filter(Boolean).join(" · "))],
            ["Memory", resource([formatQuantityBytes(declared.memoryRequestBytes) && `request ${formatQuantityBytes(declared.memoryRequestBytes)}`, formatQuantityBytes(declared.memoryLimitBytes) && `limit ${formatQuantityBytes(declared.memoryLimitBytes)}`].filter(Boolean).join(" · "))],
            [
              "Ports",
              declared.specRecorded ? (
                declared.ports.length ? (
                  <span key="p" className="flex flex-wrap gap-1.5">
                    {declared.ports.map((p) => (
                      <Tag key={`${p.name}-${p.port}`}>
                        {p.name ? `${p.name} ` : ""}
                        {p.port}/{p.protocol || "tcp"}
                      </Tag>
                    ))}
                  </span>
                ) : (
                  notDeclared
                )
              ) : (
                notRecorded
              ),
            ],
            ...(declared.sourceCommit ? ([["Rendered from", <Mono key="c" value={declared.sourceCommit} />]] as [string, ReactNode][]) : []),
          ]}
        />
      </Panel>
      <Panel title="Environment variables" meta="By name; values never leave the bundle" testId="workload-config-env">
        {!declared.specRecorded ? (
          <p className="text-sm text-ink-muted">Not recorded in this bundle.</p>
        ) : declared.envNames.length === 0 ? (
          <p className="text-sm text-ink-muted">It declares none.</p>
        ) : (
          <ul className="flex flex-wrap gap-1.5">
            {declared.envNames.map((envName) => (
              <li key={envName}>
                <span className="inline-flex h-6 items-center rounded-md border border-border px-2 font-mono text-xs text-ink">{envName}</span>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </>
  );
}

// ── Rail ────────────────────────────────────────────────────────────────────

function Rail({
  name,
  env,
  summary,
  latestRun,
  detail,
  commit,
  hrefs,
  onTab,
}: {
  name: string;
  env: { id: string; name: string };
  summary: Workload | null;
  /** A Job's newest run: its release stands in for the workload's, and it has no rollouts or profiles. */
  latestRun: WorkloadJob | null;
  detail: WorkloadDetailData | null;
  commit: { sha: string; subject: string; author: string } | null;
  hrefs: WorkloadDetailHrefs;
  onTab: (tab: WorkloadTab) => void;
}) {
  const declared = summary?.declared ?? null;
  const exit = summary ? lastExitOf(summary) : null;
  const versions = [...(detail?.versionChanges ?? [])].filter((v) => v.envId === env.id).reverse().slice(0, 5);
  const events = eventsInOrder(detail?.events ?? []).slice(0, 4);
  const container = summary?.containers[0];
  return (
    <aside className="space-y-4" aria-label={`About ${name}`}>
      <Panel title="Provenance" testId="workload-provenance">
        <Facts
          items={[
            [
              "Release",
              summary?.releaseVersion ? (
                <span key="r" className="inline-flex flex-wrap items-center gap-1.5">
                  <WorkloadRelease workload={summary} releaseHref={hrefs.release} />
                  <span className="text-ink-muted">{summary.releaseBasis === "digest" ? "verified by digest" : ""}</span>
                </span>
              ) : latestRun?.releaseVersion ? (
                <span key="r">
                  <Link href={hrefs.release(latestRun.releaseVersion)} className={INK_LINK}>
                    {shortVersion(latestRun.releaseVersion)}
                  </Link>
                  <span className="ml-1.5 text-ink-muted">in effect at the latest run</span>
                </span>
              ) : (
                <span className="text-ink-muted">not established</span>
              ),
            ],
            [
              "Commit",
              commit?.sha ? (
                <span key="c" className="block min-w-0">
                  <Mono value={commit.sha} />
                  {commit.subject && (
                    <span className="block truncate text-ink-muted" title={commit.subject}>
                      {commit.subject}
                    </span>
                  )}
                </span>
              ) : (
                <span className="text-ink-muted">not recorded</span>
              ),
            ],
            [
              "Image",
              declared?.image ? (
                <Mono key="i" value={declared.image} length={26} />
              ) : container ? (
                <Mono key="i" value={`${container.imageName}${container.imageTag ? `:${container.imageTag}` : ""}`} length={26} />
              ) : (
                <span className="text-ink-muted">not observed</span>
              ),
            ],
            ["Declared in", declared ? <Mono key="d" value={declared.sourcePath || `deploy/kcl/${env.name}/main.k`} length={26} copy={false} /> : <span className="text-ink-muted">not declared</span>],
            ...(exit ? ([["Last exit", <span key="e" className="text-warning-ink">{exit.reason}{exit.atMs ? ` · ${formatAgo(exit.atMs)}` : ""}</span>]] as [string, ReactNode][]) : []),
          ]}
        />
      </Panel>
      {!latestRun && (
      <Panel
        title="Rollouts"
        actions={
          <button type="button" onClick={() => onTab("history")} className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink">
            History
            <ArrowRight className="h-3 w-3" aria-hidden="true" />
          </button>
        }
        testId="workload-rollouts"
      >
        {versions.length === 0 ? (
          <p className="text-xs text-ink-muted">No version change in the window.</p>
        ) : (
          <ol className="space-y-3">
            {versions.map((v, index) => (
              <li key={`${v.serviceVersion}-${v.firstSeenMs}`} className="relative pl-6 text-xs">
                <span aria-hidden="true" className={cn("absolute left-1 top-1.5 h-2 w-2 rounded-full border", index === 0 ? "border-success bg-success" : "border-ink-muted bg-surface")} />
                {index < versions.length - 1 && <span aria-hidden="true" className="absolute -bottom-3 left-2 top-4 w-px bg-border" />}
                <span className="block text-ink">{v.serviceVersion}</span>
                <span className="block text-ink-muted">first seen {formatAgo(v.firstSeenMs)}</span>
              </li>
            ))}
          </ol>
        )}
      </Panel>
      )}
      <Panel
        title="Recent events"
        actions={
          <button type="button" onClick={() => onTab("events")} className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink">
            All
            <ArrowRight className="h-3 w-3" aria-hidden="true" />
          </button>
        }
        testId="workload-recent-events"
      >
        {events.length === 0 ? (
          <p className="text-xs text-ink-muted">No Kubernetes event in the window.</p>
        ) : (
          <ul className="divide-y divide-border/70 text-xs">
            {events.map((event, index) => (
              <li key={`${event.timeMs}-${index}`} className="flex items-start gap-2 py-2 first:pt-0 last:pb-0">
                <Status tone={event.type === "Warning" ? "warning" : "neutral"} className="mt-1" />
                <span className="min-w-0 flex-1">
                  <span className="block text-ink" title={event.message}>
                    <span className="font-medium">{event.reason}</span> <span className="line-clamp-2 inline">{event.message}</span>
                  </span>
                </span>
                <span className="shrink-0 text-ink-muted">{formatAgo(event.timeMs).replace(" ago", "")}</span>
              </li>
            ))}
          </ul>
        )}
      </Panel>
      <Panel title="Jump to" testId="workload-jump">
        <nav className="-mx-2 flex flex-col" aria-label="Jump to">
          {(
            [
              [`Traces for ${name}`, () => onTab("traces")],
              ...(latestRun ? [] : ([["Profiles", () => onTab("profiles")]] as const)),
            ] as const
          ).map(([label, go]) => (
            <button key={label} type="button" onClick={go} className="flex h-8 items-center justify-between rounded-md px-2 text-left text-sm text-ink hover:bg-ink/5">
              {label}
              <ArrowRight className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
            </button>
          ))}
          <Link href={hrefs.dashboards} className="no-color flex h-8 items-center justify-between rounded-md px-2 text-sm text-ink hover:bg-ink/5">
            Dashboards
            <ArrowRight className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
          </Link>
        </nav>
      </Panel>
    </aside>
  );
}

// ── Explorer tabs ───────────────────────────────────────────────────────────

/**
 * Logs, Traces or Profiles: the existing explorer (ObservabilityPanel),
 * pinned to this environment and pre-filtered to the workload
 * (`service.name` = the workload name, the node agent's K2 convention). A
 * jump between explorers ("open trace" from a log line) switches the tab; any
 * other key stays in the URL.
 */
function ExplorerTab({
  project,
  env,
  name,
  view,
  search,
  onSearch,
}: {
  project: string;
  env: { id: string; name: string };
  name: string;
  view: ObsView;
  search: WorkloadDetailSearch;
  onSearch: WorkloadDetailProps["onSearch"];
}) {
  const environments = useMemo(() => [{ id: env.id, name: env.name, declared: null }], [env.id, env.name]);
  const panelSearch = useMemo<ObsSearch>(() => {
    const { tab: _tab, ...obs } = search;
    return { ...obs, range: obs.range ?? OVERVIEW_RANGE, view, service: name, envs: [env.name] };
  }, [search, view, name, env.name]);
  const navigate: ObsNavigate = (next, options) => {
    const { view: nextView, envs: _envs, service: _service, ...rest } = next;
    const nextTab = (Object.keys(EXPLORER_TABS) as WorkloadTab[]).find((id) => EXPLORER_TABS[id] === nextView);
    onSearch(nextTab && nextView !== view ? { ...rest, tab: nextTab } : { ...search, ...rest }, options);
  };
  return (
    <Suspense
      fallback={
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      }
    >
      <ObservabilityPanel place={{ kind: "project", project, environments }} search={panelSearch} navigate={navigate} chrome="console" />
    </Suspense>
  );
}
