// Copyright (c) 2026 Reliant Labs

/**
 * OBSERVABILITY › DASHBOARDS › OVERVIEW: the landing view of an app's
 * dashboards — what is wrong now, and which dashboard to open about it.
 *
 *   health strip      requests · error rate · p95 · alerts firing (HealthStrip,
 *                     the project Overview's, so the two cannot disagree)
 *   process health    per service: rate, errors, p95 (ProcessHealth)
 *   groups            one card per dashboard group, with its count and its
 *                     first dashboards; "View all" opens the list narrowed
 *   rail              open issues, failing jobs, recent releases, starred
 *                     and recently viewed dashboards
 *
 * Composed in the UI from the RPCs the console already reads (ListDashboards,
 * GetServiceOverview, QueryPanel, ListWorkloads, ListAlerts, ListReleases,
 * ListIssues): there is no hub spec and no query path of its own. Each card
 * says plainly when its source cannot answer, rather than showing a zero.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight, Star } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useAlerts, useIssues, useObservabilityOrg, useReleases, useWorkloads } from "@/hooks/observability-queries";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { DeliveryScope, Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import type { IssueScope } from "@/services/controlPlane/observability/issuesTypes";
import type { ObsScope, WorkloadJob } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { ProcessHealth } from "../../Workloads/ProcessHealth";
import { formatAgo } from "../format";
import { HealthStrip } from "../Home/HealthStrip";
import type { ObsEnvironment } from "../obsPlace";
import { GROUP_ABOUT, GROUP_LABELS, groupIndex, type DashboardGroup } from "./dashboardGroups";
import { recentOf, starredOf } from "./dashboardModel";
import { failingJobs, HUB_RANGE, HUB_RELEASE_DAYS, HUB_ROWS, issueCountLabel, recentPromotions } from "./hubModel";
import { INK_LINK } from "./parts";

/** Where the hub links: the console's own pages, carrying the scope. */
export interface DashboardsHubHrefs {
  alerts: string;
  releases: string;
  /** The issues inbox (Observability › Errors). */
  errors: string;
  release: (version: string) => string;
  workload: (name: string, env: string | null) => string;
  /** Error logs of one service. */
  logs: (service: string) => string;
}

export interface DashboardsHubProps {
  project: string;
  /** The console scope: an environment name or "all". */
  env: string;
  environments: readonly ObsEnvironment[];
  /** The index the list already read (filtered by app, never by search). */
  dashboards: readonly DashboardSummary[];
  hrefs: DashboardsHubHrefs & { dashboard: (ref: DashboardRef) => string };
  /** Open the list narrowed to one group. */
  onOpenGroup: (group: DashboardGroup) => void;
  /** Open the whole list. */
  onOpenAll: () => void;
  /** Shown in place of the group cards when the app has no dashboard at all. */
  empty?: ReactNode;
}

const ISSUE_FILTERS = { range: "7d" as const, statuses: [], sort: "last" as const, text: "" };

