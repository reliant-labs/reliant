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
import { Search } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import { useIssues, type IssueFilters } from "@/hooks/observability-queries";
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

import { formatAgo, formatCount, formatWhen } from "../format";
import { ServiceSelect } from "../LogsView";
import { issueStatusParam, type ObsNavigate } from "../obsSearch";
import { ObsError, ObsLoadingRows, ObsNoData } from "../ObsStates";
import { IssueDetail } from "./IssueDetail";
import { IssuesNotConfigured } from "./IssueStates";
import { StatusBadge, Trend } from "./IssueBits";
import { affectedNoun, PLATFORM_LABEL, SORT_LABEL, STATUS_LABEL } from "./issueModel";
import { StackedTable } from "@/components/Forge/StackedTable";

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

function IssueList({ scope, subject, range, text, service, statuses, platform, sort, navigate }: ErrorsViewProps) {
  const issueScope = useMemo(() => issueScopeOf(scope), [scope]);
  const filters: IssueFilters = useMemo(() => ({ range, statuses, platform, service, sort, text }), [range, statuses, platform, service, sort, text]);
  const issues = useIssues(issueScope, filters);
  const rows = useMemo(() => issues.data?.pages.flatMap((page) => page.issues) ?? [], [issues.data]);
  const filtered = statuses.length > 0 || !!platform || !!service || !!text;

  return (
    <div className="space-y-3" data-testid="obs-errors">
      <IssueToolbar scope={scope} range={range} text={text} service={service} statuses={statuses} platform={platform} sort={sort} navigate={navigate} />
      {issues.isLoading ? (
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
          onWiden={() => navigate({ range: "7d" })}
          onClearFilters={() => navigate({ istatus: undefined, platform: undefined, service: undefined, q: undefined })}
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

const SELECT = "h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function IssueToolbar({
  scope,
  range,
  text,
  service,
  statuses,
  platform,
  sort,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  text: string;
  service: string | undefined;
  statuses: IssueStatus[];
  platform: IssuePlatform | undefined;
  sort: IssueSort;
  navigate: ObsNavigate;
}) {
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
        <div role="group" aria-label="Status" className="inline-flex flex-wrap rounded-lg border border-border bg-background p-0.5" data-testid="obs-errors-status">
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
          <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <input
            id="obs-errors-query"
            type="search"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Title or culprit contains, e.g. TypeError or checkout.tsx"
            className="h-8 w-full rounded-md border border-border bg-background pl-8 pr-3 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
    active ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"
  );
}

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
  return (
    <Card padding="none" data-testid="obs-errors-list">
      <div className="overflow-x-auto">
        <StackedTable className="w-full text-sm">
          <caption className="sr-only">Issues in scope</caption>
          <thead>
            <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
              <th scope="col" className="px-4 py-2 font-medium">Issue</th>
              <th scope="col" className="px-4 py-2 font-medium">Status</th>
              <th scope="col" className="px-4 py-2 text-right font-medium">Events</th>
              <th scope="col" className="px-4 py-2 text-right font-medium">Affected</th>
              <th scope="col" className="px-4 py-2 font-medium">Trend</th>
              <th scope="col" className="px-4 py-2 font-medium">Last seen</th>
              <th scope="col" className="px-4 py-2 font-medium">First seen</th>
            </tr>
          </thead>
          <tbody>
            {issues.map((issue) => (
              <tr key={issue.id} className="border-b border-border/50 align-top last:border-b-0 hover:bg-muted/40" data-testid={`obs-issue-${issue.id}`} data-status={issue.status ?? ""}>
                <th scope="row" className="max-w-[32rem] px-4 py-2 text-left font-normal">
                  <button
                    type="button"
                    onClick={() => onOpen(issue.id)}
                    className="block max-w-full truncate text-left font-medium text-foreground underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    data-testid={`obs-issue-open-${issue.id}`}
                  >
                    {issue.title || issue.exceptionType || issue.fingerprint}
                  </button>
                  {issue.culprit && <p className="truncate font-mono text-xs text-muted-foreground">{issue.culprit}</p>}
                  <p className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
                    <span className="font-mono">{issue.service || "(unnamed)"}</span>
                    {issue.platform && <span>{PLATFORM_LABEL[issue.platform]}</span>}
                    {issue.envName && <span>{issue.envName}</span>}
                  </p>
                </th>
                <td className="px-4 py-2">
                  <StatusBadge status={issue.status} />
                </td>
                <td className="px-4 py-2 text-right tabular-nums text-foreground">{formatCount(issue.events)}</td>
                <td className="px-4 py-2 text-right tabular-nums text-foreground" title={affectedNoun(issue.platform, issue.affected)}>
                  {formatCount(issue.affected)} <span className="text-xs text-muted-foreground">{affectedNoun(issue.platform, issue.affected)}</span>
                </td>
                <td className="px-4 py-2">
                  <Trend points={issue.trend} label={`${formatCount(issue.events)} events, by hour`} />
                </td>
                <td className="whitespace-nowrap px-4 py-2 text-xs text-muted-foreground" title={issue.lastSeenMs ? formatWhen(issue.lastSeenMs, now) : undefined}>
                  {issue.lastSeenMs ? formatAgo(issue.lastSeenMs, now) : "—"}
                </td>
                <td className="whitespace-nowrap px-4 py-2 text-xs text-muted-foreground" title={issue.firstSeenMs ? formatWhen(issue.firstSeenMs, now) : undefined}>
                  {issue.firstSeenMs ? formatAgo(issue.firstSeenMs, now) : "—"}
                  {(issue.firstRelease || issue.firstVersion) && <span className="block font-mono">{issue.firstRelease || issue.firstVersion}</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </StackedTable>
      </div>
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
