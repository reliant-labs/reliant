// Copyright (c) 2025 Reliant Labs

/**
 * Feeds one session's recording to the player: GetSessionReplay a page at a
 * time, BUFFERED AHEAD of the playhead, then — for a session still being
 * recorded — FOLLOWED with resume_token.
 *
 * ── WHY A FEED AND NOT A QUERY ──────────────────────────────────────────────
 *
 * A replay is not one answer. It is up to four hours of events in pages of up
 * to 16 MiB, where the player wants the first page NOW and the rest just in
 * time, and a live session keeps growing after the last page. That is a
 * stream with backpressure, which a cache entry cannot express; so it lives
 * here, as a small state machine with no React in it, and the player drives it.
 *
 * ── THE RULES IT KEEPS (control-plane RumService.GetSessionReplay) ───────────
 *
 *   - next_page_token is set exactly when more complete events are available
 *     NOW. The feed fetches it while the buffered recording ends less than
 *     `aheadMs` past the playhead (or past a seek target), and stops there:
 *     a reader who watches ten seconds does not download four hours.
 *   - resume_token is set whenever a page held an event. With no next page,
 *     a live session is polled with it every `pollMs` for what was recorded
 *     since — including an event the server withheld because its last chunk
 *     was still in flight. A poll that brings nothing keeps the old token.
 *     After `idlePolls` empty polls in a row the session is over and the feed
 *     stops following it.
 *   - Events arrive whole and in recorded order; the feed passes them on as
 *     they come and never reorders, merges or alters one.
 *
 * A failed page stops the feed where it is (what already arrived keeps
 * playing) until retry(); nothing is retried behind the reader's back.
 */

import { RRWEB_FULL_SNAPSHOT, type ReplayPage, type RrwebEvent } from "@/services/controlPlane/observability/rumTypes";

export type ReplayFeedPhase =
  /** The first page is on its way. */
  | "loading"
  /** Pages remain; fetched as the playhead nears the end of the buffer. */
  | "streaming"
  /** Every recorded event has arrived. */
  | "complete"
  /** Every event so far has arrived, and the session is polled for more. */
  | "live"
  /** A page failed; retry() resumes from it. */
  | "error";

export interface ReplayFeedState {
  phase: ReplayFeedPhase;
  /** Events delivered so far. */
  events: number;
  /** Events the server sent that could not be read (skipped). */
  unreadable: number;
  /** The first and last delivered event's timestamp (epoch ms). */
  firstMs: number | null;
  lastMs: number | null;
  /** Whether a full snapshot has arrived — without one, nothing can be drawn. */
  hasFullSnapshot: boolean;
  /** A page request is in flight. */
  fetching: boolean;
  /** More pages are available now (next_page_token). */
  hasMore: boolean;
  error: unknown;
}

export interface FeedTimers {
  set: (fn: () => void, ms: number) => unknown;
  clear: (handle: unknown) => void;
}

export interface ReplayFeedOptions {
  /** One page; `pageToken` undefined for the first. */
  fetchPage: (pageToken: string | undefined) => Promise<ReplayPage>;
  /** New events, in recorded order, appended to what came before. */
  onEvents: (events: RrwebEvent[]) => void;
  onChange: (state: ReplayFeedState) => void;
  /** The session may still be recording: follow it with resume_token. */
  live: boolean;
  /** Buffer this much recording past the playhead. Default 2 minutes. */
  aheadMs?: number;
  /** How often a live session is polled. Default 5 s (the recorder's batch interval). */
  pollMs?: number;
  /** Stop following after this many empty polls in a row. Default 60 (5 minutes at 5 s). */
  idlePolls?: number;
  timers?: FeedTimers;
}

export const REPLAY_AHEAD_MS = 120_000;
export const REPLAY_POLL_MS = 5_000;
export const REPLAY_IDLE_POLLS = 60;