export function DashboardsHub({ project, env, environments, dashboards, hrefs, onOpenGroup, onOpenAll, empty }: DashboardsHubProps) {
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const all = env === ENV_ALL;
  const scopedEnv = all ? null : (environments.find((candidate) => candidate.name === env) ?? null);
  const deployed = environments.length > 0;
  // A named environment Reliant has not recorded has no telemetry to ask for.
  const unrecorded = !all && !scopedEnv;
  const envIds = useMemo(() => (scopedEnv ? [scopedEnv.id] : []), [scopedEnv]);

  const projectScope = useMemo<ObsScope | null>(() => (orgId && deployed ? { orgId, projectId: project, envIds: [], filters: [] } : null), [orgId, project, deployed]);
  const scope = useMemo<ObsScope | null>(
    () => (orgId && deployed && !unrecorded ? { orgId, projectId: project, envIds, filters: [] } : null),
    [orgId, project, envIds, deployed, unrecorded]
  );
  const delivery = useMemo<DeliveryScope | null>(() => (scope ? { orgId: scope.orgId, projectId: project, envIds } : null), [scope, project, envIds]);
  const issueScope = useMemo<IssueScope | null>(() => (scope ? { orgId: scope.orgId, projectId: project, envIds } : null), [scope, project, envIds]);

  // The same reads, with the same keys, as the project Overview: one request feeds both pages.
  const listed = useWorkloads(projectScope, HUB_RANGE);
  const alerts = useAlerts(orgId);
  const releases = useReleases(delivery, `days:${HUB_RELEASE_DAYS}`, () => ({ startMs: Date.now() - HUB_RELEASE_DAYS * 86_400_000, endMs: Date.now() }));
  const issues = useIssues(issueScope, ISSUE_FILTERS);

  const envIdSet = useMemo(() => new Set(environments.map((candidate) => candidate.id)), [environments]);
  const projectAlerts = useMemo(() => (alerts.data ?? []).filter((alert) => envIdSet.has(alert.envId)), [alerts.data, envIdSet]);
  const envNameOf = (id: string) => environments.find((candidate) => candidate.id === id)?.name;
  const groups = useMemo(() => groupIndex(dashboards), [dashboards]);
  const starred = starredOf(dashboards);
  const recent = recentOf(dashboards);
  const jobsDashboard = groups.find((g) => g.group === "jobs")?.dashboards[0];

  return (
    <div className="space-y-6" data-testid="dashboards-hub">
      {!deployed ? (
        <Card className="text-sm text-ink-muted" data-testid="hub-health-undeployed">
          Service health, failing jobs and releases appear here after the first deploy of {project}.
        </Card>
      ) : unrecorded ? (
        <Card className="text-sm text-ink-muted" data-testid="hub-health-unrecorded">
          Reliant has recorded no deploy of {env}, so there is no health to show for it. Pick another environment, or all of them.
        </Card>
      ) : (
        <>
          <HealthStrip scope={scope} range={HUB_RANGE} environments={environments} envId={scopedEnv?.id ?? null} alertsHref={hrefs.alerts} />
          <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
            <div className="min-w-0 space-y-4">
              <ProcessHealth
                scope={scope}
                range={HUB_RANGE}
                workloads={listed.data?.workloads ?? []}
                alerts={projectAlerts}
                envNameOf={envNameOf}
                workloadHref={(name, processEnv) => hrefs.workload(name, processEnv ?? (all ? null : env))}
                logsHref={hrefs.logs}
              />
            </div>
            <div className="min-w-0 space-y-4">
              <RailCard title="Open issues" testId="hub-issues" href={hrefs.errors} linkLabel="Open the inbox">
                {issues.isLoading ? (
                  <Pending />
                ) : issues.error ? (
                  <Unavailable what="Issues" error={issues.error} />
                ) : (
                  <IssueFigure count={issues.data?.pages[0]?.issues.length ?? 0} more={!!issues.data?.pages[0]?.nextPageToken} />
                )}
              </RailCard>
              <RailCard
                title="Failing jobs"
                testId="hub-jobs"
                href={jobsDashboard ? hrefs.dashboard(jobsDashboard.ref) : undefined}
                linkLabel={jobsDashboard ? `Open ${jobsDashboard.title}` : undefined}
              >
                <FailingJobs listed={listed} env={env} all={all} workloadHref={hrefs.workload} />
              </RailCard>
              <RailCard title="Recent releases" testId="hub-releases" href={hrefs.releases} linkLabel="All releases">
                <RecentReleases query={releases} releaseHref={hrefs.release} showEnv={all} />
              </RailCard>
            </div>
          </div>
        </>
      )}

      {empty ?? (
        <>
          <div className={cn("grid grid-cols-1 gap-4", recent.length > 0 && "md:grid-cols-2")}>
            <ShelfCard title="Starred" testId="hub-starred" icon={<Star className="h-3.5 w-3.5" aria-hidden="true" />} dashboards={starred} href={hrefs.dashboard} none="Star a dashboard to pin it here and in the nav." />
            {recent.length > 0 && <ShelfCard title="Recently viewed" testId="hub-recent" dashboards={recent} href={hrefs.dashboard} none="" />}
          </div>
          <section aria-labelledby="hub-groups-heading" className="space-y-3">
            <h2 id="hub-groups-heading" className="text-sm font-medium text-ink">
              Dashboards by group
            </h2>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4" data-testid="hub-groups">
              {groups.map(({ group, dashboards: members }) => (
                <GroupCard key={group} group={group} members={members} href={hrefs.dashboard} onOpen={() => onOpenGroup(group)} />
              ))}
              <button
                type="button"
                onClick={onOpenAll}
                className="flex min-h-24 flex-col items-start justify-between gap-2 rounded-lg border border-dashed border-border px-4 py-3 text-left transition-colors hover:border-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                data-testid="hub-open-all"
              >
                <span className="text-sm font-medium text-ink">All dashboards</span>
                <span className="text-xs text-ink-muted">Search every group by name, workload or tag</span>
                <span className="flex items-center gap-1 text-xs text-ink-muted">
                  Browse {dashboards.length}
                  <ArrowRight className="h-3 w-3" aria-hidden="true" />
                </span>
              </button>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

// ── Pieces ──────────────────────────────────────────────────────────────────

function RailCard({ title, testId, href, linkLabel, children }: { title: string; testId: string; href?: string; linkLabel?: string; children: ReactNode }) {
  return (
    <Card padding="none" className="min-w-0" data-testid={testId}>
      <div className="flex items-center justify-between gap-3 px-4 pt-3">
        <h3 className="text-sm font-medium text-ink">{title}</h3>
        {href && linkLabel && (
          <Link href={href} className={cn(INK_LINK, "text-xs")}>
            {linkLabel}
          </Link>
        )}
      </div>
      <div className="px-4 pb-3 pt-2 text-sm">{children}</div>
    </Card>
  );
}

function Pending() {
  return <div className="h-5 w-32 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label="Loading" />;
}

/** A source that could not answer, said as such: never a zero. */
function Unavailable({ what, error }: { what: string; error: unknown }) {
  const kind = obsErrorKind(error);
  return (
    <p className="text-ink-muted" data-testid="hub-unavailable">
      {kind === "not-set-up"
        ? `${what} need observability set up for this app.`
        : kind === "denied"
          ? `You do not have access to ${what.toLowerCase()} here.`
          : `${what} are unavailable right now.`}
    </p>
  );
}

function IssueFigure({ count, more }: { count: number; more: boolean }) {
  return (
    <div className="flex items-baseline gap-2">
      <span className={cn("text-xl font-semibold tabular-nums", count > 0 ? "text-warning-ink" : "text-ink")} data-testid="hub-issues-count">
        {issueCountLabel(count, more)}
      </span>
      <span className="text-xs text-ink-muted">{count === 0 ? "nothing open in the last week" : "new, unresolved or regressed, last 7 days"}</span>
    </div>
  );
}

interface Query<T> {
  data: T | undefined;
  isLoading: boolean;
  error: unknown;
}

function FailingJobs({
  listed,
  env,
  all,
  workloadHref,
}: {
  listed: Query<{ jobs: WorkloadJob[]; jobsTruncated: boolean }>;
  env: string;
  all: boolean;
  workloadHref: (name: string, env: string | null) => string;
}) {
  if (listed.isLoading) return <Pending />;
  if (listed.error && !listed.data) return <Unavailable what="Job runs" error={listed.error} />;
  const failing = failingJobs(listed.data?.jobs ?? [], env);
  if (failing.length === 0) {
    return (
      <p className="text-ink-muted" data-testid="hub-jobs-none">
        {listed.data?.jobs.length ? "Every job's latest run succeeded." : "No job ran in the last day."}
      </p>
    );
  }
  return (
    <ul className="space-y-1.5" data-testid="hub-jobs-failing">
      {failing.slice(0, HUB_ROWS).map((job) => (
        <li key={`${job.envName}/${job.workload}`} className="flex min-w-0 items-center justify-between gap-2">
          <span className="flex min-w-0 items-center gap-2">
            <Status tone="danger" label="Failed" hideLabel />
            <Link href={workloadHref(job.workload, job.envName)} className={cn(INK_LINK, "truncate")}>
              {job.workload}
            </Link>
            {all && <Tag>{job.envName}</Tag>}
          </span>
          <span className="shrink-0 text-xs text-ink-muted" title={job.reason || undefined}>
            {job.finishedAtMs ? formatAgo(job.finishedAtMs) : job.reason || "failed"}
          </span>
        </li>
      ))}
      {failing.length > HUB_ROWS && <li className="text-xs text-ink-muted">and {failing.length - HUB_ROWS} more</li>}
    </ul>
  );
}

function RecentReleases({
  query,
  releaseHref,
  showEnv,
}: {
  query: Query<{ promotions: Promotion[] }>;
  releaseHref: (version: string) => string;
  showEnv: boolean;
}) {
  if (query.isLoading) return <Pending />;
  if (query.error) return <Unavailable what="Releases" error={query.error} />;
  const recent = recentPromotions(query.data?.promotions ?? []);
  if (recent.length === 0) return <p className="text-ink-muted">No release promoted in the last {HUB_RELEASE_DAYS} days.</p>;
  return (
    <ul className="space-y-1.5" data-testid="hub-releases-list">
      {recent.map((p) => (
        <li key={p.promotionId} className="flex min-w-0 items-center justify-between gap-2">
          <span className="flex min-w-0 items-center gap-2">
            <Link href={releaseHref(p.version)} className={cn(INK_LINK, "truncate")}>
              {p.version}
            </Link>
            {showEnv && <Tag>{p.envName}</Tag>}
            {p.kind === "rollback" && <Tag tone="warning">rollback</Tag>}
          </span>
          <span className="shrink-0 text-xs text-ink-muted" title={p.promotedBy || undefined}>
            {formatAgo(p.promotedAtMs)}
          </span>
        </li>
      ))}
    </ul>
  );
}

function GroupCard({ group, members, href, onOpen }: { group: DashboardGroup; members: DashboardSummary[]; href: (ref: DashboardRef) => string; onOpen: () => void }) {
  const count = members.length;
  return (
    <Card padding="none" className={cn("flex min-w-0 flex-col", count === 0 && "opacity-75")} data-testid={`hub-group-${group}`}>
      <div className="flex items-baseline justify-between gap-2 px-4 pt-3">
        <h3 className="text-sm font-medium text-ink">{GROUP_LABELS[group]}</h3>
        <span className="text-xs tabular-nums text-ink-muted" data-testid={`hub-group-${group}-count`}>
          {count}
        </span>
      </div>
      <p className="px-4 text-xs text-ink-muted">{GROUP_ABOUT[group]}</p>
      <ul className="flex-1 space-y-1 px-4 pb-2 pt-2 text-sm">
        {count === 0 ? (
          <li className="text-ink-muted">None yet</li>
        ) : (
          members.slice(0, 3).map((d) => (
            <li key={`${d.ref.kind}/${d.ref.key}`} className="truncate">
              {href(d.ref) ? (
                <Link href={href(d.ref)} className={INK_LINK}>
                  {d.title}
                </Link>
              ) : (
                d.title
              )}
            </li>
          ))
        )}
      </ul>
      {count > 0 && (
        <button
          type="button"
          onClick={onOpen}
          className="flex items-center gap-1 border-t border-border px-4 py-2 text-left text-xs text-ink-muted transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          data-testid={`hub-group-${group}-open`}
        >
          View all {count} in {GROUP_LABELS[group]}
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </button>
      )}
    </Card>
  );
}

function ShelfCard({ title, testId, icon, dashboards, href, none }: { title: string; testId: string; icon?: ReactNode; dashboards: DashboardSummary[]; href: (ref: DashboardRef) => string; none: string }) {
  return (
    <Card padding="none" className="min-w-0" data-testid={testId}>
      <div className="flex items-center gap-1.5 px-4 pt-3 text-ink">
        {icon}
        <h3 className="text-sm font-medium">{title}</h3>
      </div>
      <ul className="space-y-1 px-4 pb-3 pt-2 text-sm">
        {dashboards.length === 0 ? (
          <li className="text-xs text-ink-muted">{none}</li>
        ) : (
          dashboards.slice(0, HUB_ROWS).map((d) => (
            <li key={`${d.ref.kind}/${d.ref.key}`} className="flex min-w-0 items-center justify-between gap-2">
              {href(d.ref) ? (
                <Link href={href(d.ref)} className={cn(INK_LINK, "truncate")}>
                  {d.title}
                </Link>
              ) : (
                <span className="truncate">{d.title}</span>
              )}
              {d.lastViewedAtMs !== null && <span className="shrink-0 text-xs text-ink-muted">{formatAgo(d.lastViewedAtMs)}</span>}
            </li>
          ))
        )}
      </ul>
    </Card>
  );
}
