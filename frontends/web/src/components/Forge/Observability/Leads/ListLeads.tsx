// Copyright (c) 2026 Reliant Labs

/**
 * Errors, Traces and Sessions as led pages (Console/ledConsole.tsx): the
 * same data as the console's lists, but each page leads with ONE sentence
 * (leadStory.ts), shows the item worth opening first as a card (the newest
 * issue, the slowest request beside its timeline, the replay worth watching),
 * and lists the rest as compact one-line rows with "Show all". The console's
 * full list, with every filter, is one link away.
 *
 * Each reads the scope its host hands it (the console's project and
 * environment) and links through `hrefOf`, so the same page serves the
 * product and the public demo.
 */

import { useMemo, type ReactNode } from "react";
import { CircleAlert, Play } from "lucide-react";

import { useIssues, useListTraces, useTrace, type IssueFilters, type TraceFilters } from "@/hooks/observability-queries";
import { useSessions, type SessionFilters } from "@/hooks/observability-session-queries";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope, TraceSummary } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { HeadlineBars } from "../charts/HeadlineBars";
import { affectedNoun } from "../Errors/issueModel";
import { formatAgo, formatCount, formatDuration } from "../format";
import { CardOpen, ErrorBadge, FullListLink, LeadCard, ListHeading, ListLeadHeader, RedDot, RowName, SummaryLine, TopRows } from "./leadParts";
import { MiniWaterfall } from "./MiniWaterfall";
import { errorsSummary, leadIssue, plural, sessionLength, sessionsByInterest, sessionsSummary, SLOW_MS, traceStory, tracesSummary } from "./leadStory";

/** A console list view's href with `search` (an open item, a filter). */
export type LeadHref = (view: "errors" | "traces" | "sessions", search?: Record<string, unknown>) => string;

export interface ListLeadProps {
  scope: ObsScope;
  /** The environment in scope by name, or null for every environment. */
  env: string | null;
  hrefOf: LeadHref;
  /** The plain line beside the title. */
  subtitle?: string;
  /** What the host adds after the lead (an agent card on the demo). */
  aside?: ReactNode;
}

// ── Errors ──────────────────────────────────────────────────────────────────

const OPEN: IssueFilters = { range: "7d", statuses: [], sort: "last", text: "" };
const RESOLVED: IssueFilters = { range: "7d", statuses: ["resolved"], sort: "last", text: "" };

