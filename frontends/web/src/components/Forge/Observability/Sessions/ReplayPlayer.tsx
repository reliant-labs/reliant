// Copyright (c) 2025 Reliant Labs

/**
 * The session replay player: rrweb's Replayer (MIT, `@rrweb/replay`) behind
 * forge-ui controls — play/pause, speed, scrub, skip inactivity — fed a page
 * at a time by ReplayFeed.
 *
 * ── PRIVACY ────────────────────────────────────────────────────────────────
 *
 * Every recording is MASKED WHEN RECORDED: forge-web-runtime forces
 * maskAllInputs and maskAllText, blocks images, video, audio and embeds, and
 * never records canvas. The masked characters are all the browser ever sent,
 * so there is nothing to unmask and this player offers no way to try: it hands
 * the Replayer the events exactly as stored and draws nothing of its own over
 * them. The "Masked recording" badge says so.
 *
 * The Replayer renders into its own SANDBOXED iframe (`sandbox="allow-same-
 * origin"`, no `allow-scripts`): recorded markup never runs a script here.
 * `UNSAFE_replayCanvas` — the one option that adds `allow-scripts` — is pinned
 * off, and nothing recorded canvas anyway.
 *
 * A recording can also be FORGED (the ingest key is public and bodies are never
 * inspected), so it can name remote images, stylesheets and fonts that would be
 * fetched from the viewer's browser. The replay document is therefore confined
 * to a no-network policy (replayConfinement.ts), which also explains why the
 * sandbox keeps `allow-same-origin`: rrweb cannot rebuild into an opaque origin.
 * The player says so in the line beside the badge: external assets do not load.
 *
 * ── LOADING ────────────────────────────────────────────────────────────────
 *
 * The Replayer is ~400 KB and only this component needs it, so it is imported
 * when the first playable page has arrived (a full snapshot plus one more
 * event), never with the console's bundle. Playback starts from page one; the
 * feed buffers the rest ahead of the playhead, and a seek past the buffer
 * waits for the feed to reach it. A live session keeps growing: new events are
 * appended to the running Replayer as the feed polls them in.
 */

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { AlertTriangle, EyeOff, FastForward, Pause, Play, RefreshCw, Video } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useObsReadApi } from "@/hooks/observability-queries";
import { useObsQuotaState } from "@/services/controlPlane/observability/ObsQuotaContext";
import { quotaOf } from "@/services/controlPlane/observability/quota";
import { ObsAsOf, ObsWillLoad } from "../ObsStates";
import { RRWEB_FULL_SNAPSHOT, type RrwebEvent, type RumScope, type SessionSummary, type TimelineEntry } from "@/services/controlPlane/observability/rumTypes";
import { cn } from "@/lib/utils";

import { formatDuration } from "../format";
import { ReplayFeed, type ReplayFeedState } from "./replayFeed";
import { confineReplayDocument, FULL_SNAPSHOT_REBUILT } from "./replayConfinement";
import { formatOffset, PARTIAL_GAP_MS } from "./sessionModel";

/** The slice of rrweb's Replayer this player drives. */
export interface ReplayerLike {
  wrapper: HTMLElement;
  iframe: HTMLIFrameElement;
  play(timeOffset?: number): void;
  pause(timeOffset?: number): void;
  addEvent(event: RrwebEvent): void;
  setConfig(config: { speed?: number; skipInactive?: boolean }): void;
  getCurrentTime(): number;
  getMetaData(): { startTime: number; endTime: number; totalTime: number };
  on(event: string, handler: (payload?: unknown) => void): unknown;
  destroy(): void;
}

export type ReplayerCtor = new (events: RrwebEvent[], config: Record<string, unknown>) => ReplayerLike;

/** The real Replayer and its stylesheet (mouse cursor, wrapper), as their own chunk. */
export async function loadRrwebReplayer(): Promise<ReplayerCtor> {
  const [mod] = await Promise.all([import("@rrweb/replay"), import("@rrweb/replay/dist/style.css")]);
  return mod.Replayer as unknown as ReplayerCtor;
}

/** The byte budget the player asks each replay page for: the server's default. */
export const REPLAY_PAGE_BYTES = 4 * 1024 * 1024;
export const SPEEDS = [1, 2, 4, 8] as const;
const TICK_MS = 200;