const realTimers: FeedTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export class ReplayFeed {
  private readonly opts: Required<Omit<ReplayFeedOptions, "timers">> & { timers: FeedTimers };
  private state: ReplayFeedState = {
    phase: "loading",
    events: 0,
    unreadable: 0,
    firstMs: null,
    lastMs: null,
    hasFullSnapshot: false,
    fetching: false,
    hasMore: false,
    error: null,
  };
  private nextToken: string | undefined;
  private resumeToken: string | undefined;
  /** The token of the request that failed, for retry(). */
  private failedToken: string | undefined;
  private failedWasPoll = false;
  private playheadMs = Number.NEGATIVE_INFINITY;
  private started = false;
  private stopped = false;
  private emptyPolls = 0;
  private pollHandle: unknown = null;

  constructor(options: ReplayFeedOptions) {
    this.opts = {
      ...options,
      aheadMs: options.aheadMs ?? REPLAY_AHEAD_MS,
      pollMs: options.pollMs ?? REPLAY_POLL_MS,
      idlePolls: options.idlePolls ?? REPLAY_IDLE_POLLS,
      timers: options.timers ?? realTimers,
    };
  }

  get snapshot(): ReplayFeedState {
    return this.state;
  }

  start(): void {
    if (this.started || this.stopped) return;
    this.started = true;
    this.emit({});
    void this.fetch(undefined, false);
  }

  /**
   * Where the reader is (epoch ms): the playhead, or a seek target beyond the
   * buffer. The feed fetches until the buffer reaches `aheadMs` past it.
   */
  setPlayhead(atMs: number): void {
    this.playheadMs = atMs;
    this.pump();
  }

  /** Resume after a failed page, from that page. */
  retry(): void {
    if (this.stopped || this.state.phase !== "error") return;
    const token = this.failedToken;
    const poll = this.failedWasPoll;
    this.emit({ phase: this.state.events === 0 ? "loading" : this.nextToken ? "streaming" : "live", error: null });
    void this.fetch(token, poll);
  }

  stop(): void {
    this.stopped = true;
    if (this.pollHandle !== null) this.opts.timers.clear(this.pollHandle);
    this.pollHandle = null;
  }

  private emit(patch: Partial<ReplayFeedState>): void {
    this.state = { ...this.state, ...patch };
    if (!this.stopped) this.opts.onChange(this.state);
  }

  /** Fetch the next page if the buffer is short of the playhead. */
  private pump(): void {
    if (this.stopped || !this.started || this.state.fetching || this.state.phase === "error") return;
    if (!this.nextToken) return;
    const { firstMs, lastMs } = this.state;
    if (lastMs === null || firstMs === null) {
      void this.fetch(this.nextToken, false);
      return;
    }
    if (lastMs - Math.max(this.playheadMs, firstMs) < this.opts.aheadMs) void this.fetch(this.nextToken, false);
  }

  private schedulePoll(): void {
    if (this.stopped || this.pollHandle !== null) return;
    this.pollHandle = this.opts.timers.set(() => {
      this.pollHandle = null;
      void this.fetch(this.resumeToken, true);
    }, this.opts.pollMs);
  }

  private async fetch(token: string | undefined, poll: boolean): Promise<void> {
    if (this.stopped) return;
    this.emit({ fetching: true });
    let page: ReplayPage;
    try {
      page = await this.opts.fetchPage(token);
    } catch (error) {
      if (this.stopped) return;
      this.failedToken = token;
      this.failedWasPoll = poll;
      this.emit({ fetching: false, phase: "error", error });
      return;
    }
    if (this.stopped) return;

    const events = page.events;
    if (events.length > 0) {
      this.opts.onEvents(events);
      this.emptyPolls = 0;
    } else if (poll) {
      this.emptyPolls += 1;
    }
    if (page.resumeToken) this.resumeToken = page.resumeToken;
    this.nextToken = page.nextPageToken || undefined;

    const following = this.opts.live && this.emptyPolls < this.opts.idlePolls;
    const phase: ReplayFeedPhase = this.nextToken ? "streaming" : following ? "live" : "complete";
    this.emit({
      fetching: false,
      phase,
      hasMore: !!this.nextToken,
      events: this.state.events + events.length,
      unreadable: this.state.unreadable + page.unreadable,
      firstMs: this.state.firstMs ?? (events.length ? events[0].timestamp : null),
      lastMs: events.length ? events[events.length - 1].timestamp : this.state.lastMs,
      hasFullSnapshot: this.state.hasFullSnapshot || events.some((e) => e.type === RRWEB_FULL_SNAPSHOT),
    });

    if (this.nextToken) this.pump();
    else if (phase === "live") this.schedulePoll();
  }
}