export function ErrorsLead({ scope, env, hrefOf, subtitle, aside, explainIssue }: ListLeadProps & { explainIssue?: (issue: Issue) => string | undefined }) {
  const issueScope = useMemo(() => ({ orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds }), [scope.orgId, scope.projectId, scope.envIds]);
  const openQuery = useIssues(issueScope, OPEN);
  const resolvedQuery = useIssues(issueScope, RESOLVED);
  const open = useMemo(() => [...(openQuery.data?.pages.flatMap((page) => page.issues) ?? [])].sort((a, b) => b.lastSeenMs - a.lastSeenMs), [openQuery.data]);
  const resolved = useMemo(() => resolvedQuery.data?.pages.flatMap((page) => page.issues) ?? [], [resolvedQuery.data]);
  const featured = leadIssue(open);
  const rest = open.filter((issue) => issue !== featured);
  const issueHref = (issue: Issue) => hrefOf("errors", { issue: issue.id });
  const summary = openQuery.isSuccess ? errorsSummary({ open, resolved, env, more: !!openQuery.hasNextPage }) : null;
  const explained = featured ? explainIssue?.(featured) : undefined;
  return (
    <div className="space-y-10" data-testid="lead-errors">
      <ListLeadHeader title="Errors" subtitle={subtitle} action={<FullListLink href={hrefOf("errors", { isort: "last" })}>All filters</FullListLink>}>
        <SummaryLine tone={summary?.tone}>{openQuery.isError ? "The error inbox could not be read. The full list says why." : (summary?.text ?? "Reading the error inbox…")}</SummaryLine>
      </ListLeadHeader>

      {openQuery.isSuccess && <ErrorsHeadline open={open} more={!!openQuery.hasNextPage} resolved={resolved.length} />}

      {featured && (
        <LeadCard label="Newest issue" testId="lead-featured-issue" attrs={{ "data-issue-id": featured.id }}>
          <p className="flex flex-wrap items-center gap-2 text-meta text-ink-muted">
            <CircleAlert className="h-3.5 w-3.5 text-danger-ink" aria-hidden="true" />
            Newest issue
            {featured.status === "new" && (
              <span className="inline-flex items-center gap-1.5 font-medium text-danger-ink">
                <span aria-hidden="true">·</span> New
              </span>
            )}
          </p>
          <h2 className="mt-2 text-section text-ink [overflow-wrap:anywhere]">{featured.title}</h2>
          {explained && <p className="mt-1 max-w-4xl text-ui text-ink-muted">{explained}</p>}
          <div className="mt-3 flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
            <p className="min-w-0 text-meta text-ink-muted [overflow-wrap:anywhere]">
              <span className="font-medium tabular-nums text-ink">{plural(featured.events, "event")}</span> · {formatCount(featured.affected)} {affectedNoun(featured.platform, featured.affected)} affected · {featured.service}
              {featured.firstSeenMs ? (
                <>
                  {" · first seen "}
                  {/* The release it first appeared in is a hash: on hover, not in the line. */}
                  <span title={featured.firstVersion ? `First seen in ${featured.firstVersion}` : undefined}>{formatAgo(featured.firstSeenMs)}</span>
                </>
              ) : (
                ""
              )}
              {featured.assignee ? ` · ${featured.assignee.name} is on it` : ""}
            </p>
            <CardOpen href={issueHref(featured)} label="Open issue" testId="lead-featured-issue-open" />
          </div>
        </LeadCard>
      )}

      {rest.length > 0 && (
        <section className="space-y-3" aria-labelledby="lead-errors-open">
          <ListHeading id="lead-errors-open" count={rest.length}>
            Other open issues
          </ListHeading>
          <TopRows
            label="Open issues"
            columns={["Issue", "Events", "Affected", "Last seen"]}
            rows={rest}
            rowKey={(issue) => issue.id}
            rowHref={issueHref}
            rowAttrs={(issue) => ({ "data-testid": "lead-issue-row", "data-issue-id": issue.id })}
            noun="issues"
            cells={(issue) => [
              <RowName marker={issue.status === "new" ? <RedDot label="New" /> : undefined} name={issue.title} note={env ? issue.service : `${issue.service} · ${issue.envName}`} />,
              formatCount(issue.events),
              formatCount(issue.affected),
              formatAgo(issue.lastSeenMs),
            ]}
          />
        </section>
      )}

      {aside}

      {resolved.length > 0 && (
        <section className="space-y-3" aria-labelledby="lead-errors-resolved">
          <ListHeading id="lead-errors-resolved" count={resolved.length}>
            Resolved recently
          </ListHeading>
          <TopRows
            label="Resolved issues"
            columns={["Issue", "Events", "Fixed in", "Resolved"]}
            rows={resolved}
            rowKey={(issue) => issue.id}
            rowHref={issueHref}
            noun="resolved issues"
            top={5}
            cells={(issue) => [
              <RowName strong={false} name={issue.title} note={env ? issue.service : `${issue.service} · ${issue.envName}`} />,
              formatCount(issue.events),
              <span className="block truncate" title={issue.resolvedVersion}>{issue.resolvedVersion || "—"}</span>,
              issue.resolvedAtMs ? formatAgo(issue.resolvedAtMs) : "—",
            ]}
          />
        </section>
      )}
    </div>
  );
}

/**
 * The Errors page's headline: how many issues are open, over the inbox's
 * window, and one chart of their events by hour (each issue's own hourly
 * trend, summed). Events, people affected and resolved issues are a quiet
 * row beside it. There is no previous-window read here, so it names the
 * window and makes no comparison. The inbox pages, so while more pages remain
 * every figure is of the issues loaded: the count, events and affected say
 * "N+", and the chart says whose events it sums.
 */