export interface ReplayPlayerProps {
  scope: RumScope;
  session: SessionSummary;
  /** The session may still be recording: follow it. */
  live: boolean;
  /** Open at this moment (epoch ms), e.g. just before an error. */
  startAtMs?: number;
  /** A seek asked for from outside (the timeline). A new nonce seeks again. */
  seek?: { atMs: number; nonce: number };
  /** Timeline entries, drawn as marks on the scrubber. */
  markers?: readonly TimelineEntry[];
  /** The playhead (epoch ms), as it moves. */
  onTime?: (atMs: number) => void;
  /** Test seam: the Replayer to construct. */
  loadReplayer?: () => Promise<ReplayerCtor>;
  pageBytes?: number;
  /** Test seam: the feed's buffer-ahead and live-poll intervals. */
  feedTiming?: { aheadMs?: number; pollMs?: number; idlePolls?: number };
}

interface Meta {
  startTime: number;
  endTime: number;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

export function ReplayPlayer(props: ReplayPlayerProps) {
  const { session, seek, markers = [] } = props;
  const api = useObsReadApi();
  const quota = useObsQuotaState();
  const mountRef = useRef<HTMLDivElement>(null);
  const frameRef = useRef<HTMLDivElement>(null);
  const replayerRef = useRef<ReplayerLike | null>(null);
  const feedRef = useRef<ReplayFeed | null>(null);
  const pendingSeekRef = useRef<number | null>(null);
  const playingRef = useRef(false);
  const speedRef = useRef(1);
  const skipRef = useRef(true);
  // What the session's feed is built from, read once per session: a parent
  // re-render (a new scope object, `live` flipping as the clock moves) must
  // not tear down a recording that is playing.
  const argsRef = useRef({ api, props });
  useEffect(() => {
    argsRef.current = { api, props };
  });

  const [attempt, setAttempt] = useState(0);
  const [feed, setFeed] = useState<ReplayFeedState | null>(null);
  const [ready, setReady] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [playing, setPlayingState] = useState(false);
  const [speed, setSpeed] = useState(1);
  const [skipInactive, setSkipInactive] = useState(true);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [offset, setOffset] = useState(0);
  const [viewport, setViewport] = useState<{ width: number; height: number } | null>(null);
  const [boxWidth, setBoxWidth] = useState(0);
  const [pendingSeek, setPendingSeek] = useState<number | null>(null);

  const setPlaying = useCallback((value: boolean) => {
    playingRef.current = value;
    setPlayingState(value);
  }, []);

  /** Seek to `atMs` (epoch ms), or wait for the feed to reach it. */
  const seekTo = useCallback((atMs: number) => {
    feedRef.current?.setPlayhead(atMs);
    const replayer = replayerRef.current;
    const snapshot = feedRef.current?.snapshot;
    const more = !!snapshot && (snapshot.hasMore || snapshot.phase === "loading" || snapshot.phase === "live");
    if (!replayer) {
      pendingSeekRef.current = atMs;
      setPendingSeek(atMs);
      return;
    }
    const m = replayer.getMetaData();
    if (atMs > m.endTime && more) {
      pendingSeekRef.current = atMs;
      setPendingSeek(atMs);
      return;
    }
    const at = clamp(atMs - m.startTime, 0, m.totalTime);
    pendingSeekRef.current = null;
    setPendingSeek(null);
    if (playingRef.current) replayer.play(at);
    else replayer.pause(at);
    setOffset(at);
    argsRef.current.props.onTime?.(m.startTime + at);
  }, []);

  // One feed and one Replayer per session (and per retry of a failed load).
  useEffect(() => {
    const { api: rpc, props: initial } = argsRef.current;
    const loadReplayer = initial.loadReplayer ?? loadRrwebReplayer;
    let disposed = false;
    const abort = new AbortController();
    let buffer: RrwebEvent[] = [];
    let creating = false;
    setReady(false);
    setLoadError(null);
    setMeta(null);
    setOffset(0);
    setPlaying(false);
    pendingSeekRef.current = initial.startAtMs ?? null;

    const refreshMeta = (replayer: ReplayerLike) => {
      const m = replayer.getMetaData();
      setMeta({ startTime: m.startTime, endTime: m.endTime });
      return m;
    };

    const create = () => {
      if (creating || replayerRef.current) return;
      if (buffer.length < 2 || !buffer.some((e) => e.type === RRWEB_FULL_SNAPSHOT)) return;
      creating = true;
      loadReplayer().then(
        (Replayer) => {
          if (disposed || !mountRef.current) return;
          const replayer = new Replayer(buffer, {
            root: mountRef.current,
            speed: speedRef.current,
            skipInactive: skipRef.current,
            // Never: the one option that lets the sandboxed iframe run script.
            UNSAFE_replayCanvas: false,
            mouseTail: false,
            showWarning: false,
            triggerFocus: false,
          });
          // Before the first snapshot is built into it (that happens on the
          // pause/play below), and again after every rebuild.
          confineReplayDocument(replayer.iframe?.contentDocument);
          replayer.on(FULL_SNAPSHOT_REBUILT, () => confineReplayDocument(replayer.iframe?.contentDocument));
          buffer = [];
          replayerRef.current = replayer;
          replayer.iframe?.setAttribute("title", "Session replay (masked)");
          replayer.on("resize", (payload) => {
            const size = payload as { width?: number; height?: number } | undefined;
            if (size?.width && size?.height) setViewport({ width: size.width, height: size.height });
          });
          replayer.on("finish", () => setPlaying(false));
          const m = refreshMeta(replayer);
          const target = pendingSeekRef.current;
          const at = target !== null && target <= m.endTime ? clamp(target - m.startTime, 0, m.totalTime) : 0;
          if (target !== null && target <= m.endTime) {
            pendingSeekRef.current = null;
            setPendingSeek(null);
          }
          replayer.pause(at);
          setOffset(at);
          setReady(true);
        },
        (error: unknown) => {
          if (disposed) return;
          creating = false;
          setLoadError(error);
        }
      );
    };

    const feedInstance = new ReplayFeed({
      ...initial.feedTiming,
      live: initial.live,
      fetchPage: (pageToken) =>
        rpc.getSessionReplay({
          ...initial.scope,
          sessionId: initial.session.sessionId,
          maxBytes: initial.pageBytes ?? REPLAY_PAGE_BYTES,
          pageToken,
        }, { signal: abort.signal }),
      onEvents: (events) => {
        const replayer = replayerRef.current;
        if (!replayer) {
          buffer.push(...events);
          create();
          // No full snapshot yet: nothing can be drawn, so read on until one arrives.
          if (!buffer.some((e) => e.type === RRWEB_FULL_SNAPSHOT)) feedInstance.setPlayhead(events[events.length - 1].timestamp);
          return;
        }
        for (const event of events) replayer.addEvent(event);
        const m = refreshMeta(replayer);
        const target = pendingSeekRef.current;
        if (target !== null && target <= m.endTime) seekTo(target);
      },
      onChange: setFeed,
    });
    feedRef.current = feedInstance;
    if (initial.startAtMs) feedInstance.setPlayhead(initial.startAtMs);
    feedInstance.start();

    return () => {
      disposed = true;
      abort.abort();
      feedInstance.stop();
      feedRef.current = null;
      replayerRef.current?.destroy();
      replayerRef.current = null;
    };
  }, [session.sessionId, attempt, seekTo, setPlaying]);

  // Resume only quota failures when the shared jittered reset ends. Buffered
  // events and the player stay mounted throughout the pause.
  useEffect(() => {
    if (quota.kind === "ok" && quotaOf(feedRef.current?.snapshot.error)) feedRef.current?.retry();
  }, [quota]);

  // A seek past the buffer that the feed can no longer reach lands on the end.
  useEffect(() => {
    if (!feed || pendingSeek === null || !ready) return;
    if (feed.phase === "complete") seekTo(Math.min(pendingSeek, replayerRef.current?.getMetaData().endTime ?? pendingSeek));
  }, [feed, pendingSeek, ready, seekTo]);

  // Seeks asked for from outside (the timeline).
  useEffect(() => {
    if (seek) seekTo(seek.atMs);
  }, [seek, seekTo]);

  // The playhead, while playing.
  useEffect(() => {
    if (!playing || !ready) return;
    const id = setInterval(() => {
      const replayer = replayerRef.current;
      if (!replayer) return;
      const at = replayer.getCurrentTime();
      const m = replayer.getMetaData();
      setOffset(at);
      setMeta({ startTime: m.startTime, endTime: m.endTime });
      feedRef.current?.setPlayhead(m.startTime + at);
      argsRef.current.props.onTime?.(m.startTime + at);
    }, TICK_MS);
    return () => clearInterval(id);
  }, [playing, ready]);

  // The frame's width, to scale the recorded viewport into it.
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    setBoxWidth(frame.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => setBoxWidth(entries[0]?.contentRect.width ?? frame.clientWidth));
    observer.observe(frame);
    return () => observer.disconnect();
  }, []);

  const scale = viewport && boxWidth > 0 ? Math.min(1, boxWidth / viewport.width) : 1;
  useEffect(() => {
    const wrapper = replayerRef.current?.wrapper;
    if (!wrapper) return;
    wrapper.style.transform = `scale(${scale})`;
    wrapper.style.transformOrigin = "top left";
  }, [scale, ready]);

  const toggle = () => {
    const replayer = replayerRef.current;
    if (!replayer) return;
    if (playingRef.current) {
      replayer.pause();
      setOffset(replayer.getCurrentTime());
      setPlaying(false);
      return;
    }
    const m = replayer.getMetaData();
    const current = replayer.getCurrentTime();
    replayer.play(current >= m.totalTime - TICK_MS ? 0 : current);
    setPlaying(true);
  };

  const changeSpeed = (next: number) => {
    speedRef.current = next;
    setSpeed(next);
    replayerRef.current?.setConfig({ speed: next });
  };

  const changeSkip = (next: boolean) => {
    skipRef.current = next;
    setSkipInactive(next);
    replayerRef.current?.setConfig({ skipInactive: next });
  };

  const retry = () => {
    if (loadError) {
      setAttempt((n) => n + 1);
      return;
    }
    feedRef.current?.retry();
  };

  // What the scrubber spans: the recording so far, out to the session's end
  // while pages remain to be read.
  const startMs = meta?.startTime ?? session.startedMs;
  const loadedEnd = meta?.endTime ?? startMs;
  const spanEnd = feed?.hasMore ? Math.max(loadedEnd, session.endedMs) : loadedEnd;
  const span = Math.max(1, spanEnd - startMs);
  const partialGap = feed?.phase === "complete" && meta ? session.endedMs - meta.endTime : 0;

  const blocking = !ready ? blockingState(feed, loadError, session) : null;

  return (
    <div className="space-y-2" data-testid="replay-player" data-state={ready ? "ready" : (blocking?.id ?? "loading")} data-phase={feed?.phase ?? "loading"}>
      {ready && <ObsAsOf updatedAt={feed?.lastMs ?? undefined} />}
      <div className="flex flex-wrap items-center gap-2">
        <Tag
          icon={<EyeOff />}
          title="Text and inputs were masked, and media blocked, in the browser before upload. The original content was never recorded and cannot be revealed."
        >
          <span data-testid="replay-masked-badge">Masked recording</span>
        </Tag>
        <span
          className="text-xs text-ink-muted"
          title="A recording comes from the browser and can name any address. The replay is confined so that opening one never contacts those hosts: anything the page loaded from elsewhere shows as blank or unstyled."
          data-testid="replay-external-note"
        >
          External images, stylesheets and fonts are not loaded.
        </span>
        {feed?.phase === "live" && (
          <span title="This session may still be recording: new events appear as each upload completes." data-testid="replay-live">
            <Status tone="neutral" pulse size="sm" label="Live" />
          </span>
        )}
        {ready && feed?.fetching && <span className="text-xs text-ink-muted" data-testid="replay-buffering">Buffering…</span>}
      </div>

      <div
        ref={frameRef}
        className={cn("relative w-full overflow-hidden rounded-md border border-border/60 bg-surface-sunken", !ready && "min-h-[16rem]")}
        // The recorded viewport, scaled to the frame's width: runtime geometry.
        style={ready && viewport ? { height: Math.round(viewport.height * scale) } : undefined}
      >
        <div ref={mountRef} className={cn(!ready && "hidden")} data-testid="replay-mount" />
        {blocking && <div className="absolute inset-0 flex items-center justify-center p-4">{blocking.node(retry)}</div>}
      </div>

      {ready && (
        <>
          <Scrubber
            offset={offset}
            span={span}
            loadedFraction={(loadedEnd - startMs) / span}
            startMs={startMs}
            markers={markers}
            onSeek={(at) => seekTo(startMs + at)}
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={toggle}
              leftIcon={playing ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
              data-testid="replay-play"
              aria-pressed={playing}
            >
              {playing ? "Pause" : "Play"}
            </Button>
            <span className="text-xs tabular-nums text-ink-muted" data-testid="replay-time">
              {formatOffset(offset)} / {formatOffset(spanEnd - startMs)}
            </span>
            <div role="group" aria-label="Playback speed" className="inline-flex rounded-lg border border-border bg-surface-sunken p-0.5">
              {SPEEDS.map((value) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={speed === value}
                  onClick={() => changeSpeed(value)}
                  className={chip(speed === value)}
                  data-testid={`replay-speed-${value}`}
                >
                  {value}×
                </button>
              ))}
            </div>
            <button
              type="button"
              aria-pressed={skipInactive}
              onClick={() => changeSkip(!skipInactive)}
              className={cn(chip(skipInactive), "inline-flex items-center gap-1 border border-border")}
              data-testid="replay-skip-inactive"
              title="Fast-forward through stretches where the user did nothing"
            >
              <FastForward className="h-3 w-3" aria-hidden="true" />
              Skip inactivity
            </button>
          </div>
        </>
      )}

      {ready && <Notices feed={feed} partialGap={partialGap} pendingSeek={pendingSeek} onRetry={retry} />}
    </div>
  );
}

