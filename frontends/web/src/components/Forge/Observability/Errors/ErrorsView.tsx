// Copyright (c) 2025 Reliant Labs

/**
 * Errors: the scope's issues (IssuesService), browser and server alike — and,
 * when the URL names one, that issue in detail with its triage.
 *
 * An issue is a group of errors sharing a fingerprint, in one environment of
 * one service. The list answers "what is broken, how much, since when"; the
 * counts and the hourly trend are over the panel's window, and the status
 * filter defaults to the open ones (new, unresolved, regressed), which is the
 * server's own default — so an empty filter in the URL and the server agree.
 *
 * Paging is the server's (next_page_token), with the first page's window
 * pinned for the rest (hooks/observability-queries.ts useIssues).
 */

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { useCurrentIssueUser, useIssues, useOrgMembers, type IssueFilters } from "@/hooks/observability-queries";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { issuesErrorKind } from "@/services/controlPlane/observability/errors";
import {
  ISSUE_PLATFORMS,
  ISSUE_SORTS,
  ISSUE_STATUSES,
  type Issue,
  type IssuePlatform,
  type IssueScope,
  type IssueSort,
  type IssueStatus,
} from "@/services/controlPlane/observability/issuesTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { AskReliantMenu } from "../AskReliant/AskReliant";
import { formatAgo, formatCount, formatWhen } from "../format";
import { ServiceSelect } from "../LogsView";
import { issueStatusParam, type ObsNavigate } from "../obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows, ObsNoData } from "../ObsStates";
import { IssueDetail } from "./IssueDetail";
import { IssuesNotConfigured } from "./IssueStates";
import { StatusBadge, Trend } from "./IssueBits";
import { affectedNoun, issueAskSubject, PLATFORM_LABEL, SORT_LABEL, STATUS_LABEL } from "./issueModel";
import { linkChatToIssue } from "./linkIssueChat";
import { assigneeFilterOf, assigneeFilterWaitsForMe, assigneeParam, serverAssignee } from "./issueTimelineModel";

export interface ErrorsViewProps {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  /** Substring of the title or culprit (the URL's `q`). */
  text: string;
  service: string | undefined;
  /** Empty: the open ones. */
  statuses: IssueStatus[];
  platform: IssuePlatform | undefined;
  sort: IssueSort;
  /** `me` or a user id; absent: anyone's. */
  assignee: string | undefined;
  /** The issue open in detail. */
  issueId: string | undefined;
  navigate: ObsNavigate;
}

export function ErrorsView(props: ErrorsViewProps) {
  if (props.issueId) {
    return (
      <IssueDetail
        issueId={props.issueId}
        scope={props.scope}
        subject={props.subject}
        range={props.range}
        navigate={props.navigate}
        onBack={() => props.navigate({ issue: undefined })}
      />
    );
  }
  return <IssueList {...props} />;
}

export function issueScopeOf(scope: ObsScope): IssueScope {
  return { orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds };
}

function IssueList({ scope, subject, range, text, service, statuses, platform, sort, assignee, navigate }: ErrorsViewProps) {
  const issueScope = useMemo(() => issueScopeOf(scope), [scope]);
  const me = useCurrentIssueUser();
  const assigneeFilter = useMemo(() => assigneeFilterOf(assignee), [assignee]);
  // "Mine" is a user id to the server, so the list waits until the reader is known rather than showing everyone's.
  const waitingForMe = assigneeFilterWaitsForMe(assigneeFilter, me.data?.id) && !me.isError && me.data !== null;
  const meUnknown = assigneeFilter.kind === "me" && !me.isLoading && !me.data?.id;
  const serverUser = serverAssignee(assigneeFilter, me.data?.id);
  const filters: IssueFilters = useMemo(
    () => ({ range, statuses, platform, service, assignee: serverUser, sort, text }),
    [range, statuses, platform, service, serverUser, sort, text]
  );
  const issues = useIssues(waitingForMe || meUnknown ? null : issueScope, filters);
  const rows = useMemo(() => issues.data?.pages.flatMap((page) => page.issues) ?? [], [issues.data]);
  const filtered = statuses.length > 0 || !!platform || !!service || !!text || assigneeFilter.kind !== "anyone";

  return (
    <div className="space-y-3" data-testid="obs-errors">
      <IssueToolbar scope={scope} range={range} text={text} service={service} statuses={statuses} platform={platform} sort={sort} assignee={assignee} navigate={navigate} />
      {issues.data && <ObsAsOf updatedAt={issues.dataUpdatedAt} />}
      {meUnknown ? (
        <p className="rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5 text-sm text-ink-muted" data-testid="obs-errors-mine-unknown">
          Reliant can&apos;t tell who you are here, so &ldquo;Assigned to me&rdquo; has nothing to match.{" "}
          <button type="button" className="underline underline-offset-2" onClick={() => navigate({ assignee: undefined }, { replace: true })}>
            Show everyone&apos;s
          </button>
        </p>
      ) : issues.isLoading || waitingForMe ? (
        <Card padding="none">
          <ObsLoadingRows testId="obs-errors-loading" />
        </Card>
      ) : issues.error && !issues.data ? (
        issuesErrorKind(issues.error) === "not-configured" ? (
          <IssuesNotConfigured />
        ) : (
          <ObsError error={issues.error} subject={subject} onRetry={() => void issues.refetch()} retrying={issues.isFetching} />
        )
      ) : rows.length === 0 ? (
        <ObsNoData
          what={statuses.length ? "issues" : "open issues"}
          range={range}
          filtered={filtered}
          topic="errors"
          onWiden={() => navigate({ range: "7d" })}
          onClearFilters={() => navigate({ istatus: undefined, platform: undefined, service: undefined, assignee: undefined, q: undefined })}
        />
      ) : (
        <IssueTable
          issues={rows}
          hasMore={!!issues.hasNextPage}
          loadingMore={issues.isFetchingNextPage}
          onLoadMore={() => void issues.fetchNextPage()}
          onOpen={(id) => navigate({ issue: id })}
        />
      )}
    </div>
  );
}

