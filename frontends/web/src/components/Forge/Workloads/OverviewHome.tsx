// Copyright (c) 2025 Reliant Labs

/**
 * OVERVIEW — the project home: is the app OK, what changed, what needs me.
 *
 *   header       the title, the counts in scope, the window.
 *   env strip    one compact cell per environment: its health, the release
 *                live there, who put it there and when.
 *   left         the health KPIs (one card of four figures) and what needs
 *                attention, each item with the release it followed and the
 *                way back.
 *   rail         what changed: recent releases, the tests (the project
 *                shell's landing: Tests/surfaces/OverviewTests.tsx, which
 *                also adds the Tests rows to "Needs attention"), then who
 *                uses the app (the
 *                Users card: accounts, and active, new and returning users
 *                over 7 days, read from the generated Overview dashboard's
 *                Users panels; hidden until there are users to show). It is
 *                kept as a column of its own rather than folded into the left.
 *   below        Services (long-running workloads, one tight row each:
 *                health, ready, release, requests, CPU and memory against
 *                their limits, restarts) with its filters, the Frontends (one
 *                row per site and the address it is live at), the traces and
 *                sessions tiles, Processes, and the Jobs timeline.
 *
 * Density is sized to content: no card is taller than the rows it holds, and
 * a list of two is a list of two lines.
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
 * EACH TILE ITS OWN STATE (tileState.tsx). A failed or slow read costs only
 * the tiles it feeds — never the page below it — and each such tile has its
 * own Retry; a tile with data keeps it through a failed refresh; a partial
 * answer names the figures it lacks.
 *
 * This component knows nothing about routing: the hrefs, the scope and the
 * URL's search state come in as props, so the preview and the tests mount it
 * on fixtures.
 */

import { useMemo, type CSSProperties, type ReactNode } from "react";
import { ArrowRight, Boxes, CircleCheck, RotateCcw } from "lucide-react";

import { FilterBar, FilterChip, FilterMenu, FilterSearch } from "@/components/forge-ui/filter_bar";
import Link from "@/components/forge-ui/link";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import PageHeader from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useAlerts, useObservabilityOrg, useServiceOverview, useWorkloads } from "@/hooks/observability-queries";
import { useProjectReleases } from "@/hooks/release-queries";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import type { ObsScope, Workload } from "@/services/controlPlane/observability/types";
import type { DomainsState } from "@/hooks/forge-domain-queries";
import { frontendsInScope } from "@/services/forge/frontends";
import type { ForgeHostedWorkload } from "@/services/forge/topology";
import { RANGE_LABELS, RANGE_PRESETS, resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";
import { RangeCapNote } from "../Observability/RangeCapNote";
import { cn } from "@/lib/utils";

import { DeployStatusUnread, type DeployStatusUnreadState } from "../Console/DeployStatusUnread";
import { SummaryLine } from "../Observability/Leads/leadParts";
import { overviewSummary } from "../Observability/Leads/leadStory";
import { ENV_ALL } from "../Console/envScope";
import { useConsoleNavExtras } from "../Console/consoleNavExtras";
import { consolePath, withSearch } from "../Console/paths";
import { AskReliantMenu, DeployWithAgentButton } from "../Observability/AskReliant/AskReliant";
import type { AskSubject } from "../Observability/AskReliant/askReliantPrompt";
import { ENV_KEY } from "../Observability/attributes";
import { VariableSelect } from "../Observability/Dashboards/parts";
import { promotionIsSuspect, type RollbackTarget } from "../Promote/rollback";
import { HealthStrip } from "../Observability/Home/HealthStrip";
import { latParam, SessionsTile, TracesTile } from "../Observability/Home/SignalTiles";
import { ObsError, ObsNotSetUp, ObsAsOf } from "../Observability/ObsStates";
import { Ago, INK_BUTTON, INK_LINK } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import {
  attentionItems,
  formatAge,
  jobRows,
  previewsCell,
  serviceRows,
  stripCell,
  OVERVIEW_RANGE,
  type AttentionItem,
  type JobRow,
  type KindFilter,
  type StatusFilter,
  type StripCell,
  type StripEnv,
} from "./workloadModel";
import { SkeletonRows } from "./workloadParts";
import { FrontendList, FrontendsEmpty, FrontendsLoading } from "../Frontends/FrontendList";
import { OverviewAnalyticsBox } from "./landing/OverviewAnalyticsBox";
import { OverviewServicesStrip } from "./landing/OverviewServicesStrip";
import { JobTimeline } from "./JobTimeline";
import { ServiceCards } from "./ServiceCards";
import { UsersCard } from "./UsersCard";
import { filterRows, overviewRows, rowKindsOf, ROW_KIND_LABEL } from "./overviewRows";
import { PartialNote, StaleNote, keepsStaleResult } from "./tileState";
import { OverviewTestsCard, TestsAttentionRows } from "../Tests/surfaces/OverviewTests";

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
  /** The Users page: the Users card's "View users". */
  users?: string;
  /** A release dashboard's page by key: the Users card's "Open Users dashboard". */
  dashboard?: (key: string) => string;
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
  /** The environment read failed: `envs` is empty because nothing was read (DeployStatusUnread). */
  envsUnread?: DeployStatusUnreadState;
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
   * drift call ("wrong version") the telemetry cannot make. For a
   * self-managed environment, only what the platform serves for it (its sites).
   */
  hosted?: HostedByEnv;
  /** GetStatus has not answered for every hosted environment yet. */
  hostedLoading?: boolean;
  /** The org's domains, to show which custom domains serve each frontend. Absent: not read (no domain column shown). */
  domains?: DomainsState;
  /** The Domains screen: where "Add a domain" lands. */
  domainsHref?: string;
  /**
   * The project shell's summary Overview (U6b round 3, prototype): the full
   * services table, frontends, signal tiles and job runs move to Services,
   * and the Overview keeps a compact strip of the services linking there.
   */
  summary?: OverviewSummaryLinks;
  /** The project's chats (U6c, the merged landing): waiting on the person, then the latest. In the rail, above the releases. */
  chats?: ReactNode;
}

