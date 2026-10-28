import { keepsLastResult } from "@/services/controlPlane/observability/quota";
// Copyright (c) 2025 Reliant Labs

/**
 * One session: who, where they came in, on which build, for how long, and
 * what went wrong — its masked replay beside its timeline. Opened from the
 * session list, from an error's latest event, from a trace a browser span
 * belongs to, or from a dashboard's sessions panel, at `t` when the link
 * names a moment.
 */

import { useMemo, useState, type ReactNode } from "react";
import { ArrowLeft, SearchX, VideoOff } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Mono from "@/components/forge-ui/mono";
import Status from "@/components/forge-ui/status";
import { useSession, useSessionTimeline } from "@/hooks/observability-session-queries";
import { rumErrorKind } from "@/services/controlPlane/observability/errors";
import type { RumScope, SessionSummary } from "@/services/controlPlane/observability/rumTypes";

import { formatAgo, formatCount, formatDuration, formatWhen } from "../format";
import type { ObsNavigate } from "../obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows } from "../ObsStates";
import { ReplayPlayer, type ReplayerCtor } from "./ReplayPlayer";
import { SessionTimeline } from "./SessionTimeline";
import { isLive, LEAVE_SESSIONS, sessionTitle } from "./sessionModel";
import { SessionNote, SessionsNotConfigured } from "./SessionStates";

export interface SessionDetailProps {
  scope: RumScope;
  sessionId: string;
  subject: string;
  /** Open the replay at this moment (epoch ms). */
  atMs?: number;
  navigate: ObsNavigate;
  onBack: () => void;
  /** Test seam: the Replayer the player constructs. */
  loadReplayer?: () => Promise<ReplayerCtor>;
}

export function SessionDetail({ scope, sessionId, subject, atMs, navigate, onBack, loadReplayer }: SessionDetailProps) {
  const session = useSession(scope, sessionId);
  const back = (
    <Button variant="ghost" size="sm" leftIcon={<ArrowLeft className="h-3.5 w-3.5" />} onClick={onBack} data-testid="session-back">
      All sessions
    </Button>
  );
  if (session.isLoading) {
    return (
      <div className="space-y-3" data-testid="session-detail" data-state="loading">
        {back}
        <ObsLoadingRows rows={5} testId="session-loading" />
      </div>
    );
  }
  if ((session.error && !keepsLastResult(session.error, session.data)) || !session.data) {
    const kind = rumErrorKind(session.error);
    return (
      <div className="space-y-3" data-testid="session-detail" data-state="error">
        {back}
        {kind === "not-found" ? (
          <SessionNote
            icon={<SearchX className="h-4 w-4" />}
            title="That session isn't here"
            testId="session-not-found"
            action={
              <Button variant="outline" size="sm" onClick={onBack}>
                Back to the list
              </Button>
            }
          >
            It may be older than this environment keeps sessions, or it belongs to an environment or service outside the current scope.
          </SessionNote>
        ) : kind === "not-configured" ? (
          <SessionsNotConfigured />
        ) : (
          <ObsError error={session.error} subject={subject} onRetry={() => void session.refetch()} retrying={session.isFetching} />
        )}
      </div>
    );
  }
  return <><ObsAsOf updatedAt={session.dataUpdatedAt} /><SessionBody scope={scope} session={session.data} subject={subject} atMs={atMs} navigate={navigate} back={back} loadReplayer={loadReplayer} /></>;
}

