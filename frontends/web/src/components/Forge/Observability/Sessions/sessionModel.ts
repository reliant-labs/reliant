// Copyright (c) 2025 Reliant Labs

/**
 * The Sessions view's small decisions, kept out of the components so they are
 * tested once: when a session counts as live, how a playback offset reads,
 * what each timeline kind is called, and where a span carries its session.
 */

import type { ObsSearch } from "../obsSearch";
import { RRWEB_FULL_SNAPSHOT, RRWEB_META, type RrwebEvent, type RumScope, type SessionSummary, type TimelineEntry, type TimelineKind } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";

/** The scope a RumService call carries: no attribute filters (RumScope has none). */
export function rumScopeOf(scope: ObsScope, service?: string): RumScope {
  return { orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds, services: service ? [service] : [] };
}

/**
 * A session whose last activity is this recent may still be recording. The
 * recorder flushes every 5 s and the session rollup trails it by a little, so
 * two minutes of silence is a session that ended (or a tab left idle — which
 * the feed then stops following after its idle polls).
 */
export const LIVE_WINDOW_MS = 2 * 60_000;

export function isLive(session: Pick<SessionSummary, "endedMs">, now: number = Date.now()): boolean {
  return session.endedMs > 0 && now - session.endedMs < LIVE_WINDOW_MS;
}

/**
 * A recording that stops this long before its session's last activity is
 * partial: the rest was never uploaded (the tab closed mid-batch, a batch was
 * dropped) or has not finished arriving.
 */
export const PARTIAL_GAP_MS = 30_000;

/**
 * Where playback of `events` can start: the Meta event (it carries the
 * viewport rrweb sizes the page by) just before the first full snapshot, else
 * the snapshot itself; -1 when there is no full snapshot.
 *
 * Events before it belong to no page rrweb can draw. They are usually the last
 * few events of the PREVIOUS session: the recorder stamps a batch with the
 * session id current when it uploads (up to 5 s later), so at a session
 * boundary the old session's tail is stored under the new one.
 */
export function recordingStart(events: readonly RrwebEvent[]): number {
  const snapshot = events.findIndex((e) => e.type === RRWEB_FULL_SNAPSHOT);
  if (snapshot < 0) return -1;
  for (let i = snapshot - 1; i >= 0; i--) {
    if (events[i].type === RRWEB_META) return i;
  }
  return snapshot;
}

/**
 * Why a session has no replay, as far as its summary can tell. A session with
 * no page view and no interaction saw only background requests: nothing was on
 * screen for the recorder (which captures nothing in a hidden window). Anything
 * else could be any of the recorder's reasons, so they are named together.
 */
export function noReplayReason(session: Pick<SessionSummary, "pageViews" | "interactions">): string {
  if (session.pageViews === 0 && session.interactions === 0) {
    return "Nothing was on screen to record: the session saw no page view or interaction, only background requests, as a window in the background does.";
  }
  return "The recorder didn't capture it: the session was outside the app's replay sample, replay is off for this app, or its window stayed in the background.";
}

/**
 * Sessions whose replay this tab has opened and found unplayable (no full
 * snapshot, or a snapshot with nothing after it). The list only knows that a
 * session has replay events, not whether they can be played, so it marks these
 * from what the player learned. Held for the tab's session.
 */
const UNPLAYABLE_KEY = "obs.sessions.unplayable";
const UNPLAYABLE_MAX = 500;
let unplayable: Set<string> | null = null;

function unplayableSet(): Set<string> {
  if (unplayable) return unplayable;
  unplayable = new Set();
  try {
    const raw = window.sessionStorage.getItem(UNPLAYABLE_KEY);
    const ids: unknown = raw ? JSON.parse(raw) : [];
    if (Array.isArray(ids)) for (const id of ids) if (typeof id === "string") unplayable.add(id);
  } catch {
    // No storage: remembered for this page only.
  }
  return unplayable;
}

export function noteReplayUnplayable(sessionId: string): void {
  const ids = unplayableSet();
  if (ids.has(sessionId)) return;
  ids.add(sessionId);
  try {
    window.sessionStorage.setItem(UNPLAYABLE_KEY, JSON.stringify([...ids].slice(-UNPLAYABLE_MAX)));
  } catch {
    // Not persisted: still known for this page.
  }
}

export function isReplayKnownUnplayable(sessionId: string): boolean {
  return unplayableSet().has(sessionId);
}

/** Tests only: forget what the player learned. */
export function resetReplayVerdictsForTests(): void {
  unplayable = null;
  try {
    window.sessionStorage.removeItem(UNPLAYABLE_KEY);
  } catch {
    // Nothing to clear.
  }
}

