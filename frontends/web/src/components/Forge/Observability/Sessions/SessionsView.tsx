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
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Mono from "@/components/forge-ui/mono";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { REPLAY_EVIDENCE_RANGE, useFrontendReplay, useReplayEvidence, useSessions, type SessionFilters } from "@/hooks/observability-session-queries";
import { rumErrorKind } from "@/services/controlPlane/observability/errors";
import type { RumScope, SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { formatAgo, formatCount, formatDuration } from "../format";
import { ServiceSelect } from "../LogsView";
import type { ObsNavigate } from "../obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows, ObsNoData } from "../ObsStates";
import { Place } from "../Place";
import type { ReplayerCtor } from "./ReplayPlayer";
import { SessionDetail } from "./SessionDetail";
import { isReplayKnownUnplayable, rumScopeOf, sessionTitle } from "./sessionModel";
import { SessionsNotConfigured, SessionsReplayOff } from "./SessionStates";
import { offEnvIds, replayStatus } from "./replayStatus";
import { ClientName, DeviceSummary } from "../Client";
import { ClientFilters, hasClientFilters, NO_CLIENT_FILTERS, type ClientFilterValues } from "../ClientFilters";
import { CONTROL_FIELD, SEGMENTED, segmentTone } from "../../controlStyles";

export interface SessionsViewProps {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  service: string | undefined;
  /** Entry path contains (the URL's `q`). */
  text: string;
  hasError: boolean;
  user: string;
  /** Only sessions of this client and frontend build (the URL's `browser`, `os`, `fe`). */
  client?: ClientFilterValues;
  /** The page-token trail (the URL's `cursor`). */
  cursor: readonly string[];
  /** The session open in detail. */
  sessionId: string | undefined;
  /** Open its replay at this moment (epoch ms). */
  atMs: number | undefined;
  navigate: ObsNavigate;
  /** Test seam: the Replayer the player constructs. */
  loadReplayer?: () => Promise<ReplayerCtor>;
  /** The place's environments: names an environment the replay-state read could not reach. */
  environments?: readonly { id: string; name: string }[];
}

/** What a filter change also resets: the page (its token belongs to the old filters). */
const FIRST_PAGE = { cursor: undefined } as const;
const NO_FILTER_VALUES: ClientFilterValues = {};
const SESSION_CLIENT_FIELDS = ["browser", "os", "fe"] as const;

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

function SessionList({ scope, rumScope, subject, range, service, text, hasError, user, client = NO_FILTER_VALUES, cursor, navigate, environments }: SessionsViewProps & { rumScope: RumScope }) {
  const pageToken = cursor.length ? cursor[cursor.length - 1] : undefined;
  const { browser, os, fe } = client;
  const filters: SessionFilters = useMemo(
    () => ({
      range,
      hasError,
     
      userId: user,
      pathContains: text,
      service,
      pageToken,
      browser: browser || undefined,
      os: os || undefined,
      frontendVersion: fe || undefined,
    }),
    [range, hasError, user, text, service, pageToken, browser, os, fe]
  );
  const sessions = useSessions(rumScope, filters);
  const rows = sessions.data?.sessions ?? [];
  const filtered = hasError || !!user || !!text || !!service || hasClientFilters(client);
  const page = cursor.length + 1;

  return (
    <div className="space-y-3" data-testid="obs-sessions">
      <SessionToolbar scope={scope} range={range} service={service} text={text} hasError={hasError} user={user} navigate={navigate} />
      <ClientFilters
        id="obs-sessions-client"
        fields={SESSION_CLIENT_FIELDS}
        values={client}
        onApply={(next) => navigate({ ...next, ...FIRST_PAGE }, { replace: true })}
      />
      {sessions.data && <ObsAsOf updatedAt={sessions.dataUpdatedAt} />}
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
      ) : rows.length === 0 && page === 1 && !filtered ? (
        <SessionsNoReplays rumScope={rumScope} range={range} environments={environments} onWiden={() => navigate({ range: "7d", ...FIRST_PAGE })} />
      ) : rows.length === 0 && page === 1 ? (
        <ObsNoData
          what="browser sessions"
          range={range}
          filtered={filtered}
          topic="sessions"
          onWiden={() => navigate({ range: "7d", ...FIRST_PAGE })}
          onClearFilters={() => navigate({ errors: undefined, user: undefined, q: undefined, service: undefined, ...NO_CLIENT_FILTERS, ...FIRST_PAGE })}
        />
      ) : (
        <Card padding="none" data-testid="obs-sessions-list" aria-busy={sessions.isFetching || undefined}>
          <SessionTable sessions={rows} onOpen={(id) => navigate({ session: id, t: undefined })} />
          <nav aria-label="Session pages" className="flex items-center justify-between gap-2 border-t border-border/60 px-4 py-2 text-xs text-ink-muted">
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

/**
 * The empty, unfiltered first page: no replay in the window. It reads each
 * frontend's replay switch (ListFrontendReplay) and, for one whose config
 * reads "off", whether replays arrived anyway over the last 7 days — the
 * deprecated build flag, which the server cannot see. Until both answer, or
 * when either fails, the note stays the generic one: it never says "off" on a
 * guess. A 7-day window needs no second read: this empty page is the evidence.
 */
function SessionsNoReplays({
  rumScope,
  range,
  environments,
  onWiden,
}: {
  rumScope: RumScope;
  range: RangePreset;
  environments?: readonly { id: string; name: string }[];
  onWiden: () => void;
}) {
  const replay = useFrontendReplay(rumScope);
  const list = replay.data;
  const offEnvs = useMemo(() => (list ? offEnvIds(list) : []), [list]);
  const needEvidence = offEnvs.length > 0 && range !== REPLAY_EVIDENCE_RANGE;
  const evidence = useReplayEvidence(rumScope, offEnvs, needEvidence);
  const status = useMemo(() => {
    if (!list || (needEvidence && evidence.isLoading)) return null;
    const observed = !needEvidence ? [] : evidence.data ? evidence.data.sessions : null;
    return replayStatus(list, observed, (id) => environments?.find((env) => env.id === id)?.name);
  }, [list, needEvidence, evidence.isLoading, evidence.data, environments]);
  return <SessionsReplayOff range={range} onWiden={onWiden} status={status} />;
}

const INPUT =
  `${CONTROL_FIELD} h-8 w-full px-2.5 text-sm`;

function SessionToolbar({
  scope,
  range,
  service,
  text,
  hasError,
  user,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  service: string | undefined;
  text: string;
  hasError: boolean;
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
        <div role="group" aria-label="Only sessions" className={SEGMENTED}>
          <button
            type="button"
            aria-pressed={hasError}
            onClick={() => navigate({ errors: hasError ? undefined : true, ...FIRST_PAGE }, { replace: true })}
            className={chip(hasError)}
            data-testid="obs-sessions-with-errors"
          >
            With errors
          </button>
        </div>
        <ServiceSelect scope={scope} range={range} value={service} onChange={(next) => navigate({ service: next, ...FIRST_PAGE }, { replace: true })} id="obs-sessions-service" />
      </div>
      <form role="search" onSubmit={submit} className="flex max-w-2xl flex-wrap items-center gap-2">
        <div className="relative min-w-[14rem] flex-1">
          <label htmlFor="obs-sessions-path" className="sr-only">
            Entry path contains
          </label>
          <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-ink-muted" aria-hidden="true" />
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
    segmentTone(active)
  );
}

type SessionRow = { session: SessionSummary };

function SessionTable({ sessions, onOpen }: { sessions: SessionSummary[]; onOpen: (id: string) => void }) {
  const now = Date.now();
  if (sessions.length === 0) {
    return <p className="px-4 py-6 text-sm text-ink-muted">No more sessions in this window.</p>;
  }
  // Not a whole-row action: the session column carries its id's copy button,
  // and a button cannot hold another. Its title is the opening button.
  const columns: DataTableColumn<SessionRow>[] = [
    {
      key: "session",
      header: "Session",
      interactive: true,
      render: (_, { session: s }) => (
        <>
          <button
            type="button"
            onClick={() => onOpen(s.sessionId)}
            className="block max-w-full truncate text-left font-medium text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            title={s.startedMs ? `Started ${formatAgo(s.startedMs, now)}` : undefined}
            data-testid={`obs-session-open-${s.sessionId}`}
          >
            {sessionTitle(s.service, s.startedMs, { now })}
          </button>
          <span className="mt-0.5 flex flex-wrap items-center gap-x-2 text-xs font-normal text-ink-muted">
            {s.userId ? <span className="text-ink">{s.userId}</span> : <span>anonymous</span>}
            {s.envName && <span>{s.envName}</span>}
            <ReplayMark session={s} />
            <Mono value={s.sessionId} length={8} />
          </span>
        </>
      ),
    },
    {
      key: "location",
      header: "Location",
      className: "max-w-[12rem]",
      render: (_, { session: s }) => <Place geo={s} className="text-xs text-ink-muted" testId={`obs-session-place-${s.sessionId}`} />,
    },
    {
      key: "device",
      header: "Device",
      className: "max-w-[10rem]",
      render: (_, { session: s }) => <DeviceSummary client={s.client} className="text-xs" testId={`obs-session-device-${s.sessionId}`} />,
    },
    {
      key: "client",
      header: "Client",
      className: "max-w-[10rem]",
      render: (_, { session: s }) => <ClientName client={s.client} className="text-xs" testId={`obs-session-client-${s.sessionId}`} />,
    },
    {
      key: "version",
      header: "App version",
      className: "max-w-[10rem]",
      render: (_, { session: s }) => <AppVersion session={s} />,
    },
    {
      key: "entry",
      header: "Entry",
      className: "max-w-[16rem] truncate",
      render: (_, { session: s }) => (
        <span className="font-mono text-xs" title={s.entryPath}>
          {s.entryPath || "/"}
        </span>
      ),
    },
    { key: "duration", header: "Duration", numeric: true, render: (_, { session: s }) => formatDuration(Math.max(0, s.endedMs - s.startedMs)) },
    { key: "pages", header: "Pages", numeric: true, render: (_, { session: s }) => formatCount(s.pageViews) },
    { key: "interactions", header: "Interactions", numeric: true, render: (_, { session: s }) => formatCount(s.interactions) },
    {
      key: "errors",
      header: "Errors",
      render: (_, { session: s }) => (s.errors > 0 ? <Status tone="danger" label={formatCount(s.errors)} /> : <Status tone="neutral" label="none" />),
    },
  ];
  return (
    <DataTable<SessionRow>
      flush
      stacked
      ariaLabel="Browser sessions in scope, newest first"
      columns={columns}
      data={sessions.map((session) => ({ session }))}
      getRowKey={({ session }) => `${session.service}-${session.sessionId}`}
      getRowProps={({ session }) => {
        const playable = replayPlayable(session);
        // Dimmed, not hidden: a session without a playable replay still has a
        // timeline worth opening, but it is not the row a reader wants first.
        return {
          className: cn("align-top hover:bg-ink/5", !playable && "opacity-60 hover:opacity-100 focus-within:opacity-100"),
          "data-testid": `obs-session-${session.sessionId}`,
          "data-replay": playable ? "playable" : session.hasReplay ? "unplayable" : "none",
        };
      }}
    />
  );
}

/**
 * The version the session ran: the forge release its frontend shipped in
 * (which names every service of that bundle), with the frontend's build id —
 * what its source maps are keyed by — beneath; the build id alone when no
 * release carries it. In the desktop app, the app's own version when it differs.
 */
function AppVersion({ session: s }: { session: SessionSummary }) {
  const desktop = s.client.desktopVersion && s.client.desktopVersion !== s.serviceVersion ? s.client.desktopVersion : "";
  if (!s.release && !s.serviceVersion && !desktop) return <span className="text-xs text-ink-muted">—</span>;
  return (
    <span className="flex min-w-0 flex-col leading-tight" data-testid={`obs-session-version-${s.sessionId}`}>
      {s.release ? (
        <span className="truncate font-mono text-xs text-ink" title={`Release ${s.release}: the bundle this frontend build shipped in`} data-testid="obs-session-release">
          {s.release}
        </span>
      ) : (
        s.serviceVersion && (
          <span className="truncate font-mono text-xs text-ink" title={`Frontend build ${s.serviceVersion} (no release carries it)`}>
            {s.serviceVersion}
          </span>
        )
      )}
      {s.release && s.serviceVersion && (
        <span className="truncate font-mono text-2xs text-ink-muted" title="The frontend's build id, which its source maps are keyed by">
          build {s.serviceVersion}
        </span>
      )}
      {desktop && <span className="truncate text-2xs text-ink-muted">desktop {desktop}</span>}
    </span>
  );
}

/** Whether the row offers a replay worth opening: recorded, and not found unplayable. */
function replayPlayable(session: SessionSummary): boolean {
  return session.hasReplay && !isReplayKnownUnplayable(session.sessionId);
}

/** The row's replay mark: a replay, or a replay this tab found unplayable. */
function ReplayMark({ session }: { session: SessionSummary }) {
  if (session.hasReplay && !isReplayKnownUnplayable(session.sessionId)) {
    return <Tag title="This session's replay was recorded">replay</Tag>;
  }
  if (session.hasReplay) {
    return (
      <span title="Its replay was opened and holds no full snapshot of the page to play from." data-testid={`obs-session-replay-incomplete-${session.sessionId}`}>
        replay incomplete
      </span>
    );
  }
  return null;
}