function chip(active: boolean) {
  return cn(
    "rounded-md px-2 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
    active ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
  );
}

const MARK_TONE: Partial<Record<string, string>> = {
  error: "bg-danger",
  request_failed: "bg-warning",
  navigation: "bg-ink/60",
};

function Scrubber({
  offset,
  span,
  loadedFraction,
  startMs,
  markers,
  onSeek,
}: {
  offset: number;
  span: number;
  loadedFraction: number;
  startMs: number;
  markers: readonly TimelineEntry[];
  onSeek: (offsetMs: number) => void;
}) {
  const pct = (ms: number) => `${clamp((ms / span) * 100, 0, 100)}%`;
  const marks = markers.filter((m) => m.kind && MARK_TONE[m.kind] && m.timeMs >= startMs && m.timeMs <= startMs + span);
  return (
    <div className="relative h-5 rounded-md has-[input:focus-visible]:ring-2 has-[input:focus-visible]:ring-ring" data-testid="replay-scrubber">
      {/* Positions below are runtime geometry (fractions of the recording). */}
      <div className="absolute inset-x-0 top-2 h-1 rounded-full bg-border" aria-hidden="true">
        <div className="absolute inset-y-0 left-0 rounded-full bg-border-strong" style={{ width: pct(loadedFraction * span) }} data-testid="replay-loaded" />
        <div className="absolute inset-y-0 left-0 rounded-full bg-primary" style={{ width: pct(offset) }} />
        {marks.map((m, i) => (
          <span
            key={`${m.timeMs}-${i}`}
            className={cn("absolute -top-1 h-3 w-0.5 rounded-full", MARK_TONE[m.kind ?? ""])}
            style={{ left: pct(m.timeMs - startMs) }}
            data-testid={`replay-mark-${m.kind}`}
          />
        ))}
      </div>
      <input
        type="range"
        min={0}
        max={Math.round(span)}
        step={100}
        value={Math.round(clamp(offset, 0, span))}
        onChange={(event) => onSeek(Number(event.target.value))}
        aria-label="Playback position"
        aria-valuetext={`${formatOffset(offset)} of ${formatOffset(span)}`}
        className="absolute inset-0 h-full w-full cursor-pointer opacity-0"
        data-testid="replay-seek"
      />
    </div>
  );
}