function SessionBody({
  scope,
  session,
  subject,
  atMs,
  navigate,
  back,
  loadReplayer,
}: {
  scope: RumScope;
  session: SessionSummary;
  subject: string;
  atMs?: number;
  navigate: ObsNavigate;
  back: ReactNode;
  loadReplayer?: () => Promise<ReplayerCtor>;
}) {
  const timeline = useSessionTimeline(scope, session.sessionId);
  const entries = useMemo(() => timeline.data?.pages.flatMap((page) => page.entries) ?? [], [timeline.data]);
  const [playhead, setPlayhead] = useState<number | null>(null);
  const [seek, setSeek] = useState<{ atMs: number; nonce: number } | undefined>(undefined);
  // Decided once, when the session is opened: following a live session is
  // the player's job from then on (it stops when the session goes quiet).
  const [live] = useState(() => isLive(session));
  const now = Date.now();

  const openIssue = (issueId: string) => navigate({ ...LEAVE_SESSIONS, view: "errors", issue: issueId, istatus: undefined, platform: undefined, isort: undefined, assignee: undefined });
  const openTrace = (traceId: string, spanId?: string) => navigate({ ...LEAVE_SESSIONS, view: "traces", trace: traceId, span: spanId });

  return (
    <div className="space-y-4" data-testid="session-detail" data-state="ready" data-session={session.sessionId}>
      {back}
      <header className="space-y-1.5" data-testid="session-header">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-base font-semibold text-ink" data-testid="session-title">
            {sessionTitle(session.service, session.startedMs, { now })}
          </h3>
          <Mono value={session.sessionId} length={8} />
          {session.errors > 0 ? (
            <Status tone="danger" label={`${formatCount(session.errors)} error${session.errors === 1 ? "" : "s"}`} />
          ) : (
            <Status tone="neutral" label="no errors" />
          )}
          {live && <Status tone="neutral" pulse label="active now" />}
        </div>
        <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-muted">
          {session.userId ? <span className="text-ink">{session.userId}</span> : <span>anonymous user</span>}
          <span>
            entered on <span className="font-mono text-ink">{session.entryPath || "/"}</span>
          </span>
          {session.serviceVersion && <span className="font-mono">{session.serviceVersion}</span>}
          {session.envName && <span>in {session.envName}</span>}
          {session.startedMs > 0 && <span title={formatWhen(session.startedMs, now)}>started {formatAgo(session.startedMs, now)}</span>}
          <span>{formatDuration(Math.max(0, session.endedMs - session.startedMs))}</span>
          <span>
            {formatCount(session.pageViews)} page{session.pageViews === 1 ? "" : "s"}
          </span>
          <span>
            {formatCount(session.interactions)} interaction{session.interactions === 1 ? "" : "s"}
          </span>
        </p>
      </header>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(20rem,2fr)]">
        <Card padding="md" data-testid="session-replay">
          {session.hasReplay ? (
            <ReplayPlayer
              key={session.sessionId}
              scope={scope}
              session={session}
              live={live}
              startAtMs={atMs}
              seek={seek}
              markers={entries}
              onTime={setPlayhead}
              loadReplayer={loadReplayer}
            />
          ) : (
            <SessionNote icon={<VideoOff className="h-4 w-4" />} title="This session wasn't recorded" testId="session-no-replay">
              Replay is recorded only where the environment opts in, and then for a sample of sessions. The timeline still shows what the user
              did and what failed.
            </SessionNote>
          )}
        </Card>
        <Card padding="md" data-testid="session-timeline-card">
          <h4 className="mb-2 text-sm font-medium text-ink">Timeline</h4>
          {timeline.isLoading ? (
            <ObsLoadingRows rows={4} testId="session-timeline-loading" />
          ) : timeline.error && !timeline.data ? (
            <ObsError error={timeline.error} subject={subject} onRetry={() => void timeline.refetch()} retrying={timeline.isFetching} />
          ) : (
            <SessionTimeline
              entries={entries}
              startMs={session.startedMs}
              currentMs={session.hasReplay ? playhead : null}
              hasMore={!!timeline.hasNextPage}
              loadingMore={timeline.isFetchingNextPage}
              onLoadMore={() => void timeline.fetchNextPage()}
              onSeek={session.hasReplay ? (at) => setSeek((prev) => ({ atMs: at, nonce: (prev?.nonce ?? 0) + 1 })) : undefined}
              onIssue={openIssue}
              onTrace={openTrace}
            />
          )}
        </Card>
      </div>
    </div>
  );
}
