// Copyright (c) 2025 Reliant Labs

/**
 * One issue: what it is (title, culprit, where), how much (events and affected
 * over the window, per version), since when (first release), the latest
 * occurrence with its stack and a way into its trace, the issues that tend to
 * fire with it, its recent events, its triage, and its history.
 */

import { useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ExternalLink, GitCommitHorizontal, MonitorPlay } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { useIssue, useIssueEvents, useIssueTimeline } from "@/hooks/observability-queries";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { issuesErrorKind } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import type { Issue, IssueDetail as IssueDetailModel, IssueEvent, IssueFrame, IssueVersionBreakdown } from "@/services/controlPlane/observability/issuesTypes";
import type { RumScope } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { ActivityFeed } from "../../Activity/ActivityFeed";
import { AskReliantActions } from "../AskReliant/AskReliant";
import { AttributeTable } from "../AttributeTable";
import { shortCommit } from "../deliveryScope";
import { formatAgo, formatCount, formatTimestamp, formatWhen } from "../format";
import type { ObsNavigate } from "../obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows } from "../ObsStates";
import { SessionName } from "../Sessions/SessionName";
import { rumScopeOf, sessionLink } from "../Sessions/sessionModel";
import { StatusBadge, Trend } from "./IssueBits";
import { affectedNoun, frameText, issueAskSubject, PLATFORM_LABEL } from "./issueModel";
import { IssueNotFound, IssuesNotConfigured } from "./IssueStates";
import { IssueAssignee } from "./IssueAssignee";
import { IssueTimeline, useOpenChat } from "./IssueTimeline";
import { chatLabel, latestChatLink, mergeTimelinePages } from "./issueTimelineModel";
import { IssueTriage } from "./IssueTriage";
import { linkChatToIssue } from "./linkIssueChat";

export function IssueDetail({
  issueId,
  scope,
  subject,
  range,
  navigate,
  onBack,
}: {
  issueId: string;
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  navigate: ObsNavigate;
  onBack: () => void;
}) {
  const detail = useIssue(issueId, range);
  const back = (
    <Button variant="ghost" size="sm" leftIcon={<ArrowLeft className="h-3.5 w-3.5" />} onClick={onBack} data-testid="obs-issue-back">
      Back to errors
    </Button>
  );
  if (detail.isLoading) {
    return (
      <div className="space-y-3" data-testid="obs-issue" data-state="loading">
        {back}
        <ObsLoadingRows rows={5} testId="obs-issue-loading" />
      </div>
    );
  }
  if ((detail.error && !keepsLastResult(detail.error, detail.data)) || !detail.data) {
    const kind = issuesErrorKind(detail.error);
    return (
      <div className="space-y-3" data-testid="obs-issue" data-state="error">
        {back}
        {kind === "not-found" ? (
          <IssueNotFound onBack={onBack} />
        ) : kind === "not-configured" ? (
          <IssuesNotConfigured />
        ) : (
          <ObsError error={detail.error} subject={subject} onRetry={() => void detail.refetch()} retrying={detail.isFetching} />
        )}
      </div>
    );
  }
  const openTrace = (traceId: string, spanId?: string) =>
    navigate({ view: "traces", trace: traceId, span: spanId || undefined, issue: undefined, istatus: undefined, platform: undefined, isort: undefined, assignee: undefined, q: undefined });
  // A browser error's session, opened a few seconds before the error so the replay shows what led to it.
  const openSession: SessionLinks = {
    scope: rumScopeOf(scope),
    open: (sessionId, atMs) => navigate(sessionLink(sessionId, atMs > 0 ? atMs - SESSION_LEAD_MS : undefined)),
  };
  return (
    <><ObsAsOf updatedAt={detail.dataUpdatedAt} /><IssueBody
      detail={detail.data}
      scope={scope}
      subject={subject}
      range={range}
      back={back}
      openTrace={openTrace}
      openSession={openSession}
      openIssue={(id) => navigate({ issue: id })}
    /></>
  );
}

/** How far before an error its session opens. */
const SESSION_LEAD_MS = 5_000;

/** How an event links to its browser session: where the session is read from (for its name), and how it opens. */
interface SessionLinks {
  scope: RumScope;
  open: (sessionId: string, atMs: number) => void;
}

function SessionLink({ event, onSession }: { event: IssueEvent; onSession: SessionLinks }) {
  if (!event.sessionId) return null;
  return (
    <button
      type="button"
      onClick={() => onSession.open(event.sessionId, event.timeMs)}
      className="inline-flex items-center gap-1 text-xs text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      title={`Session ${event.sessionId}`}
      data-testid={`obs-issue-session-${event.sessionId}`}
    >
      <MonitorPlay className="h-3 w-3" aria-hidden="true" />
      <SessionName scope={onSession.scope} sessionId={event.sessionId} />
    </button>
  );
}