/** A playback position: `1:05`, `1:02:09`. */
export function formatOffset(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

export const TIMELINE_KIND_LABEL: Record<TimelineKind, string> = {
  navigation: "Navigation",
  click: "Click",
  request_failed: "Failed request",
  error: "Error",
  event: "Event",
};

/** The timeline's filter chips, and which kinds each shows. */
export const TIMELINE_FILTERS = [
  { id: "all", label: "All", kinds: null },
  { id: "errors", label: "Errors", kinds: ["error", "request_failed"] },
  { id: "navigation", label: "Navigation", kinds: ["navigation"] },
  { id: "clicks", label: "Clicks", kinds: ["click"] },
  { id: "events", label: "Events", kinds: ["event"] },
] as const satisfies readonly { id: string; label: string; kinds: readonly TimelineKind[] | null }[];
export type TimelineFilter = (typeof TIMELINE_FILTERS)[number]["id"];

export function filterTimeline(entries: readonly TimelineEntry[], filter: TimelineFilter): TimelineEntry[] {
  const kinds = TIMELINE_FILTERS.find((f) => f.id === filter)?.kinds ?? null;
  return kinds ? entries.filter((e) => e.kind !== null && (kinds as readonly TimelineKind[]).includes(e.kind)) : [...entries];
}

/** The entry the playhead is at or just past: the last one that has happened. */
export function currentEntryIndex(entries: readonly TimelineEntry[], atMs: number | null): number {
  if (atMs === null) return -1;
  let found = -1;
  for (let i = 0; i < entries.length; i++) {
    if (entries[i].timeMs <= atMs) found = i;
    else break;
  }
  return found;
}

/** A navigation's name as a person says it. */
export function navigationLabel(name: string): string {
  if (name === "documentLoad") return "Page load";
  if (name === "routeChange") return "Route change";
  return name || "Navigation";
}

/** What a session is called when it does not say which frontend recorded it. */
export const UNKNOWN_APP = "Unknown app";

export interface SessionWhenOptions {
  now?: number;
  /** IANA zone the day words are judged in. Default: the viewer's. */
  timeZone?: string;
  /** Default: the viewer's. */
  locale?: string;
}

/** A calendar day in `timeZone` as a day number, so two days subtract without DST arithmetic. */
function dayNumber(ms: number, timeZone: string | undefined): number {
  const parts = new Intl.DateTimeFormat("en-US", { timeZone, year: "numeric", month: "numeric", day: "numeric" }).formatToParts(ms);
  const part = (type: string) => Number(parts.find((p) => p.type === type)?.value);
  return Date.UTC(part("year"), part("month") - 1, part("day")) / 86_400_000;
}

/**
 * When a session started, as a person says it in their own timezone:
 * `Today 14:32`, `Yesterday 09:10`, `Oct 25, 14:32`, and the year only when it
 * is not this one (`Oct 25, 2024, 14:32`).
 */
export function formatSessionWhen(startedMs: number, { now = Date.now(), timeZone, locale }: SessionWhenOptions = {}): string {
  if (!(startedMs > 0)) return "unknown time";
  const time = new Intl.DateTimeFormat(locale, { timeZone, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(startedMs);
  const days = dayNumber(now, timeZone) - dayNumber(startedMs, timeZone);
  if (days === 0) return `Today ${time}`;
  if (days === 1) return `Yesterday ${time}`;
  const sameYear =
    new Intl.DateTimeFormat("en-US", { timeZone, year: "numeric" }).format(startedMs) === new Intl.DateTimeFormat("en-US", { timeZone, year: "numeric" }).format(now);
  const date = new Intl.DateTimeFormat(locale, { timeZone, month: "short", day: "numeric", year: sameYear ? undefined : "numeric" }).format(startedMs);
  return `${date}, ${time}`;
}

/**
 * A session's name everywhere it is shown or linked: which frontend, and when
 * it started — `shop-web · Today 14:32`. Never the session id: that is opaque
 * to a reader, and stays beside the name as copyable metadata.
 */
export function sessionTitle(app: string | undefined, startedMs: number, options?: SessionWhenOptions): string {
  return `${app?.trim() || UNKNOWN_APP} · ${formatSessionWhen(startedMs, options)}`;
}

/** The attribute every browser span and replay record carries (forge-web-runtime / the OTel web SDK). */
export const RUM_SESSION_ATTRIBUTE = "rum.sessionId";

/** The browser session a trace ran in, from any of its spans' attributes or resource. */
export function rumSessionIdOf(spans: readonly { attributes: Record<string, string>; resource: Record<string, string> }[]): string | undefined {
  for (const span of spans) {
    const id = span.attributes[RUM_SESSION_ATTRIBUTE] || span.resource[RUM_SESSION_ATTRIBUTE];
    if (id) return id;
  }
  return undefined;
}

/**
 * What opening a session from another view navigates to: the Sessions view
 * with that session open at `atMs`, and the other view's own keys dropped.
 */
export function sessionLink(sessionId: string, atMs?: number): ObsSearch {
  return {
    view: "sessions",
    session: sessionId,
    t: atMs !== undefined && atMs > 0 ? Math.round(atMs) : undefined,
    trace: undefined,
    span: undefined,
    issue: undefined,
    istatus: undefined,
    platform: undefined,
    isort: undefined,
    assignee: undefined,
    q: undefined,
    errors: undefined,
    live: undefined,
    severity: undefined,
    minDuration: undefined,
    cursor: undefined,
  };
}

/** The keys Sessions owns, dropped when one of its links leaves for another view. */
export const LEAVE_SESSIONS: ObsSearch = {
  session: undefined,
  t: undefined,
  replay: undefined,
  user: undefined,
  cursor: undefined,
  errors: undefined,
  q: undefined,
};