export interface OverviewSummaryLinks {
  services: string;
  service: (name: string, env: string) => string;
  /** The database's home (Data), scoped to its environment. */
  data?: (env: string) => string;
  /** Analytics, and its Events & metrics tab: the Analytics box's links (U6e). */
  analytics?: string;
  events?: string;
}

export type HostedByEnv = ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>;
const NO_HOSTED: HostedByEnv = new Map();

export { OVERVIEW_RANGE } from "./workloadModel";

export function OverviewHome({
  project,
  env,
  envs,
  envsLoading,
  envsUnread,
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
  summary,
  chats,
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
  // Every run in scope, for the jobs' timeline; the window it is drawn over.
  const jobRuns = useMemo(() => (listed.data?.jobs ?? []).filter((run) => all || run.envName === env), [listed.data, all, env]);
  const jobWindow = useMemo(() => resolveWindow(range), [range, listed.dataUpdatedAt]); // eslint-disable-line react-hooks/exhaustive-deps

  // The list's request side: GetServiceOverview for the scope.
  const redGroupBy = useMemo(() => (kpiScope?.envIds.length === 0 ? [ENV_KEY] : []), [kpiScope]);
  const red = useServiceOverview(kpiScope, range, [], redGroupBy);
  // Its own state: a failed telemetry read blanks only the figures, never the rows the workload read drew.
  const redFailed = !!red.error && !keepsStaleResult(red.error, red.data);
  const obsHref = (view: "traces" | "sessions" | "logs", extra: Record<string, unknown> = {}) =>
    withSearch(consolePath(project, { kind: "obs-view", view }), { env, range, ...extra });

  const projectEnvIds = useMemo(() => new Set(envs.map((candidate) => candidate.id)), [envs]);
  const envNameOf = useMemo(() => (id: string) => envs.find((candidate) => candidate.id === id)?.name, [envs]);
  // ONE list: services, jobs and the processes that send telemetry with no workload (overviewRows.ts).
  const rows = useMemo(
    () =>
      overviewRows({
        services,
        jobs,
        workloads,
        telemetry: redFailed ? [] : (red.data?.services ?? []),
        alerts: (alerts.data ?? []).filter((alert) => projectEnvIds.has(alert.envId)),
        env,
        envNameOf,
        workloadsUnread: !!listed.error && !listed.data,
      }),
    [services, jobs, workloads, red.data, redFailed, alerts.data, projectEnvIds, env, envNameOf, listed.error, listed.data]
  );
  const visible = useMemo(() => filterRows(rows, search), [rows, search]);
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
  // Each tile draws its own state (tileState.tsx): a failed workload read costs
  // only the tiles it feeds, a failed refresh keeps the last result on screen,
  // and Retry refetches this one query, joining a fetch already in flight.
  const workloadsFailed = !!listed.error && !keepsStaleResult(listed.error, listed.data);
  const workloadsStale = !!listed.error && !workloadsFailed;
  const missing = listed.data?.unavailableSections ?? [];
  const servicesMissing = missing.filter((section) => section !== "jobs");
  const retryWorkloads = () => void listed.refetch({ cancelRefetch: false });
  const where = all ? "all environments" : env;
  const unhealthy = rows.filter((row) => row.attention).length;

  // NOTHING DEPLOYED: the header states no counts. They would be drawn from
  // telemetry about environments the page is about to say do not exist.
  const empty = !envsLoading && envs.length === 0;
  // An empty list from a failed read says nothing about what is deployed.
  const unread = empty && !!envsUnread;
  const extras = useConsoleNavExtras();
  const overviewSubtitle = extras.subtitle?.("overview");
  // The led console's one sentence: what "Needs attention" holds, once it is known.
  const ledSummary = extras.led && !empty && !unrecorded ? overviewSummary(loading || workloadsFailed ? null : attention, all ? null : env) : null;
  const rangeControl = (
    <span className="inline-flex flex-wrap items-center gap-2">
      <VariableSelect
        id="overview-range"
        label="time"
        value={range}
        options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
        onChange={(value) => onSearch({ range: value === OVERVIEW_RANGE ? undefined : (value as RangePreset) })}
      />
      <RangeCapNote range={range} read="overview" subject="Requests and errors are" testId="overview-range-cap" />
    </span>
  );
  // One compact row: the title, its counts inline, the window on the right.
  const header = (
    <>
    <PageHeader
      density="compact"
      title="Overview"
      subtitle={overviewSubtitle ?? undefined}
      meta={
        unread ? undefined : empty ? (
          <span data-testid="overview-counts">Not deployed</span>
        ) : loading ? (
          <span className="inline-flex h-4 w-56 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading" />
        ) : (
          <>
            <span data-testid="overview-counts">
              {services.length} {services.length === 1 ? "service" : "services"} and {jobs.length} {jobs.length === 1 ? "job" : "jobs"} in {where}
            </span>
            {unhealthy > 0 && <Status tone="warning" label={`${unhealthy} need${unhealthy === 1 ? "s" : ""} attention`} />}
            {listed.data && <ObsAsOf updatedAt={listed.dataUpdatedAt} />}
          </>
        )
      }
      controls={empty ? undefined : rangeControl}
      className="mb-0"
    />
    {ledSummary && <SummaryLine tone={ledSummary.tone}>{ledSummary.text}</SummaryLine>}
    {extras.intro?.("overview")}
    </>
  );

  if (empty) {
    return (
      <div className="space-y-4" data-testid="overview" data-state={unread ? "unread" : "empty"}>
        {header}
        {deploys}
        {chats}
        {envsUnread ? (
          <DeployStatusUnread state={envsUnread} testId="overview-unread" />
        ) : (
          <GettingStarted
            testId="overview-getting-started"
            icon={<Boxes className="h-4 w-4" />}
            title="Nothing deployed yet"
            about="The overview shows every service, worker and job of each environment: whether it is healthy, its CPU and memory against its limits, restarts, and recent events."
            agent={<DeployWithAgentButton env={null} forgeProject={project} />}
            steps={[
              {
                text: "Deploy an environment through Reliant. Its workloads and their health appear here as soon as they start.",
                command: "reliant forge env deploy <env>",
              },
            ]}
            docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
          >
            <p>
              {project} has no deployed environment for Reliant to watch yet. A local environment (reliant forge env up) runs on your machine and
              reports nothing here.
            </p>
          </GettingStarted>
        )}
      </div>
    );
  }

  const filtered = !!(search.q || search.status || search.kind);
  const windowWords = RANGE_LABELS[range].toLowerCase();

  // THE PROJECT SHELL'S LANDING (U6e, owner: "better separation of
  // sections"): the headline signals, then one card per question — what needs
  // you, what runs (a compact strip; the full live view is Services), your
  // chats, what shipped, who uses it. The full tables below are the
  // standalone console's.
  if (summary) {
    return (
      <div className="space-y-6" data-testid="overview" data-layout="landing" data-state={loading ? "loading" : workloadsFailed ? "error" : "ready"}>
        {header}
        {deploys}
        <EnvStrip envs={envs} env={env} workloads={workloads} jobs={allJobs} loading={loading} failed={workloadsFailed} scopedHref={hrefs.scoped} />
        {unrecorded ? (
          <p className="text-ui text-ink-muted" data-testid="overview-unrecorded-env">
            Reliant has no record of <span className="font-medium text-ink">{env}</span>, so nothing it runs is reported here. Once it is deployed through Reliant, its
            services and their health appear on this page.
          </p>
        ) : (
          <HealthStrip scope={kpiScope} range={range} environments={environments} envId={scopedEnv?.id ?? null} alertsHref={hrefs.alerts} />
        )}
        <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-6 xl:grid-cols-[minmax(0,1fr)_340px]">
          <div className="min-w-0 space-y-6">
            {!unrecorded && (
              <LandingCard testId="overview-card-attention">
                <NeedsAttention
                  items={attention}
                  loading={loading}
                  failed={workloadsFailed}
                  where={where}
                  showEnv={all}
                  hrefOf={(item) => (item.workload ? hrefs.workload(item.workload, item.envName) : hrefs.alerts)}
                  navigateTo={navigateTo}
                  envs={envs}
                  releaseHref={hrefs.release}
                  rollback={rollback}
                  tests={<TestsAttentionRows project={project} />}
                />
              </LandingCard>
            )}
            <LandingCard testId="overview-card-services">
              {notSetUp ? <ObsNotSetUp subject={project} topic="overview" env={all ? undefined : env} /> : <OverviewServicesStrip app={project} env={env} range={range} links={summary} />}
            </LandingCard>
            {chats && <LandingCard testId="overview-card-chats">{chats}</LandingCard>}
          </div>
          <aside className="min-w-0 space-y-6" aria-label="Releases and analytics" data-testid="overview-rail">
            <LandingCard testId="overview-card-releases">
              <RecentReleases project={project} envs={envs} hrefs={hrefs} />
            </LandingCard>
            <OverviewTestsCard project={project} frame={(card) => <LandingCard testId="overview-card-tests">{card}</LandingCard>} />
            {summary.analytics && (
              <LandingCard testId="overview-card-analytics">
                <OverviewAnalyticsBox scope={kpiScope} analyticsHref={summary.analytics} eventsHref={summary.events ?? summary.analytics} />
              </LandingCard>
            )}
          </aside>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-10" data-testid="overview" data-state={loading ? "loading" : workloadsFailed ? "error" : "ready"}>
      {header}
      {deploys}
      <EnvStrip envs={envs} env={env} workloads={workloads} jobs={allJobs} loading={loading} failed={workloadsFailed} scopedHref={hrefs.scoped} />

      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-x-12 gap-y-10 xl:grid-cols-[minmax(0,1fr)_300px]">
        <div className="min-w-0 space-y-10">
          {unrecorded ? (
            <p className="text-ui text-ink-muted" data-testid="overview-unrecorded-env">
              Reliant has no record of <span className="font-medium text-ink">{env}</span>, so nothing it runs is reported here. Once it is deployed through Reliant, its
              services and their health appear on this page.
            </p>
          ) : (
            <>
              <HealthStrip scope={kpiScope} range={range} environments={environments} envId={scopedEnv?.id ?? null} alertsHref={hrefs.alerts} />
              <NeedsAttention
                items={attention}
                loading={loading}
                failed={workloadsFailed}
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
        {/* What changed, then who uses the app. */}
        <aside className="min-w-0 space-y-10" aria-label="Recent changes" data-testid="overview-rail">
          {chats}
          <RecentReleases project={project} envs={envs} hrefs={hrefs} />
          {!unrecorded && !extras.led && <UsersCard scope={kpiScope} project={project} range={range} usersHref={hrefs.users} dashboardHref={hrefs.dashboard} />}
        </aside>
      </div>

      {notSetUp ? (
        <ObsNotSetUp subject={project} topic="overview" env={all ? undefined : env} />
      ) : (
        <section className="space-y-3" aria-labelledby="overview-services-heading" data-testid="overview-services">
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <h2 id="overview-services-heading" className="text-section text-ink" title="Everything the app runs: services, jobs and processes, with their health and requests. CPU, memory and pods are on each workload's page.">
              Services
              {!loading && <span className="ml-2 text-ui font-normal tabular-nums text-ink-muted">{rows.length}</span>}
            </h2>
            {/* The request figures are their own read: during a quota pause they are the last result, and say so. */}
            {red.data && <ObsAsOf updatedAt={red.dataUpdatedAt} />}
            <FilterBar
              label="Filter services"
              active={[search.q, search.status, search.kind].filter(Boolean).length}
              onClear={() => onSearch({ q: undefined, status: undefined, kind: undefined })}
              testId="overview-toolbar"
              className="w-full sm:w-auto"
            >
              <FilterSearch value={search.q ?? ""} onChange={(q) => onSearch({ q: q || undefined })} placeholder="Filter services" testId="overview-filter" />
              <FilterChip
                on={search.status === "attention"}
                count={filterRows(rows, { ...search, status: "attention" }).length}
                onClick={() => onSearch({ status: search.status ? undefined : "attention" })}
                testId="overview-chip-status"
                title={search.status ? "Show every service" : "Show only the services that need attention"}
              >
                Needs attention
              </FilterChip>
              <FilterMenu
                label="Kind"
                value={search.kind}
                options={rowKindsOf(rows).map((kind) => ({ value: kind, label: ROW_KIND_LABEL[kind], count: filterRows(rows, { ...search, kind }).length }))}
                onChange={(kind) => onSearch({ kind: kind as KindFilter | undefined })}
                testId="overview-chip-kind"
              />
            </FilterBar>
          </div>
          {workloadsFailed && (
            <div className="py-2" data-testid="overview-services-error">
              <ObsError error={listed.error} subject={project} onRetry={retryWorkloads} retrying={listed.isFetching} />
            </div>
          )}
          {/* The telemetry is its own read: a failed workload read still lists what sends requests, with its figures. */}
          {(!workloadsFailed || rows.length > 0) && (
            <div data-testid="overview-services-table">
              <ServiceCards
                rows={visible}
                loading={loading && !workloadsFailed}
                showEnv={all}
                hosted={hosted}
                hrefs={hrefs}
                rowHrefs={{
                  logs: (name) => obsHref("logs", { service: name, severity: "error,fatal" }),
                  traces: (name) => obsHref("traces", { service: name }),
                }}
                redLoading={red.isLoading}
                empty={
                  filtered ? (
                    <>
                      Nothing matches these filters.{" "}
                      <button type="button" className={INK_BUTTON} onClick={() => onSearch({ q: undefined, status: undefined, kind: undefined })}>
                        Clear them
                      </button>
                    </>
                  ) : unrecorded ? (
                    <>Nothing is reported for {env}.</>
                  ) : (
                    <>
                      No service or job has reported from {where} in the {windowWords}. Workloads appear once Reliant sees their pods.
                    </>
                  )
                }
              />
            </div>
          )}
          {redFailed && (
            <div className="flex flex-wrap items-center gap-x-2 text-meta text-ink-muted" data-testid="overview-services-red-error">
              Requests, error logs and alerts per service could not be read, so those columns are empty.
              <button type="button" className={INK_BUTTON} onClick={() => void red.refetch({ cancelRefetch: false })} disabled={red.isFetching} data-testid="overview-services-red-retry">
                {red.isFetching ? "Retrying…" : "Try again"}
              </button>
            </div>
          )}
          {listed.data?.truncated && (
            <p className="text-meta text-ink-muted" data-testid="overview-services-truncated">
              Showing the first 500 workloads.
            </p>
          )}
          {!workloadsFailed && <PartialNote sections={servicesMissing} onRetry={retryWorkloads} retrying={listed.isFetching} testId="overview-services-partial" />}
          {workloadsStale && <StaleNote onRetry={retryWorkloads} retrying={listed.isFetching} testId="overview-services-stale" />}
        </section>
      )}

      {!summary && !unrecorded && (
        <FrontendsPanel
          scope={env}
          envs={envs}
          hosted={hosted}
          loading={envsLoading || hostedLoading}
          domains={domains}
          domainsHref={domainsHref}
        />
      )}

      {!summary && !unrecorded && !notSetUp && (
        // Each tile reads, loads, fails and retries on its own (SignalTiles).
        <div className="grid grid-cols-[minmax(0,1fr)] gap-x-12 gap-y-10 lg:grid-cols-2" data-testid="overview-signals">
          <TracesTile
            scope={kpiScope}
            range={range}
            band={{ href: (band) => obsHref("traces", { lat: latParam(band) }) }}
            all={{ href: () => obsHref("traces") }}
          />
          <SessionsTile scope={kpiScope} range={range} session={{ href: (id) => obsHref("sessions", { session: id }) }} all={{ href: () => obsHref("sessions") }} />
        </div>
      )}

      {!summary && !notSetUp && (
        <div>
          <Section title="Job runs" meta={`Every run of each job, ${windowWords}`} testId="overview-jobs">
            {workloadsFailed ? (
              <div className="py-2" data-testid="overview-jobs-error">
                <ObsError error={listed.error} subject={project} onRetry={retryWorkloads} retrying={listed.isFetching} />
              </div>
            ) : missing.includes("jobs") ? (
              // Never "no job has run": the jobs were not read.
              <PartialNote sections={["jobs"]} onRetry={retryWorkloads} retrying={listed.isFetching} testId="overview-jobs-partial" />
            ) : (
              <JobTimeline
                rows={jobs}
                runs={jobRuns}
                window={jobWindow}
                loading={loading}
                showEnv={all}
                hrefs={hrefs}
                empty={<>No job has run in {where} in the {windowWords}. Migrations and provisioning jobs appear here after the next deploy that runs them.</>}
              />
            )}
            {workloadsStale && <StaleNote onRetry={retryWorkloads} retrying={listed.isFetching} testId="overview-jobs-stale" />}
            {listed.data?.jobsTruncated && (
              <p className="pt-2 text-meta text-ink-muted" data-testid="overview-jobs-truncated">
                Built from the newest 200 runs in the {windowWords}; older runs are not counted.
              </p>
            )}
          </Section>
        </div>
      )}
    </div>
  );
}

/**
 * A section of the page: its heading, what it covers, a link one level down,
 * then its content on the page itself. No card: whitespace separates sections.
 */
function Section({ title, meta, actions, testId, children }: { title: ReactNode; meta?: ReactNode; actions?: ReactNode; testId?: string; children: ReactNode }) {
  return (
    <section className="min-w-0 space-y-3" data-testid={testId}>
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="min-w-0 text-section text-ink">
          {title}
          {meta && <span className="ml-2 text-meta font-normal text-ink-muted">{meta}</span>}
        </h2>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}

// ── Frontends ───────────────────────────────────────────────────────────────

/**
 * The scope's frontends beside its services: the sites people open, each with
 * the address it is live at. Read from the GetStatus observations the page
 * already holds (`hosted`), so it costs no request of its own. A self-managed
 * environment runs its services on a cluster the customer owns, but the
 * platform may still serve its sites (control-plane's prod serves reliant-web),
 * so it is covered too; a scope of only self-managed environments with no site
 * omits the panel rather than inviting a frontend it may never have.
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
  const covered = envs.filter((candidate) => (scope === ENV_ALL ? !candidate.preview : candidate.name === scope));
  const frontends = frontendsInScope(scope, envs, hosted, domains?.availability === "available" ? domains.domains : null);
  if (frontends.length === 0 && !covered.some((candidate) => candidate.placed)) return null;
  // Shaped like Services above it: a heading with its count, then one tight row per site.
  return (
    <section className="space-y-3" aria-labelledby="overview-frontends-heading" data-testid="overview-frontends">
      <h2 id="overview-frontends-heading" className="text-section text-ink" title="Websites Reliant hosts, and the address each is live at">
        Frontends
        {frontends.length > 0 && <span className="ml-2 text-ui font-normal tabular-nums text-ink-muted">{frontends.length}</span>}
      </h2>
      {loading && frontends.length === 0 ? (
        <FrontendsLoading />
      ) : frontends.length === 0 ? (
        <FrontendsEmpty env={scope === ENV_ALL ? undefined : scope} />
      ) : (
        <div className="border-t border-border">
          <FrontendList frontends={frontends} showEnv={scope === ENV_ALL} domainsHref={domainsHref} />
        </div>
      )}
    </section>
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
      // One row of environments on a desktop, read like tabs: no boxes, the
      // one in scope underlined. One per line on a phone, where five in a row
      // left each ~70px and clipped its status.
      className="grid grid-cols-1 gap-x-8 border-b border-border md:grid-cols-[repeat(var(--env-cells),minmax(0,1fr))]"
      style={{ "--env-cells": count } as CSSProperties}
      data-testid="overview-env-strip"
    >
      {cells.map((cell) => (
        <EnvCell key={cell.env.name} cell={cell} selected={cell.env.name === env} loading={loading} failed={failed} href={scopedHref(cell.env.name)} />
      ))}
      {previewCell && (
        <div className="relative min-w-0 pb-4 pt-1" data-testid="overview-env-previews" data-selected={previewSelected ? "true" : undefined}>
          <div className="flex items-center justify-between gap-2">
            <span className="text-ui font-medium text-ink-muted">Previews</span>
            {!loading && !failed && <Status tone={previewCell.tone} label={previewCell.label} size="sm" />}
          </div>
          <div className="mt-1 flex min-w-0 items-baseline gap-1.5 text-meta">
            <span className="text-ink">{previews.length} running</span>
            {!loading && !failed && (
              <span className="truncate text-ink-muted">
                · {previewCell.services} {previewCell.services === 1 ? "service" : "services"}
              </span>
            )}
          </div>
          <div className="flex flex-wrap gap-x-1.5 text-meta text-ink-muted">
            {previews.map((preview, index) => (
              <span key={preview.name}>
                {index > 0 && <span aria-hidden="true">· </span>}
                <Link
                  href={scopedHref(preview.name)}
                  aria-current={preview.name === env ? "page" : undefined}
                  className={cn("no-color rounded text-ink-muted hover:text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", preview.name === env && "font-medium text-ink")}
                >
                  {preview.name}
                </Link>
              </span>
            ))}
          </div>
          {previewSelected && <span aria-hidden="true" className="absolute inset-x-0 -bottom-px h-0.5 rounded-full bg-ink" />}
        </div>
      )}
    </div>
  );
}

function EnvCell({
  cell,
  selected,
  loading,
  failed,
  href,
}: {
  cell: StripCell;
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
      className="no-color group relative block min-w-0 rounded-t-md pb-4 pt-1 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      <div className="flex items-center justify-between gap-2">
        <span className={cn("text-ui font-medium", selected ? "text-ink" : "text-ink-muted group-hover:text-ink")}>{env.name}</span>
        {loading ? (
          <span className="h-3 w-14 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-hidden="true" />
        ) : failed ? (
          <span className="text-meta text-ink-muted">Health unavailable</span>
        ) : (
          <Status tone={cell.tone} label={cell.label} size="sm" />
        )}
      </div>
      {/* The release live there, then who put it there and when: one line. */}
      <div className="mt-1 flex min-w-0 items-baseline gap-1.5 text-meta">
        <span className="shrink-0 tabular-nums text-ink" title={env.release || undefined}>
          {env.release ? shortVersion(env.release) : <span className="text-ink-muted">No release</span>}
        </span>
        {env.release && (
          <span className="truncate text-ink-muted">
            {env.promotedBy ?? "promoted"}
            {env.promotedAt && " · "}
            <Ago at={env.promotedAt} />
          </span>
        )}
      </div>
      {/* A failed job is not a workload's health: it says so on its own line, only when there is one. */}
      {!loading && cell.failedJobs > 0 && (
        <div className="truncate text-meta text-danger-ink">
          {cell.failedJobs} job{cell.failedJobs === 1 ? "" : "s"} failed
        </div>
      )}
      {selected && <span aria-hidden="true" className="absolute inset-x-0 -bottom-px h-0.5 rounded-full bg-ink" />}
    </Link>
  );
}

/** One block of the landing: a card, so each section reads as its own (U6e). */
function LandingCard({ testId, children }: { testId: string; children: ReactNode }) {
  return (
    <div className="min-w-0 rounded-xl border border-border bg-surface px-5 py-4" data-testid={testId}>
      {children}
    </div>
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
  tests,
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
  /** The Tests rows (Tests/surfaces): the default branch's, whatever the scope. */
  tests?: ReactNode;
}) {
  return (
    <Section title="Needs attention" testId="overview-attention">
      {loading ? (
        <table className="w-full">
          <tbody>
            <SkeletonRows columns={3} rows={2} />
          </tbody>
        </table>
      ) : failed && items.length === 0 ? (
        <p className="text-ui text-ink-muted" data-testid="overview-attention-unread">
          Workload health could not be read, so this cannot say what needs attention. No alert is firing in {where}.
        </p>
      ) : items.length === 0 ? (
        <p className="flex items-center gap-2 text-ui text-ink-muted" data-testid="overview-attention-none">
          <CircleCheck className="h-4 w-4 shrink-0 text-success-ink" aria-hidden="true" />
          Nothing needs attention in {where}.
        </p>
      ) : (
        <>
        {/* The alerts are their own read: a workload read that failed takes only its own rows away. */}
        {failed && (
          <p className="pb-2 text-ui text-ink-muted" data-testid="overview-attention-unread">
            Workload health could not be read, so only the firing alerts are listed.
          </p>
        )}
        {/* On a phone a row is two lines (the subject, then the detail under it) rather than one wider than the screen. */}
        <div className="border-t border-border" data-testid="overview-attention-scroll">
        <table className="w-full text-ui" aria-label="Needs attention">
          <tbody>
            {items.map((item) => {
              const href = hrefOf(item);
              const suspect = suspectOf(item, envs);
              const target = rollback?.targets.get(item.envName);
              return (
                <tr
                  key={item.key}
                  className="relative cursor-pointer border-b border-border-subtle hover:bg-ink/5 max-sm:grid max-sm:grid-cols-[1.5rem_minmax(0,1fr)_auto_auto] max-sm:items-center max-sm:py-1.5"
                  onClick={(event) => {
                    if ((event.target as HTMLElement).closest("a, button")) return;
                    navigateTo(href);
                  }}
                  data-testid={`attention-${item.key}`}
                  data-tone={item.tone}
                >
                  <td className="h-[52px] w-6 pl-3 pr-0 max-sm:h-7">
                    <Status tone={item.tone} />
                  </td>
                  {/* min-w: when the row outgrows a phone, the subject keeps room to read rather than the detail. */}
                  <td className="h-[52px] min-w-40 max-w-0 px-3 max-sm:h-7 max-sm:min-w-0 max-sm:max-w-none max-sm:pr-1">
                    <Link href={href} className="no-color block truncate text-ink">
                      <span className="font-medium">{item.subject}</span> {item.what}
                    </Link>
                  </td>
                  {showEnv && (
                    <td className="h-10 w-px whitespace-nowrap px-2 max-sm:w-auto max-sm:col-start-2 max-sm:row-start-2 max-sm:h-auto max-sm:px-3">
                      <Tag>{item.envName}</Tag>
                    </td>
                  )}
                  <td
                    className="h-[52px] w-px max-w-64 truncate whitespace-nowrap px-4 text-right text-meta text-ink-muted max-sm:col-span-3 max-sm:col-start-2 max-sm:h-auto max-sm:w-auto max-sm:max-w-none max-sm:px-3 max-sm:pb-1 max-sm:text-left"
                    title={item.detail}
                  >
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
                  <td className="h-[52px] w-px whitespace-nowrap pr-2 text-right max-sm:w-auto max-sm:col-start-3 max-sm:row-start-1 max-sm:h-7" data-testid={`attention-ask-${item.key}`}>
                    <AskReliantMenu subject={attentionSubject(item)} label={`Ask Reliant about ${item.subject}`} />
                  </td>
                  {rollback && (
                    <td className="h-[52px] w-px whitespace-nowrap pr-3 text-right max-sm:w-auto max-sm:col-start-4 max-sm:row-start-1 max-sm:h-7">
                      {target && (
                        <button
                          type="button"
                          onClick={() => rollback.onRollback(target)}
                          disabled={!!rollback.blockedReason}
                          title={rollback.blockedReason ?? `Plan a rollback of ${target.env} to ${target.previous}`}
                          aria-label={`Roll back ${target.env} to ${shortVersion(target.previous)}`}
                          className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-2.5 text-meta font-medium text-ink hover:bg-ink/5 disabled:cursor-not-allowed disabled:opacity-60"
                          data-testid={`attention-rollback-${item.key}`}
                        >
                          <RotateCcw className="h-3 w-3" aria-hidden="true" />
                          {/* A phone keeps the icon; the button's name says the whole of it. */}
                          <span className="max-sm:sr-only">
                            Roll back {target.env} to {shortVersion(target.previous)}
                          </span>
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
        </div>
        </>
      )}
      {tests}
    </Section>
  );
}

/** What "Ask Reliant" hands an agent about a row: references only (askReliantPrompt). */
export function attentionSubject(item: AttentionItem): AskSubject {
  if (item.kind === "alert") return { kind: "alert", rule: item.subject, fingerprint: "", env: item.envName || null, state: "firing" };
  return {
    kind: "workload",
    name: item.workload ?? item.subject,
    env: item.envName || null,
    job: item.kind === "job",
    state: item.what,
    release: item.release || undefined,
  };
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
    <Link href={href} className="no-color -mr-2 inline-flex h-7 items-center gap-1 rounded-md px-2 text-meta text-ink-muted hover:bg-ink/5 hover:text-ink">
      {children}
      <ArrowRight className="h-3 w-3" aria-hidden="true" />
    </Link>
  );
}

function RecentReleases({ project, envs, hrefs }: { project: string; envs: StripEnv[]; hrefs: OverviewHrefs }) {
  const releases = useProjectReleases(project);
  const recent = (releases.data ?? []).slice(0, 3);
  return (
    <Section title="Recent releases" actions={<RailLink href={hrefs.releases}>All releases</RailLink>} testId="overview-recent-releases">
      {releases.isLoading ? (
        <div className="h-20 animate-pulse rounded-lg bg-ink/5 motion-reduce:animate-none" aria-label="Loading releases" />
      ) : releases.error && !releases.data ? (
        <p className="text-ui text-ink-muted">Releases could not be read right now.</p>
      ) : recent.length === 0 ? (
        <p className="text-ui text-ink-muted">No release yet. Your first deploy cuts one.</p>
      ) : (
        // One release, two lines: the version, where it is live and when; then what it changed and who cut it.
        <ol className="space-y-4">
          {recent.map((release) => {
            const live = envs.filter((candidate) => candidate.release === release.version).map((candidate) => candidate.name);
            return (
              <li key={release.version} className="min-w-0 text-meta" data-testid={`overview-release-${release.version}`}>
                <div className="flex min-w-0 items-baseline gap-2">
                  <Link href={hrefs.release(release.version)} className="no-color shrink-0 text-ui font-medium tabular-nums text-ink hover:underline" title={release.version}>
                    {shortVersion(release.version)}
                  </Link>
                  <span className={cn("min-w-0 truncate", live.length ? "text-ink" : "text-ink-muted")}>
                    {live.length ? `live in ${live.join(", ")}` : "not live"}
                  </span>
                  <span className="ml-auto shrink-0 text-ink-muted">
                    <Ago at={release.createdAt} />
                  </span>
                </div>
                {(release.subject || release.author) && (
                  <span className="block truncate text-ink-muted" title={release.subject || undefined}>
                    {release.author && <span className="text-ink">{release.author}</span>}
                    {release.author && release.subject ? " · " : ""}
                    {release.subject}
                  </span>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </Section>
  );
}