function ErrorsHeadline({ open, more, resolved }: { open: readonly Issue[]; more: boolean; resolved: number }) {
  const { timestamps, values } = useMemo(() => {
    const byHour = new Map<number, number>();
    for (const issue of open) for (const point of issue.trend) if (point.hourMs) byHour.set(point.hourMs, (byHour.get(point.hourMs) ?? 0) + point.events);
    const hours = [...byHour.keys()].sort((a, b) => a - b);
    return { timestamps: hours.map((ms) => ms / 1000), values: hours.map((ms) => byHour.get(ms) ?? 0) };
  }, [open]);
  const events = open.reduce((sum, issue) => sum + issue.events, 0);
  const affected = open.reduce((sum, issue) => sum + issue.affected, 0);
  return (
    <section className="min-w-0 space-y-5" aria-label="Open issues, last 7 days" data-testid="lead-errors-headline">
      <div className="flex flex-wrap items-end justify-between gap-x-12 gap-y-6">
        <div className="min-w-0">
          <div className="flex items-baseline gap-2 text-ui">
            <span className="font-medium text-ink">Open issues</span>
            <span className="text-meta text-ink-muted">Last 7 days</span>
          </div>
          <div className="mt-1 text-figure tabular-nums text-ink">
            {formatCount(open.length)}
            {more ? "+" : ""}
          </div>
        </div>
        <dl className="flex flex-wrap gap-x-10 gap-y-5">
          {(
            [
              // Of the loaded issues only, so "+" while more remain, as the count.
              ["Events", `${formatCount(events)}${more ? "+" : ""}`],
              ["Affected", `${formatCount(affected)}${more ? "+" : ""}`],
              ["Resolved", formatCount(resolved)],
            ] as const
          ).map(([label, value]) => (
            <div key={label} className="min-w-0">
              <dt className="text-meta text-ink-muted">{label}</dt>
              <dd className="mt-0.5 text-2xl font-semibold tabular-nums text-ink">{value}</dd>
            </div>
          ))}
        </dl>
      </div>
      {timestamps.length > 1 ? (
        <>
          <HeadlineBars values={values} timestamps={timestamps} label={more ? `Error events by hour, of the ${formatCount(open.length)} issues loaded` : "Error events by hour"} testId="lead-errors-chart" />
          {more && (
            <p className="text-meta text-ink-muted" data-testid="lead-errors-partial">
              Events of the {formatCount(open.length)} issues loaded. More issues are open; the full list has them all.
            </p>
          )}
        </>
      ) : (
        <div className="flex h-28 flex-col justify-end" aria-hidden="true">
          <div className="border-b border-border" />
        </div>
      )}
    </section>
  );
}

// ── Traces ──────────────────────────────────────────────────────────────────

const TRACES: TraceFilters = { range: "1h", rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0 };

export function TracesLead({ scope, hrefOf, subtitle, aside }: ListLeadProps) {
  const traces = useListTraces(scope, TRACES);
  const all = useMemo(() => traces.data?.pages.flatMap((page) => page.traces) ?? [], [traces.data]);
  const slowest = useMemo(() => [...all].sort((a, b) => b.durationMs - a.durationMs), [all]);
  const top = slowest[0];
  const traceHref = (trace: TraceSummary) => hrefOf("traces", { trace: trace.traceId });
  const summary = traces.isSuccess ? tracesSummary({ traces: all, window: "last hour", more: !!traces.hasNextPage }) : null;
  return (
    <div className="space-y-10" data-testid="lead-traces">
      <ListLeadHeader title="Traces" subtitle={subtitle} action={<FullListLink href={hrefOf("traces", { range: "1h" })}>Chart and filters</FullListLink>}>
        <SummaryLine tone={summary?.tone}>{traces.isError ? "Traces could not be read. The full list says why." : (summary?.text ?? "Reading the last hour of requests…")}</SummaryLine>
      </ListLeadHeader>

      {top && <SlowestTrace scope={scope} trace={top} href={traceHref(top)} />}

      {slowest.length > 1 && (
        <section className="space-y-3" aria-labelledby="lead-traces-slowest">
          <ListHeading id="lead-traces-slowest" count={slowest.length}>
            Slowest requests, last hour
          </ListHeading>
          <TopRows
            label="Slowest requests"
            columns={["Request", "Duration", "Steps", "When"]}
            rows={slowest}
            rowKey={(trace) => trace.traceId}
            rowHref={traceHref}
            rowAttrs={(trace) => ({ "data-testid": "lead-trace-row", "data-trace-id": trace.traceId })}
            noun="requests"
            cells={(trace) => [
              <RowName marker={trace.errorCount > 0 ? <RedDot label="Failed" /> : undefined} name={trace.rootOperation || trace.traceId} note={trace.rootService} />,
              <span className={cn(trace.durationMs >= SLOW_MS && "font-semibold text-warning-ink")}>{formatDuration(trace.durationMs)}</span>,
              formatCount(trace.spanCount),
              formatAgo(trace.startMs),
            ]}
          />
        </section>
      )}

      {aside}
    </div>
  );
}