interface Blocking {
  id: string;
  node: (retry: () => void) => ReactNode;
}

/** What stands in for the player before it can play — and why, when it never will. */
function blockingState(feed: ReplayFeedState | null, loadError: unknown, session: SessionSummary): Blocking | null {
  if (loadError) {
    return {
      id: "player-error",
      node: (retry) => (
        <StateNote icon={<AlertTriangle className="h-4 w-4" />} title="The player didn't load" testId="replay-player-error" onRetry={retry}>
          The replay player is loaded on demand, and that download failed. Your connection may have dropped, or the console was just updated.
        </StateNote>
      ),
    };
  }
  if (!feed || feed.phase === "loading" || (feed.events >= 2 && feed.hasFullSnapshot && feed.phase !== "error")) {
    return { id: "loading", node: () => <p className="text-sm text-ink-muted" data-testid="replay-loading">Loading the recording…</p> };
  }
  if (feed.phase === "error" && quotaOf(feed.error)) {
    return { id: "quota", node: () => <ObsWillLoad error={feed.error} /> };
  }
  if (feed.phase === "error") {
    return {
      id: "error",
      node: (retry) => (
        <StateNote icon={<AlertTriangle className="h-4 w-4" />} title="The recording didn't load" testId="replay-error" onRetry={retry}>
          The request for this session&apos;s recording failed. The timeline beside it is unaffected.
        </StateNote>
      ),
    };
  }
  if (feed.events === 0) {
    if (feed.phase === "live") {
      return {
        id: "waiting",
        node: () => (
          <StateNote icon={<Video className="h-4 w-4" />} title="Waiting for the recording" testId="replay-waiting">
            This session is still active and nothing of its recording has finished uploading yet. It appears here as it does.
          </StateNote>
        ),
      };
    }
    return {
      id: "empty",
      node: () => (
        <StateNote icon={<Video className="h-4 w-4" />} title="Nothing of this recording was kept" testId="replay-empty">
          {session.hasReplay
            ? "The session was recorded, but none of its events are left: they are past the environment's replay retention, or were never uploaded."
            : "This session was not recorded."}
        </StateNote>
      ),
    };
  }
  // Events, but no full snapshot: rrweb has nothing to draw the incremental changes onto.
  if (feed.phase === "live" || feed.phase === "streaming") {
    return {
      id: "waiting-snapshot",
      node: () => (
        <StateNote icon={<Video className="h-4 w-4" />} title="Waiting for a full page snapshot" testId="replay-waiting-snapshot">
          {feed.events} events have arrived, but none is a full snapshot of the page yet, which playback starts from.
        </StateNote>
      ),
    };
  }
  return {
    id: "incomplete",
    node: () => (
      <StateNote icon={<AlertTriangle className="h-4 w-4" />} title="This recording is incomplete and can't be played" testId="replay-incomplete">
        {feed.hasFullSnapshot
          ? "It holds a single snapshot of the page and nothing recorded after it, so there is nothing to play."
          : `It holds ${feed.events} event${feed.events === 1 ? "" : "s"} but no full snapshot of the page, which playback needs to start from: the upload that carried it was lost or dropped.`}{" "}
        The timeline still shows what happened.
      </StateNote>
    ),
  };
}