const SELECT = "h-8 rounded-md border border-border bg-surface-sunken px-2 text-sm text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function IssueToolbar({
  scope,
  range,
  text,
  service,
  statuses,
  platform,
  sort,
  assignee,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  text: string;
  service: string | undefined;
  statuses: IssueStatus[];
  platform: IssuePlatform | undefined;
  sort: IssueSort;
  assignee: string | undefined;
  navigate: ObsNavigate;
}) {
  const members = useOrgMembers(scope.orgId);
  const me = useCurrentIssueUser();
  const filter = assigneeFilterOf(assignee);
  const people = (members.data?.users ?? []).filter((m) => m.id !== me.data?.id);
  // A person named in the URL who the roster does not list (or cannot be read) is still selectable.
  const named = filter.kind === "user" && !people.some((p) => p.id === filter.userId) && filter.userId !== me.data?.id;
  const [draft, setDraft] = useState(text);
  useEffect(() => setDraft(text), [text]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    navigate({ q: draft.trim() || undefined });
  };
  const toggle = (status: IssueStatus) => {
    const next = statuses.includes(status) ? statuses.filter((s) => s !== status) : [...statuses, status];
    navigate({ istatus: issueStatusParam(next) }, { replace: true });
  };
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label="Status" className="inline-flex flex-wrap rounded-lg border border-border bg-surface-sunken p-0.5" data-testid="obs-errors-status">
          <button
            type="button"
            aria-pressed={statuses.length === 0}
            onClick={() => navigate({ istatus: undefined }, { replace: true })}
            className={chip(statuses.length === 0)}
            data-testid="obs-errors-status-open"
            title="New, unresolved and regressed"
          >
            Open
          </button>
          {ISSUE_STATUSES.map((status) => (
            <button
              key={status}
              type="button"
              aria-pressed={statuses.includes(status)}
              onClick={() => toggle(status)}
              className={chip(statuses.includes(status))}
              data-testid={`obs-errors-status-${status}`}
            >
              {STATUS_LABEL[status]}
            </button>
          ))}
        </div>
        <label htmlFor="obs-errors-platform" className="sr-only">
          Platform
        </label>
        <select
          id="obs-errors-platform"
          value={platform ?? ""}
          onChange={(event) => navigate({ platform: (event.target.value || undefined) as IssuePlatform | undefined }, { replace: true })}
          className={SELECT}
          data-testid="obs-errors-platform"
        >
          <option value="">Server and browser</option>
          {ISSUE_PLATFORMS.map((p) => (
            <option key={p} value={p}>
              {PLATFORM_LABEL[p]} only
            </option>
          ))}
        </select>
        <label htmlFor="obs-errors-assignee" className="sr-only">
          Assignee
        </label>
        <select
          id="obs-errors-assignee"
          value={assignee ?? ""}
          onChange={(event) => navigate({ assignee: event.target.value || undefined }, { replace: true })}
          className={SELECT}
          data-testid="obs-errors-assignee"
        >
          <option value="">Anyone</option>
          <option value="me">Assigned to me</option>
          {people.map((person) => (
            <option key={person.id} value={assigneeParam({ kind: "user", userId: person.id })}>
              {person.name}
            </option>
          ))}
          {named && <option value={assignee}>{assignee}</option>}
        </select>
        <ServiceSelect scope={scope} range={range} value={service} onChange={(next) => navigate({ service: next }, { replace: true })} id="obs-errors-service" />
        <label htmlFor="obs-errors-sort" className="sr-only">
          Sort
        </label>
        <select
          id="obs-errors-sort"
          value={sort}
          onChange={(event) => navigate({ isort: event.target.value === "last" ? undefined : (event.target.value as IssueSort) }, { replace: true })}
          className={SELECT}
          data-testid="obs-errors-sort"
        >
          {ISSUE_SORTS.map((s) => (
            <option key={s} value={s}>
              {SORT_LABEL[s]}
            </option>
          ))}
        </select>
      </div>
      <form role="search" onSubmit={submit} className="max-w-xl">
        <label htmlFor="obs-errors-query" className="sr-only">
          Title or culprit contains
        </label>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-ink-muted" aria-hidden="true" />
          <input
            id="obs-errors-query"
            type="search"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Title or culprit contains, e.g. TypeError or checkout.tsx"
            className="h-8 w-full rounded-md border border-border bg-surface-sunken pl-8 pr-3 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="obs-errors-query"
          />
        </div>
      </form>
    </div>
  );
}