/** The slowest request, as a card: what happened on the left, its timeline in miniature on the right. */
function SlowestTrace({ scope, trace, href }: { scope: ObsScope; trace: TraceSummary; href: string }) {
  const detail = useTrace(scope, trace.traceId, "1h");
  const spans = detail.data?.spans ?? [];
  const story = spans.length ? traceStory(spans) : null;
  return (
    <LeadCard label="Slowest request" testId="lead-slowest-trace" attrs={{ "data-trace-id": trace.traceId }}>
      <div className="grid gap-x-6 gap-y-3 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-meta text-ink-muted">Slowest request</p>
          <div className="flex flex-wrap items-baseline justify-between gap-x-3">
            <h2 className="min-w-0 text-section text-ink [overflow-wrap:anywhere]">{trace.rootOperation || trace.traceId}</h2>
            <p className="text-2xl font-semibold tabular-nums text-ink">{formatDuration(trace.durationMs)}</p>
          </div>
          {story && <p className="text-ui text-ink-muted [overflow-wrap:anywhere]">{story}</p>}
          <div className="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-1 pt-1">
            <p className="min-w-0 truncate text-meta text-ink-muted">
              {trace.rootService} · {plural(trace.spanCount, "step")} · {formatAgo(trace.startMs)}
            </p>
            <CardOpen href={href} label="Open the full timeline" testId="lead-slowest-trace-open" />
          </div>
        </div>
        {spans.length > 1 && <MiniWaterfall spans={spans} />}
      </div>
    </LeadCard>
  );
}

// ── Sessions ────────────────────────────────────────────────────────────────

const SESSIONS: SessionFilters = { range: "24h", hasError: false, userId: "", pathContains: "" };

/** Sessions list more rows than the others: a visit is one short line, and the page has nothing else under it. */
const SESSION_ROWS = 15;

export function SessionsLead({ scope, env, hrefOf, subtitle, aside }: ListLeadProps) {
  const sessions = useSessions(scope, SESSIONS);
  const rows = useMemo(() => sessionsByInterest(sessions.data?.sessions ?? []), [sessions.data]);
  const [featured, ...rest] = rows;
  const sessionHref = (session: SessionSummary) => hrefOf("sessions", { session: session.sessionId });
  const summary = sessions.isSuccess ? sessionsSummary({ sessions: rows, window: "last 24 hours", more: !!sessions.data?.nextPageToken }) : null;
  return (
    <div className="space-y-10" data-testid="lead-sessions">
      <ListLeadHeader title="Sessions" subtitle={subtitle} action={<FullListLink href={hrefOf("sessions", { range: "24h" })}>All filters</FullListLink>}>
        <SummaryLine tone={summary?.tone}>{sessions.isError ? "Sessions could not be read. The full list says why." : (summary?.text ?? "Reading the last day's visits…")}</SummaryLine>
      </ListLeadHeader>

      {featured && <FeaturedReplay session={featured} href={sessionHref(featured)} />}

      {rest.length > 0 && (
        <section className="space-y-3" aria-labelledby="lead-sessions-list">
          <ListHeading id="lead-sessions-list" count={rest.length}>
            Other visits
          </ListHeading>
          <TopRows
            label="Recorded visits"
            columns={["Visit", "Pages", "Length", "When"]}
            rows={rest}
            rowKey={(session) => session.sessionId}
            rowHref={sessionHref}
            noun="visits"
            top={SESSION_ROWS}
            cells={(session) => [
              <>
                <MiniReplay replay={session.hasReplay} />
                <span className="min-w-0 truncate font-medium text-ink" title={session.entryPath || "/"} data-testid={`lead-session-${session.sessionId}`}>
                  {session.entryPath || "/"}
                </span>
                {session.errors > 0 && <ErrorBadge errors={session.errors} />}
                <span className="min-w-0 shrink truncate text-meta text-ink-muted max-sm:hidden">
                  {session.userId || "Signed-out visitor"}
                  {env ? "" : ` · ${session.envName}`}
                </span>
              </>,
              formatCount(session.pageViews),
              formatDuration(sessionLength(session)),
              formatAgo(session.startedMs),
            ]}
          />
        </section>
      )}

      {aside}
    </div>
  );
}

