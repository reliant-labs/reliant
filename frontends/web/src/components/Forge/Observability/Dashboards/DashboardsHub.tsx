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
 *   rail              open issues, failing jobs and recent releases: one
 *                     card, a section each, so a short answer ("5 open")
 *                     costs a line rather than a card of its own
 *   shelf             starred and recently viewed dashboards, side by side
 *                     in one card
 *   built-in          the dashboards Reliant hosts for every app (source
 *                     `builtin`: costs, usage, and the Overview, Users,
 *                     Product, Acquisition and Cost views as they ship), one
 *                     row of up to five cards with an icon and a line each,
 *                     above the project's own. Shown only when there is one:
 *                     no placeholder for a view that does not exist yet.
 *   groups            the project's dashboards (forge's per-service ones and
 *                     what the repo and the workspace declare), by group
 *
 * Every group card has the same shape and height: its name and count (the
 * header opens the list narrowed to it), one line of what it holds, and room
 * for two dashboards and "and N more", so the grid reads as a grid rather than
 * a skyline.
 *
 * Composed in the UI from the RPCs the console already reads (ListDashboards,
 * GetServiceOverview, QueryPanel, ListWorkloads, ListAlerts, ListReleases,
 * ListIssues): there is no hub spec and no query path of its own. Each card
 * says plainly when its source cannot answer, rather than showing a zero.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight, Gauge, LayoutDashboard, Megaphone, ShoppingBag, Star, Users, Wallet, type LucideIcon } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useAlerts, useIssues, useObservabilityOrg, useReleases, useWorkloads } from "@/hooks/observability-queries";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { DeliveryScope, Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
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
  /** `environments` is empty because the list could not be read, not because nothing is deployed. */
  environmentsUnread?: boolean;
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

export function DashboardsHub({ project, env, environments, environmentsUnread = false, dashboards, hrefs, onOpenGroup, onOpenAll, empty }: DashboardsHubProps) {
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
  // Built-ins have their own row above the groups, so the group cards are the project's own dashboards.
  const builtins = useMemo(() => dashboards.filter((d) => d.source === "builtin"), [dashboards]);
  const groups = useMemo(() => groupIndex(dashboards.filter((d) => d.source !== "builtin")), [dashboards]);
  const starred = starredOf(dashboards);
  const recent = recentOf(dashboards);
  const jobsDashboard = groups.find((g) => g.group === "jobs")?.dashboards[0];

  return (
    <div className="space-y-6" data-testid="dashboards-hub">
      {!deployed && environmentsUnread ? (
        <Card className="text-sm text-ink-muted" data-testid="hub-health-unread">
          Service health, failing jobs and releases appear here once {project}&apos;s environments load.
        </Card>
      ) : !deployed ? (
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
            <Card padding="none" className="min-w-0 divide-y divide-border" data-testid="hub-rail">
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
            </Card>
          </div>
        </>
      )}

      {empty ?? (
        <>
          <Card
            padding="none"
            className={cn("grid min-w-0 grid-cols-1 divide-y divide-border", recent.length > 0 && "md:grid-cols-2 md:divide-x md:divide-y-0")}
            data-testid="hub-shelf"
          >
            <ShelfCard title="Starred" testId="hub-starred" icon={<Star className="h-3.5 w-3.5" aria-hidden="true" />} dashboards={starred} href={hrefs.dashboard} none="Star a dashboard to pin it here and in the nav." />
            {recent.length > 0 && <ShelfCard title="Recently viewed" testId="hub-recent" dashboards={recent} href={hrefs.dashboard} none="" />}
          </Card>
          {builtins.length > 0 && <BuiltinRow dashboards={builtins} href={hrefs.dashboard} />}
          <section aria-labelledby="hub-groups-heading" className="space-y-3">
            <h2 id="hub-groups-heading" className="text-sm font-medium text-ink">
              {builtins.length > 0 ? "Your dashboards" : "Dashboards by group"}
              {builtins.length > 0 && <span className="ml-2 text-xs font-normal text-ink-muted">Generated per service, and declared in your repo or workspace</span>}
            </h2>
            <div className="grid auto-rows-fr grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4" data-testid="hub-groups">
              {groups.map(({ group, dashboards: members }) => (
                <GroupCard key={group} group={group} members={members} href={hrefs.dashboard} onOpen={() => onOpenGroup(group)} />
              ))}
              <button
                type="button"
                onClick={onOpenAll}
                className="flex min-w-0 flex-col items-start gap-0.5 rounded-lg border border-dashed border-border-strong px-4 py-3 text-left transition-colors hover:border-ink-muted hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                data-testid="hub-open-all"
              >
                <span className="flex w-full items-center justify-between gap-2 text-sm font-medium text-ink">
                  All dashboards
                  <span className="flex items-center gap-1 text-xs font-normal tabular-nums text-ink-muted">
                    {dashboards.length}
                    <ArrowRight className="h-3 w-3" aria-hidden="true" />
                  </span>
                </span>
                <span className="text-xs text-ink-muted">Search every group by name, workload or tag.</span>
              </button>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

// ── Pieces ──────────────────────────────────────────────────────────────────

/** One section of the rail card: a title, its way out, and a line or a short list. */
function RailCard({ title, testId, href, linkLabel, children }: { title: string; testId: string; href?: string; linkLabel?: string; children: ReactNode }) {
  return (
    <section className="min-w-0 px-4 py-3" data-testid={testId}>
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-medium text-ink">{title}</h3>
        {href && linkLabel && (
          <Link href={href} className={cn(INK_LINK, "text-xs")}>
            {linkLabel}
          </Link>
        )}
      </div>
      <div className="pt-1.5 text-sm">{children}</div>
    </section>
  );
}

function Pending() {
  return <div className="h-5 w-32 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label="Loading" />;
}

/** The last result, kept while the query quota refills: said so, never passed off as current. */
function PausedNote() {
  return (
    <span className="block text-xs text-ink-muted" data-testid="hub-jobs-paused">
      Paused: the last result, until the query quota refills.
    </span>
  );
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

export function FailingJobs({
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
  // A failed refetch keeps React Query's last data. Only a quota refusal may go
  // on showing it (marked paused): a denial after access was revoked, or any
  // other failure, must not leave stale job names and reasons on a health view.
  if (listed.error && !keepsLastResult(listed.error, listed.data)) return <Unavailable what="Job runs" error={listed.error} />;
  const paused = !!listed.error;
  const jobs = listed.data?.jobs ?? [];
  const failing = failingJobs(jobs, env);
  if (failing.length === 0) {
    // Only failures are claimed: a running or unknown latest run is not a
    // success, and a truncated list (ListWorkloads caps it, across every env)
    // may not hold this environment's runs at all.
    const ranHere = all ? jobs.length > 0 : jobs.some((job) => job.envName === env);
    const none = !ranHere
      ? listed.data?.jobsTruncated
        ? "No run of this environment's jobs among the most recent listed."
        : "No job ran in the last day."
      : listed.data?.jobsTruncated
        ? "No failed latest run among the most recent jobs listed."
        : "No job's latest run failed.";
    return (
      <p className="text-ink-muted" data-testid="hub-jobs-none">
        {none}
        {paused && <PausedNote />}
      </p>
    );
  }
  return (
    <ul className="space-y-1.5" data-testid="hub-jobs-failing">
      {paused && (
        <li>
          <PausedNote />
        </li>
      )}
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

/** The icon a built-in dashboard is drawn with, from what its key or view says it is about. */
function builtinIcon(d: DashboardSummary): LucideIcon {
  const about = `${d.ref.key} ${d.viewKind ?? ""}`.toLowerCase();
  if (/cost|billing|spend/.test(about)) return Wallet;
  if (/user|identity|auth/.test(about)) return Users;
  if (/gtm|acquisition|growth|marketing/.test(about)) return Megaphone;
  if (/product|funnel/.test(about)) return ShoppingBag;
  if (/llm|model|usage/.test(about)) return Gauge;
  return LayoutDashboard;
}

/** Built-in dashboards: one row of same-sized cards, an icon, a title and one line of what it shows. */
function BuiltinRow({ dashboards, href }: { dashboards: DashboardSummary[]; href: (ref: DashboardRef) => string }) {
  return (
    <section aria-labelledby="hub-builtin-heading" className="space-y-3" data-testid="hub-builtin">
      <h2 id="hub-builtin-heading" className="text-sm font-medium text-ink">
        Built-in
        <span className="ml-2 text-xs font-normal text-ink-muted">Hosted by Reliant for every app</span>
      </h2>
      <ul className="grid auto-rows-fr grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
        {dashboards.map((d) => {
          const Icon = builtinIcon(d);
          const target = href(d.ref);
          const body = (
            <>
              <span className="flex min-w-0 items-center gap-2">
                <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border text-ink-muted">
                  <Icon className="h-3.5 w-3.5" aria-hidden="true" />
                </span>
                <span className="truncate text-sm font-medium text-ink">{d.title}</span>
              </span>
              {d.description && <span className="mt-1 line-clamp-2 text-xs text-ink-muted">{d.description}</span>}
            </>
          );
          return (
            <li key={`${d.ref.kind}/${d.ref.key}`} className="min-w-0" data-testid={`hub-builtin-${d.ref.key}`}>
              {target ? (
                <Link
                  href={target}
                  className="no-color flex h-full min-w-0 flex-col rounded-lg border border-border bg-surface px-3 py-2.5 transition-colors hover:border-ink-muted hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {body}
                </Link>
              ) : (
                <div className="flex h-full min-w-0 flex-col rounded-lg border border-border bg-surface px-3 py-2.5">{body}</div>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/** Dashboards a group card lists before "N more". */
const GROUP_SHOWN = 2;

function GroupCard({ group, members, href, onOpen }: { group: DashboardGroup; members: DashboardSummary[]; href: (ref: DashboardRef) => string; onOpen: () => void }) {
  const count = members.length;
  const title = (
    <>
      <span className="truncate text-sm font-medium text-ink">{GROUP_LABELS[group]}</span>
      <span className="flex shrink-0 items-center gap-1 text-xs tabular-nums text-ink-muted" data-testid={`hub-group-${group}-count`}>
        {count}
        {count > 0 && <ArrowRight className="h-3 w-3" aria-hidden="true" />}
      </span>
    </>
  );
  return (
    <Card padding="none" className={cn("flex min-w-0 flex-col px-4 py-3", count === 0 && "opacity-75")} data-testid={`hub-group-${group}`}>
      <h3 className="min-w-0">
        {count > 0 ? (
          // The header opens the list narrowed to this group: the card's one way to "everything in it".
          <button
            type="button"
            onClick={onOpen}
            title={`View all ${count} in ${GROUP_LABELS[group]}`}
            className="-mx-1 flex w-[calc(100%+0.5rem)] min-w-0 items-center justify-between gap-2 rounded px-1 text-left hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid={`hub-group-${group}-open`}
          >
            {title}
          </button>
        ) : (
          <span className="flex min-w-0 items-center justify-between gap-2">{title}</span>
        )}
      </h3>
      <p className="truncate text-xs text-ink-muted" title={GROUP_ABOUT[group]}>
        {GROUP_ABOUT[group]}
      </p>
      {/* Room for three lines (two dashboards and "and N more") whatever the group holds, so every card is one height. */}
      <ul className="mt-2 min-h-[4rem] space-y-1 text-sm">
        {count === 0 ? (
          <li className="text-ink-muted">None yet</li>
        ) : (
          members.slice(0, GROUP_SHOWN).map((d) => (
            <li key={`${d.ref.kind}/${d.ref.key}`} className="truncate leading-5">
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
        {count > GROUP_SHOWN && (
          <li className="text-xs leading-5 text-ink-muted">and {count - GROUP_SHOWN} more</li>
        )}
      </ul>
    </Card>
  );
}

/** One column of the shelf card: a title and up to HUB_ROWS dashboards, one line each. */
function ShelfCard({ title, testId, icon, dashboards, href, none }: { title: string; testId: string; icon?: ReactNode; dashboards: DashboardSummary[]; href: (ref: DashboardRef) => string; none: string }) {
  return (
    <section className="min-w-0 px-4 py-3" data-testid={testId}>
      <div className="flex items-center gap-1.5 text-ink">
        {icon}
        <h3 className="text-sm font-medium">{title}</h3>
      </div>
      <ul className="space-y-1 pt-1.5 text-sm">
        {dashboards.length === 0 ? (
          <li className="text-xs text-ink-muted">{none}</li>
        ) : (
          dashboards.slice(0, HUB_ROWS).map((d) => (
            <li key={`${d.ref.kind}/${d.ref.key}`} className="flex min-w-0 items-center justify-between gap-2 leading-5">
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
    </section>
  );
}