function IssueBody({
  detail,
  scope,
  subject,
  range,
  back,
  openTrace,
  openSession,
  openIssue,
}: {
  detail: IssueDetailModel;
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  back: ReactNode;
  openTrace: (traceId: string, spanId?: string) => void;
  openSession: SessionLinks;
  openIssue: (id: string) => void;
}) {
  const { issue } = detail;
  const now = Date.now();
  // Captured at render: the chat's first send happens after this view has unmounted.
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  const onChatCreated = useMemo(() => linkChatToIssue(api, queryClient, issue.id), [api, queryClient, issue.id]);
  const askSubject = useMemo(() => issueAskSubject(issue, detail), [issue, detail]);
  // The timeline the Discussion reads (one query): a chat already started from this issue leads.
  const timeline = useIssueTimeline(issue.id);
  const linked = useMemo(() => latestChatLink(mergeTimelinePages(timeline.data?.pages ?? [])), [timeline.data]);
  const openChat = useOpenChat();
  const historyScope = useMemo(() => ({ orgId: scope.orgId, projectId: scope.projectId, envIds: issue.envId ? [issue.envId] : [] }), [scope.orgId, scope.projectId, issue.envId]);
  return (
    <div className="space-y-4" data-testid="obs-issue" data-state="ready" data-status={issue.status ?? ""}>
      {back}
      <header className="space-y-1.5" data-testid="obs-issue-header">
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status={issue.status} />
          <h3 className="min-w-0 break-words text-base font-semibold text-ink">{issue.title || issue.exceptionType || issue.fingerprint}</h3>
          <span className="ml-auto">
            <AskReliantActions
              subject={askSubject}
              onChatCreated={onChatCreated}
              linkedChat={linked ? { label: chatLabel(linked), open: () => void openChat(linked.chatId) } : undefined}
            />
          </span>
        </div>
        {issue.culprit && <p className="break-all font-mono text-xs text-ink-muted" data-testid="obs-issue-culprit">{issue.culprit}</p>}
        <p className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-ink-muted">
          <span className="font-mono text-ink">{issue.service || "(unnamed)"}</span>
          {issue.platform && <span>{PLATFORM_LABEL[issue.platform]}</span>}
          {issue.envName && <span>in {issue.envName}</span>}
          {issue.lastSeenMs > 0 && <span title={formatWhen(issue.lastSeenMs, now)}>last seen {formatAgo(issue.lastSeenMs, now)}</span>}
          {issue.firstSeenMs > 0 && <span title={formatWhen(issue.firstSeenMs, now)}>first seen {formatAgo(issue.firstSeenMs, now)}</span>}
          {issue.status === "resolved" && issue.resolvedVersion && <span>resolved in {issue.resolvedVersion}</span>}
          {issue.status === "ignored" && <span>{issue.ignoredUntilMs ? `ignored until ${formatWhen(issue.ignoredUntilMs, now)}` : "ignored until unignored"}</span>}
          {issue.mergedInto && (
            <button type="button" className="underline underline-offset-2 hover:text-ink" onClick={() => openIssue(issue.mergedInto)}>
              merged into another issue
            </button>
          )}
        </p>
      </header>

      <IssueTriage issue={issue} />
      <IssueAssignee issue={issue} orgId={scope.orgId} />
      {issue.relatedCount > 0 && <Incident issue={issue} detail={detail} openIssue={openIssue} />}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <div className="min-w-0 space-y-4">
          <Card padding="none" data-testid="obs-issue-discussion">
            <Section title="Discussion" note="Status changes, comments, assignments and chats, oldest first." />
            <IssueTimeline key={`${scope.orgId}:${issue.id}`} issueId={issue.id} orgId={scope.orgId} />
          </Card>
          <Card padding="none" data-testid="obs-issue-latest">
            <Section title="Latest event" note={detail.latestEvent ? formatTimestamp(detail.latestEvent.timeMs, now) : undefined} />
            {detail.latestEvent ? (
              <LatestEvent event={detail.latestEvent} onTrace={openTrace} onSession={openSession} />
            ) : (
              <p className="px-4 py-4 text-sm text-ink-muted">The newest event has aged out of retention; the counts still stand.</p>
            )}
          </Card>
          <Card padding="none" data-testid="obs-issue-frames">
            <Section
              title="Stack"
              note={issue.symbolicated ? "Innermost frame first." : "Innermost frame first. This browser stack could not be mapped back to source, so it shows bundle locations."}
            />
            <Frames frames={detail.frames} />
          </Card>
          <Card padding="none" data-testid="obs-issue-events">
            <Section title="Recent events" note="Newest first." />
            <RecentEvents issueId={issue.id} onTrace={openTrace} onSession={openSession} />
          </Card>
        </div>
        <div className="min-w-0 space-y-4">
          <Card padding="none" data-testid="obs-issue-counts">
            <Section title={RANGE_LABELS[range]} />
            <div className="space-y-3 px-4 py-3">
              <p className="flex flex-wrap items-baseline gap-x-4 text-sm">
                <span>
                  <span className="text-lg font-semibold tabular-nums text-ink">{formatCount(issue.events)}</span>{" "}
                  <span className="text-ink-muted">events</span>
                </span>
                <span>
                  <span className="text-lg font-semibold tabular-nums text-ink">{formatCount(issue.affected)}</span>{" "}
                  <span className="text-ink-muted">{affectedNoun(issue.platform, issue.affected)}</span>
                </span>
              </p>
              <Trend points={issue.trend} label={`${formatCount(issue.events)} events, by hour`} />
            </div>
          </Card>
          <Card padding="none" data-testid="obs-issue-release">
            <Section title="First release" />
            {detail.firstRelease ? (
              <div className="space-y-1 px-4 py-3 text-xs text-ink-muted">
                <p className="font-mono text-sm text-ink">{detail.firstRelease.version}</p>
                {detail.firstRelease.gitCommit && (
                  <p className="inline-flex items-center gap-1 font-mono" title={detail.firstRelease.gitCommit}>
                    <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
                    {shortCommit(detail.firstRelease.gitCommit)}
                    {detail.firstRelease.commitRange && <span> ({detail.firstRelease.commitRange})</span>}
                  </p>
                )}
                {detail.firstRelease.promotedAtMs > 0 && <p>promoted {formatWhen(detail.firstRelease.promotedAtMs, now)}</p>}
              </div>
            ) : (
              <p className="px-4 py-3 text-xs text-ink-muted">
                First seen in <span className="font-mono text-ink">{issue.firstVersion || "an unstamped build"}</span>, which isn&apos;t a release the ledger knows.
              </p>
            )}
          </Card>
          <Card padding="none" data-testid="obs-issue-versions">
            <Section title="By version" note="Newest version first." />
            <Versions detail={detail} />
          </Card>
          <Card padding="none" data-testid="obs-issue-related">
            <Section title="Fires with" note="Issues seen in the same traces or sessions." />
            {detail.coOccurringIssueIds.length === 0 ? (
              <p className="px-4 py-3 text-xs text-ink-muted">Nothing else tends to fire alongside it.</p>
            ) : (
              <ul className="space-y-1 px-4 py-3">
                {detail.coOccurringIssueIds.map((id) => (
                  <li key={id}>
                    <button
                      type="button"
                      onClick={() => openIssue(id)}
                      className="font-mono text-xs text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      data-testid={`obs-issue-related-${id}`}
                    >
                      {id}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          <Card padding="none" data-testid="obs-issue-history">
            <Section title="History" note="Who triaged it, and when it opened or regressed." />
            <ActivityFeed scope={historyScope} target={{ kind: "issue", id: issue.id }} subject={subject} empty="No triage yet." testId="obs-issue-history-feed" />
          </Card>
        </div>
      </div>
    </div>
  );
}

/**
 * "N related errors (same incident)". ListIssues has no incident_key filter, so
 * there is no list to link to; the issues of the incident come from GetIssue
 * (incident_issue_ids), which the reader opens one by one.
 */
function Incident({ issue, detail, openIssue }: { issue: Issue; detail: IssueDetailModel; openIssue: (id: string) => void }) {
  const ids = detail.incidentIssueIds;
  return (
    <div className="rounded-md border border-border/60 bg-surface-sunken px-3 py-2 text-xs" data-testid="obs-issue-incident" data-incident={issue.incidentKey}>
      <p className="text-ink">
        <strong className="font-medium">{issue.relatedCount} related {issue.relatedCount === 1 ? "error" : "errors"}</strong> (same incident)
        <span className="text-ink-muted"> — one cause failing several callers at about the same time. Each keeps its own counts and state.</span>
      </p>
      {ids.length > 0 && (
        <ul className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
          {ids.map((id) => (
            <li key={id}>
              <button
                type="button"
                onClick={() => openIssue(id)}
                className="font-mono text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                data-testid={`obs-issue-incident-${id}`}
              >
                {id}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Section({ title, note }: { title: string; note?: string }) {
  return (
    <div className="border-b border-border/60 px-4 py-3">
      <h4 className="text-sm font-medium text-ink">{title}</h4>
      {note && <p className="text-xs text-ink-muted">{note}</p>}
    </div>
  );
}

function TraceLink({ event, onTrace }: { event: IssueEvent; onTrace: (traceId: string, spanId?: string) => void }) {
  if (!event.traceId) return null;
  return (
    <button
      type="button"
      onClick={() => onTrace(event.traceId, event.spanId)}
      className="inline-flex items-center gap-1 text-xs text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid={`obs-issue-trace-${event.traceId}`}
    >
      <ExternalLink className="h-3 w-3" aria-hidden="true" />
      Open trace <span className="font-mono">{event.traceId.slice(0, 12)}</span>
    </button>
  );
}

function LatestEvent({ event, onTrace, onSession }: { event: IssueEvent; onTrace: (traceId: string, spanId?: string) => void; onSession: SessionLinks }) {
  return (
    <div className="space-y-3 px-4 py-3">
      <p className="whitespace-pre-wrap break-words font-mono text-xs text-ink" data-testid="obs-issue-latest-message">
        {event.message || "(no message)"}
      </p>
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-muted">
        {event.serviceVersion && <span className="font-mono">{event.serviceVersion}</span>}
        {event.urlPath && <span className="font-mono">{event.urlPath}</span>}
        <SessionLink event={event} onSession={onSession} />
        <TraceLink event={event} onTrace={onTrace} />
      </p>
      <AttributeTable title="Attributes" attributes={event.attributes} />
    </div>
  );
}

function Frames({ frames }: { frames: IssueFrame[] }) {
  if (frames.length === 0) return <p className="px-4 py-3 text-xs text-ink-muted">No stack was recorded for the latest event.</p>;
  return (
    <ol className="divide-y divide-border/40 font-mono text-xs">
      {frames.map((frame, index) => (
        <li
          key={`${index}-${frame.file}-${frame.line}`}
          className={cn("px-4 py-1.5", frame.inApp ? "text-ink" : "text-ink-muted")}
          data-testid="obs-issue-frame"
          data-in-app={frame.inApp}
        >
          {frameText(frame)}
          {!frame.inApp && <span className="ml-2 font-sans text-2xs uppercase tracking-wide">library</span>}
        </li>
      ))}
    </ol>
  );
}

type VersionRow = { version: IssueVersionBreakdown };

const VERSION_COLUMNS: DataTableColumn<VersionRow>[] = [
  { key: "version", header: "Version", className: "font-mono", render: (_, { version }) => version.version || "(unstamped)" },
  { key: "events", header: "Events", numeric: true, render: (_, { version }) => formatCount(version.events) },
  { key: "affected", header: "Affected", numeric: true, render: (_, { version }) => formatCount(version.affected) },
];

function Versions({ detail }: { detail: IssueDetailModel }) {
  if (detail.byVersion.length === 0) return <p className="px-4 py-3 text-xs text-ink-muted">No events in the window.</p>;
  return (
    <DataTable<VersionRow>
      flush
      stacked
      compact
      ariaLabel="Events and affected per version"
      columns={VERSION_COLUMNS}
      data={detail.byVersion.map((version) => ({ version }))}
      getRowKey={({ version }) => version.version}
      getRowProps={({ version }) => ({ "data-testid": `obs-issue-version-${version.version}` })}
    />
  );
}

function RecentEvents({ issueId, onTrace, onSession }: { issueId: string; onTrace: (traceId: string, spanId?: string) => void; onSession: SessionLinks }) {
  const events = useIssueEvents(issueId);
  const rows = events.data?.pages.flatMap((page) => page.events) ?? [];
  if (events.isLoading) return <ObsLoadingRows rows={3} testId="obs-issue-events-loading" />;
  if (events.error && !events.data) return <p className="px-4 py-3 text-xs text-danger-ink">The events didn&apos;t load.</p>;
  if (rows.length === 0) return <p className="px-4 py-3 text-xs text-ink-muted">No events retained.</p>;
  const now = Date.now();
  return (
    <div>
      <ol className="divide-y divide-border/40">
        {rows.map((event, index) => (
          <li key={`${event.timeMs}-${event.traceId}-${index}`} className="space-y-0.5 px-4 py-2 text-xs" data-testid="obs-issue-event">
            <p className="flex flex-wrap items-center gap-x-3 text-ink-muted">
              <span className="tabular-nums">{formatTimestamp(event.timeMs, now)}</span>
              {event.serviceVersion && <span className="font-mono">{event.serviceVersion}</span>}
              <TraceLink event={event} onTrace={onTrace} />
              <SessionLink event={event} onSession={onSession} />
            </p>
            {event.message && <p className="truncate font-mono text-ink">{event.message}</p>}
          </li>
        ))}
      </ol>
      {events.hasNextPage && (
        <div className="border-t border-border/60 px-4 py-2">
          <Button variant="ghost" size="sm" onClick={() => void events.fetchNextPage()} loading={events.isFetchingNextPage} data-testid="obs-issue-events-more">
            Show older events
          </Button>
        </div>
      )}
    </div>
  );
}