function StateNote({ icon, title, testId, onRetry, children }: { icon: ReactNode; title: string; testId: string; onRetry?: () => void; children: ReactNode }) {
  return (
    <div role="status" data-testid={testId} className="flex max-w-md items-start gap-3 text-left">
      <span className="mt-0.5 shrink-0 text-ink-muted" aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-ink">{title}</p>
        <p className="text-xs text-ink-muted">{children}</p>
        {onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry} leftIcon={<RefreshCw className="h-3 w-3" />} data-testid={`${testId}-retry`}>
            Try again
          </Button>
        )}
      </div>
    </div>
  );
}

function Notices({ feed, partialGap, pendingSeek, onRetry }: { feed: ReplayFeedState | null; partialGap: number; pendingSeek: number | null; onRetry: () => void }) {
  if (!feed) return null;
  const notes: { id: string; tone?: "error"; text: ReactNode; action?: ReactNode }[] = [];
  if (pendingSeek !== null) notes.push({ id: "seeking", text: "Loading the recording up to that moment…" });
  if (feed.phase === "error" && !quotaOf(feed.error)) {
    notes.push({
      id: "error",
      tone: "error",
      text: "The rest of the recording didn't load. What arrived keeps playing.",
      action: (
        <Button variant="ghost" size="xs" onClick={onRetry} data-testid="replay-notice-retry">
          Try again
        </Button>
      ),
    });
  }
  if (feed.phase === "live") {
    notes.push({
      id: "live",
      text: "Following a live session: new events are added as each upload completes. An event still being uploaded is held back until it is whole.",
    });
  }
  if (partialGap > PARTIAL_GAP_MS) {
    notes.push({
      id: "partial",
      text: `The recording stops ${formatDuration(partialGap)} before the session's last activity. The rest was never uploaded — the tab closed mid-batch, or a batch was dropped.`,
    });
  }
  if (feed.unreadable > 0) {
    notes.push({
      id: "unreadable",
      text: `${feed.unreadable} recorded event${feed.unreadable === 1 ? "" : "s"} could not be read and ${feed.unreadable === 1 ? "was" : "were"} skipped, so playback may jump there.`,
    });
  }
  if (notes.length === 0) return null;
  return (
    <ul className="space-y-1">
      {notes.map((note) => (
        <li
          key={note.id}
          role="status"
          data-testid={`replay-notice-${note.id}`}
          className={cn("flex flex-wrap items-center gap-2 text-xs", note.tone === "error" ? "text-danger-ink" : "text-ink-muted")}
        >
          <span>{note.text}</span>
          {note.action}
        </li>
      ))}
    </ul>
  );
}