function chip(active: boolean) {
  return cn(
    "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
    active ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
  );
}

type IssueRow = { issue: Issue };

function IssueTable({
  issues,
  hasMore,
  loadingMore,
  onLoadMore,
  onOpen,
}: {
  issues: Issue[];
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  onOpen: (id: string) => void;
}) {
  const now = Date.now();
  // Captured at render: a chat's first send happens after this view has unmounted (linkIssueChat.ts).
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  const columns: DataTableColumn<IssueRow>[] = [
    {
      key: "issue",
      header: "Issue",
      className: "w-full max-w-0",
      render: (_, { issue }) => (
        <>
          <span className="block truncate font-medium" data-testid={`obs-issue-open-${issue.id}`}>
            {issueTitle(issue)}
          </span>
          {issue.culprit && <span className="block truncate font-mono text-xs text-ink-muted">{issue.culprit}</span>}
          <span className="mt-0.5 flex flex-wrap gap-x-2 text-xs font-normal text-ink-muted">
            <span className="font-mono">{issue.service || "(unnamed)"}</span>
            {issue.platform && <span>{PLATFORM_LABEL[issue.platform]}</span>}
            {issue.envName && <span>{issue.envName}</span>}
          </span>
        </>
      ),
    },
    {
      key: "status",
      header: "Status",
      render: (_, { issue }) => (
        <>
          <StatusBadge status={issue.status} />
          {issue.relatedCount > 0 && (
            <span className="mt-1 block text-xs text-ink-muted" data-testid={`obs-issue-related-count-${issue.id}`} title="Other issues of the same incident">
              +{issue.relatedCount} related
            </span>
          )}
        </>
      ),
    },
    {
      key: "assignee",
      header: "Assignee",
      render: (_, { issue }) => (
        <span className="text-xs" data-testid={`obs-issue-assignee-${issue.id}`}>
          {issue.assignee ? <span className="text-ink">{issue.assignee.name}</span> : <span className="text-ink-muted">—</span>}
        </span>
      ),
    },
    { key: "events", header: "Events", numeric: true, render: (_, { issue }) => formatCount(issue.events) },
    {
      key: "affected",
      header: "Affected",
      numeric: true,
      render: (_, { issue }) => (
        <span title={affectedNoun(issue.platform, issue.affected)}>
          {formatCount(issue.affected)} <span className="text-xs text-ink-muted">{affectedNoun(issue.platform, issue.affected)}</span>
        </span>
      ),
    },
    { key: "trend", header: "Trend", render: (_, { issue }) => <Trend points={issue.trend} label={`${formatCount(issue.events)} events, by hour`} /> },
    {
      key: "lastSeen",
      header: "Last seen",
      render: (_, { issue }) => (
        <span className="text-xs text-ink-muted" title={issue.lastSeenMs ? formatWhen(issue.lastSeenMs, now) : undefined}>{issue.lastSeenMs ? formatAgo(issue.lastSeenMs, now) : "—"}</span>
      ),
    },
    {
      key: "firstSeen",
      header: "First seen",
      render: (_, { issue }) => (
        <span className="text-xs text-ink-muted" title={issue.firstSeenMs ? formatWhen(issue.firstSeenMs, now) : undefined}>
          {issue.firstSeenMs ? formatAgo(issue.firstSeenMs, now) : "—"}
          {(issue.firstRelease || issue.firstVersion) && <span className="block font-mono">{issue.firstRelease || issue.firstVersion}</span>}
        </span>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      width: "2.5rem",
      className: "px-2 text-right",
      render: (_, { issue }) => (
        <AskReliantMenu subject={issueAskSubject(issue)} onChatCreated={linkChatToIssue(api, queryClient, issue.id)} testId={`obs-issue-ask-${issue.id}`} />
      ),
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="obs-errors-list">
      <DataTable<IssueRow>
        flush
        stacked
        ariaLabel="Issues in scope"
        columns={columns}
        data={issues.map((issue) => ({ issue }))}
        getRowKey={({ issue }) => issue.id}
        getRowAction={({ issue }) => () => onOpen(issue.id)}
        getRowLabel={({ issue }) => issueTitle(issue)}
        getRowProps={({ issue }) => ({ className: "align-top", "data-testid": `obs-issue-${issue.id}`, "data-status": issue.status ?? "" })}
      />
      {hasMore && (
        <div className="border-t border-border/60 px-4 py-2">
          <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} data-testid="obs-errors-more">
            Load more issues
          </Button>
        </div>
      )}
    </Card>
  );
}

function issueTitle(issue: Issue): string {
  return issue.title || issue.exceptionType || issue.fingerprint;
}