/** The replay worth watching: its first frame with a play button beside what the visitor did. */
function FeaturedReplay({ session, href }: { session: SessionSummary; href: string }) {
  return (
    <LeadCard label="Replay worth watching" testId="lead-featured-session" className="p-3">
      <div className="grid gap-4 sm:grid-cols-[18rem_minmax(0,1fr)]">
        <ReplayThumb path={session.entryPath} errors={session.errors} replay={session.hasReplay} />
        <div className="flex min-w-0 flex-col gap-1 py-1">
          <p className="text-meta text-ink-muted">{session.errors ? "A visit that hit an error" : "The newest visit"}</p>
          <h2 className="truncate text-section text-ink" title={session.entryPath || "/"}>
            {session.entryPath || "/"}
          </h2>
          <p className="text-ui text-ink-muted [overflow-wrap:anywhere]">
            {session.userId || "A signed-out visitor"} looked at {plural(session.pageViews, "page")} over {formatDuration(sessionLength(session))}
            {session.errors ? ` and hit ${plural(session.errors, "error")}.` : "."}
            {session.hasReplay ? (session.errors ? " Watch where it went wrong." : "") : " No replay was recorded."}
          </p>
          <div className="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-1 pt-1">
            {/* The build it ran is a hash: on hover, not in the line. */}
            <p className="min-w-0 truncate text-meta text-ink-muted" title={session.release || session.serviceVersion ? `Ran ${session.release || session.serviceVersion}` : undefined}>
              {formatAgo(session.startedMs)}
              {session.envName ? ` · ${session.envName}` : ""}
            </p>
            <CardOpen href={href} label={session.hasReplay ? "Watch the replay" : "Open the visit"} testId="lead-featured-session-open" />
          </div>
        </div>
      </div>
    </LeadCard>
  );
}

/** A replay's thumbnail: a little browser on its first page, a play button, and its errors. */
function ReplayThumb({ path, errors, replay }: { path: string; errors: number; replay: boolean }) {
  return (
    <div className="relative overflow-hidden rounded-lg border border-border bg-surface-sunken" aria-hidden="true">
      <div className="flex items-center gap-1 border-b border-border bg-surface px-2 py-1.5">
        <span className="h-1.5 w-1.5 rounded-full bg-ink/15" />
        <span className="h-1.5 w-1.5 rounded-full bg-ink/15" />
        <span className="h-1.5 w-1.5 rounded-full bg-ink/15" />
        <span className="ml-1.5 truncate rounded bg-surface-sunken px-1.5 font-mono text-meta leading-4 text-ink-muted">{path || "/"}</span>
      </div>
      <div className="space-y-1.5 px-3 py-3">
        <div className="h-2 w-2/3 rounded bg-ink/10" />
        <div className="h-1.5 w-5/6 rounded bg-ink/[0.07]" />
        <div className="grid grid-cols-3 gap-1.5 pt-1">
          <div className="h-7 rounded bg-ink/[0.06]" />
          <div className="h-7 rounded bg-ink/[0.06]" />
          <div className="h-7 rounded bg-ink/[0.06]" />
        </div>
      </div>
      {replay && (
        <span className="absolute inset-0 top-6 flex items-center justify-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-md transition-transform group-hover:scale-110">
            <Play className="ml-0.5 h-4 w-4" fill="currentColor" />
          </span>
        </span>
      )}
      {errors > 0 && (
        <span className="absolute right-2 top-8">
          <ErrorBadge errors={errors} />
        </span>
      )}
    </div>
  );
}

/** A row's tiny replay frame with its play glyph (no glyph: no replay was recorded). */
function MiniReplay({ replay }: { replay: boolean }) {
  return (
    <span className="relative flex h-6 w-9 shrink-0 items-center justify-center rounded border border-border bg-surface-sunken" aria-hidden="true">
      <span className="absolute inset-x-0 top-0 h-1.5 border-b border-border bg-surface" />
      {replay && <Play className="mt-1 h-2.5 w-2.5 text-primary-ink" fill="currentColor" />}
    </span>
  );
}
