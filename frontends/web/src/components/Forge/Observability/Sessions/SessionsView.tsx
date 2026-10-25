// Copyright (c) 2025 Reliant Labs

/**
 * Sessions: the scope's real-user browser sessions (RumService), newest
 * first — and, when the URL names one, that session with its masked replay
 * and timeline.
 *
 * Everything a reader narrows by is in the URL: the window (`range`), the
 * service, only sessions with errors (`errors`) or with a replay (`replay`),
 * a user id (`user`), the entry path (`q`), and the PAGE: `cursor` is the
 * trail of page tokens walked to reach it, so a page is a link and Previous is
 * one too. A filter change starts again from the first page, because a token
 * is only valid with the filters that issued it.
 */

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { ChevronLeft, ChevronRight, Search } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useSessions, type SessionFilters } from "@/hooks/observability-session-queries";
import { rumErrorKind } from "@/services/controlPlane/observability/errors";
import type { RumScope, SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";
import { StackedTable } from "@/components/Forge/StackedTable";

import { formatAgo, formatCount, formatDuration, formatWhen } from "../format";
import { ServiceSelect, shortId } from "../LogsView";
import type { ObsNavigate } from "../obsSearch";
import { ObsError, ObsLoadingRows, ObsNoData } from "../ObsStates";
import type { ReplayerCtor } from "./ReplayPlayer";
import { SessionDetail } from "./SessionDetail";
import { SessionsNotConfigured } from "./SessionStates";

export interface SessionsViewProps {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  service: string | undefined;
  /** Entry path contains (the URL's `q`). */
  text: string;
  hasError: boolean;
  hasReplay: boolean;
  user: string;
  /** The page-token trail (the URL's `cursor`). */
  cursor: readonly string[];
  /** The session open in detail. */
  sessionId: string | undefined;
  /** Open its replay at this moment (epoch ms). */
  atMs: number | undefined;
  navigate: ObsNavigate;
  /** Test seam: the Replayer the player constructs. */
  loadReplayer?: () => Promise<ReplayerCtor>;
}

/** The scope a RumService call carries: no attribute filters (RumScope has none). */
export function rumScopeOf(scope: ObsScope, service?: string): RumScope {
  return { orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds, services: service ? [service] : [] };
}

/** What a filter change also resets: the page (its token belongs to the old filters). */
const FIRST_PAGE = { cursor: undefined } as const;

export function SessionsView(props: SessionsViewProps) {
  const { scope, service, sessionId, navigate } = props;
  const rumScope = useMemo(() => rumScopeOf(scope), [scope]);
  if (sessionId) {
    return (
      <SessionDetail
        scope={rumScope}
        sessionId={sessionId}
        subject={props.subject}
        atMs={props.atMs}
        navigate={navigate}
        onBack={() => navigate({ session: undefined, t: undefined })}
        loadReplayer={props.loadReplayer}
      />
    );
  }
  return <SessionList {...props} rumScope={rumScope} service={service} />;
}

function SessionList({ scope, rumScope, subject, range, service, text, hasError, hasReplay, user, cursor, navigate }: SessionsViewProps & { rumScope: RumScope }) {
  const pageToken = cursor.length ? cursor[cursor.length - 1] : undefined;
  const filters: SessionFilters = useMemo(
    () => ({ range, hasError, hasReplay, userId: user, pathContains: text, service, pageToken }),
    [range, hasError, hasReplay, user, text, service, pageToken]
  );
  const sessions = useSessions(rumScope, filters);
  const rows = sessions.data?.sessions ?? [];
  const filtered = hasError || hasReplay || !!user || !!text || !!service;
  const page = cursor.length + 1;

  return (
    <div className="space-y-3" data-testid="obs-sessions">
      <SessionToolbar scope={scope} range={range} service={service} text={text} hasError={hasError} hasReplay={hasReplay} user={user} navigate={navigate} />
      {sessions.isLoading ? (
        <Card padding="none">
          <ObsLoadingRows testId="obs-sessions-loading" />
        </Card>
      ) : sessions.error && !sessions.data ? (
        rumErrorKind(sessions.error) === "not-configured" ? (
          <SessionsNotConfigured />
        ) : (
          <ObsError error={sessions.error} subject={subject} onRetry={() => void sessions.refetch()} retrying={sessions.isFetching} />
        )
      ) : rows.length === 0 && page === 1 ? (
        <ObsNoData
          what="browser sessions"
          range={range}
          filtered={filtered}
          onWiden={() => navigate({ range: "7d", ...FIRST_PAGE })}
          onClearFilters={() => navigate({ errors: undefined, replay: undefined, user: undefined, q: undefined, service: undefined, ...FIRST_PAGE })}
        />
      ) : (
        <Card padding="none" data-testid="obs-sessions-list" aria-busy={sessions.isFetching || undefined}>
          <SessionTable sessions={rows} onOpen={(id) => navigate({ session: id, t: undefined })} />
          <nav aria-label="Session pages" className="flex items-center justify-between gap-2 border-t border-border/60 px-4 py-2 text-xs text-muted-foreground">
            <span data-testid="obs-sessions-page">Page {page}</span>
            <span className="flex gap-2">
              <Button
                variant="ghost"
                size="sm"
                disabled={page === 1}
                onClick={() => navigate({ cursor: cursor.length > 1 ? cursor.slice(0, -1) : undefined })}
                leftIcon={<ChevronLeft className="h-3.5 w-3.5" />}
                data-testid="obs-sessions-prev"
              >
                Newer
              </Button>
              <Button
                variant="ghost"
                size="sm"
                disabled={!sessions.data?.nextPageToken || sessions.isPlaceholderData}
                onClick={() => navigate({ cursor: [...cursor, sessions.data!.nextPageToken] })}
                rightIcon={<ChevronRight className="h-3.5 w-3.5" />}
                data-testid="obs-sessions-next"
              >
                Older
              </Button>
            </span>
          </nav>
        </Card>
      )}
    </div>
  );
}

const INPUT =
  "h-8 w-full rounded-md border border-border bg-background px-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function SessionToolbar({
  scope,
  range,
  service,
  text,
  hasError,
  hasReplay,
  user,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  service: string | undefined;
  text: string;
  hasError: boolean;
  hasReplay: boolean;
  user: string;
  navigate: ObsNavigate;
}) {
  const [pathDraft, setPathDraft] = useState(text);
  const [userDraft, setUserDraft] = useState(user);
  useEffect(() => setPathDraft(text), [text]);
  useEffect(() => setUserDraft(user), [user]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    navigate({ q: pathDraft.trim() || undefined, user: userDraft.trim() || undefined, ...FIRST_PAGE });
  };
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label="Only sessions" className="inline-flex rounded-lg border border-border bg-background p-0.5">
          <button
            type="button"
            aria-pressed={hasError}
            onClick={() => navigate({ errors: hasError ? undefined : true, ...FIRST_PAGE }, { replace: true })}
            className={chip(hasError)}
            data-testid="obs-sessions-with-errors"
          >
            With errors
          </button>
          <button
            type="button"
            aria-pressed={hasReplay}
            onClick={() => navigate({ replay: hasReplay ? undefined : true, ...FIRST_PAGE }, { replace: true })}
            className={chip(hasReplay)}
            data-testid="obs-sessions-with-replay"
          >
            With replay
          </button>
        </div>
        <ServiceSelect scope={scope} range={range} value={service} onChange={(next) => navigate({ service: next, ...FIRST_PAGE }, { replace: true })} id="obs-sessions-service" />
      </div>
      <form role="search" onSubmit={submit} className="flex max-w-2xl flex-wrap items-center gap-2">
        <div className="relative min-w-[14rem] flex-1">
          <label htmlFor="obs-sessions-path" className="sr-only">
            Entry path contains
          </label>
          <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <input
            id="obs-sessions-path"
            type="search"
            value={pathDraft}
            onChange={(event) => setPathDraft(event.target.value)}
            placeholder="Entry path contains, e.g. /checkout"
            className={cn(INPUT, "pl-8")}
            data-testid="obs-sessions-path"
          />
        </div>
        <div className="w-44">
          <label htmlFor="obs-sessions-user" className="sr-only">
            User id
          </label>
          <input
            id="obs-sessions-user"
            type="search"
            value={userDraft}
            onChange={(event) => setUserDraft(event.target.value)}
            placeholder="User id"
            className={INPUT}
            data-testid="obs-sessions-user"
          />
        </div>
        <Button type="submit" variant="outline" size="sm" data-testid="obs-sessions-search">
          Search
        </Button>
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

function SessionTable({ sessions, onOpen }: { sessions: SessionSummary[]; onOpen: (id: string) => void }) {
  const now = Date.now();
  if (sessions.length === 0) {
    return <p className="px-4 py-6 text-sm text-muted-foreground">No more sessions in this window.</p>;
  }
  return (
    <div className="overflow-x-auto">
      <StackedTable className="w-full text-sm">
        <caption className="sr-only">Browser sessions in scope, newest first</caption>
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-4 py-2 font-medium">Session</th>
            <th scope="col" className="px-4 py-2 font-medium">Entry</th>
            <th scope="col" className="px-4 py-2 font-medium">Started</th>
            <th scope="col" className="px-4 py-2 text-right font-medium">Duration</th>
            <th scope="col" className="px-4 py-2 text-right font-medium">Pages</th>
            <th scope="col" className="px-4 py-2 text-right font-medium">Interactions</th>
            <th scope="col" className="px-4 py-2 font-medium">Errors</th>
          </tr>
        </thead>
        <tbody>
          {sessions.map((s) => (
            <tr key={`${s.service}-${s.sessionId}`} className="border-b border-border/50 align-top last:border-b-0 hover:bg-muted/40" data-testid={`obs-session-${s.sessionId}`}>
              <th scope="row" className="px-4 py-2 text-left font-normal">
                <button
                  type="button"
                  onClick={() => onOpen(s.sessionId)}
                  className="block max-w-full truncate text-left font-medium text-foreground underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  data-testid={`obs-session-open-${s.sessionId}`}
                >
                  {s.userId || <span className="text-muted-foreground">anonymous</span>}
                </button>
                <p className="mt-0.5 flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
                  <span className="font-mono" title={s.sessionId}>
                    {shortId(s.sessionId)}
                  </span>
                  <span className="font-mono">{s.service || "(unnamed)"}</span>
                  {s.envName && <span>{s.envName}</span>}
                  {s.hasReplay && <Tag title="This session's replay was recorded">replay</Tag>}
                </p>
              </th>
              <td className="max-w-[16rem] truncate px-4 py-2 font-mono text-xs text-foreground" title={s.entryPath}>
                {s.entryPath || "/"}
              </td>
              <td className="whitespace-nowrap px-4 py-2 text-xs text-muted-foreground" title={s.startedMs ? formatWhen(s.startedMs, now) : undefined}>
                {s.startedMs ? formatAgo(s.startedMs, now) : "—"}
              </td>
              <td className="px-4 py-2 text-right tabular-nums text-foreground">{formatDuration(Math.max(0, s.endedMs - s.startedMs))}</td>
              <td className="px-4 py-2 text-right tabular-nums text-foreground">{formatCount(s.pageViews)}</td>
              <td className="px-4 py-2 text-right tabular-nums text-foreground">{formatCount(s.interactions)}</td>
              <td className="px-4 py-2">
                {s.errors > 0 ? <Status tone="danger" label={formatCount(s.errors)} /> : <Status tone="neutral" label="none" />}
              </td>
            </tr>
          ))}
        </tbody>
      </StackedTable>
    </div>
  );
}
