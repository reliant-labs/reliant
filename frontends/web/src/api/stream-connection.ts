// Copyright (c) 2025 Reliant Labs
// Connection lifecycle shared by the two live streams: the user-wide stream
// (streaming-grpc.ts) and the per-chat stream (chat-streaming-grpc.ts). Each
// stream owns one instance, so they connect, back off, detect a dead socket
// and reconnect independently — a chat switch never touches the user stream,
// and a chat stream failure never takes the user stream down with it.

import { Code, ConnectError } from "@connectrpc/connect";
import { logger } from "../lib/logger";
import { pageActivity } from "../lib/pageActivity";
import type { ConnectionStatus } from "../types/streaming";

// Reconnect pacing. Each stream is a push path the UI has no other source
// for, so there is no state in which giving up permanently is correct — a
// stream that stops retrying leaves the app silently stale until a full page
// reload. We retry forever with exponential backoff capped at
// MAX_RECONNECT_DELAY_MS, and the wake triggers (online / tab-visible)
// collapse the backoff to zero the moment the user is plausibly back.
export const BASE_RECONNECT_DELAY_MS = 1_000;
export const MAX_RECONNECT_DELAY_MS = 30_000;
// Cap the exponent so Math.pow stays finite over a long outage.
const MAX_RECONNECT_EXPONENT = 6;

// A connection must stay up at least this long before we treat it as healthy
// and reset the backoff.
//
// Backoff used to reset the moment the stream reached "connected", which is the
// wrong signal: a server that accepts the stream and then immediately ends it
// is NOT a recovered server, but it looked like one, so every cycle restarted
// at attempt 1 and the exponential backoff never engaged. A server-side bug
// that ended each stream in ~5ms therefore produced an unthrottled reconnect
// storm (~20/sec for hours in dev), each cycle firing a full chat refetch.
//
// Requiring the connection to survive this window means genuine recoveries
// still reset promptly, while connect/drop churn keeps escalating the delay.
export const SHORT_LIVED_CONNECTION_MS = 10_000;

// Liveness watchdog. The server sends a heartbeat every 30s on both streams
// (internal/handlers/streaming defaultHeartbeatInterval). A half-open
// connection — backend SIGKILLed under `air`, laptop slept, proxy dropped the
// socket without a FIN — never surfaces as a stream error, so `for await` just
// blocks forever and no reconnect is attempted. Treat "no event of any kind
// for 2.5 heartbeat intervals" as dead and force a reconnect.
export const STREAM_STALE_TIMEOUT_MS = 75_000;
export const WATCHDOG_TICK_MS = 15_000;

// Resume liveness check. A tab that comes back after a phone lock or a
// laptop sleep usually still believes its stream is "connected" — iOS
// suspended the socket without closing it — and the watchdog above would
// leave it sitting on that dead socket for up to 75s, missing every update
// while the reads multiplexed onto the same HTTP/2 connection hang with it.
// A live stream delivers a heartbeat every 30s, so if nothing at all has
// arrived for longer than that by the time the page has had a moment to
// drain whatever was buffered, the connection is gone: replace it now.
export const RESUME_LIVENESS_GRACE_MS = 2_000;
export const RESUME_STALE_AFTER_MS = 40_000;

// A page hidden longer than this has had its sockets at the mercy of the
// browser: a phone suspends them without closing them, so the stream can read
// "connected" and be dead, and silence is not yet measurable against the 40s
// threshold above. Past this, never trust the stream — replace it and resume
// from the cursor (a replay of nothing when it was in fact alive). Short hides
// (app switcher peek, notification shade) keep the cheaper silence check.
export const RESUME_REPLACE_AFTER_HIDDEN_MS = 10_000;
// Resume signals arrive in bursts (visibilitychange, pageshow, online, focus
// within one wake); replace the stream at most once per burst.
export const RESUME_REPLACE_MIN_INTERVAL_MS = 5_000;

/**
 * Whether a stream failure means the network connection under it died (as
 * opposed to the server ending the stream with a status). Every request
 * multiplexed onto that connection is stranded too.
 */
export function isConnectionLevelFailure(error: unknown): boolean {
  if (!(error instanceof ConnectError)) return true;
  return error.code === Code.Unknown || error.code === Code.Unavailable;
}

/** What a stream reports about its connection. */
export interface StreamLifecycleCallbacks {
  onStatusChange?: (status: ConnectionStatus) => void;
  onError?: (error: string) => void;
}

/**
 * A server stream that reconnects forever: supersession of a replaced
 * connection, backoff that only a user wake resets, a liveness watchdog and
 * resume handling. Subclasses say how to open the stream, how to handle its
 * frames, and which setup failures are not worth retrying.
 */
export abstract class ReconnectingStream<E> {
  protected abortController: AbortController | null = null;
  // True while the stream is meant to be down: before the first start, after
  // stop(), and (the chat stream) while no chat is open.
  protected isIntentionallyClosed = false;
  protected reconnectAttempts = 0;
  // Timestamp of the last connection that stayed up long enough to count as
  // healthy. Used to distinguish a real recovery from a connect/drop churn
  // loop — see SHORT_LIVED_CONNECTION_MS.
  protected connectionOpenedAt = 0;
  protected isConnected_ = false;
  // Liveness / lifecycle bookkeeping. `connectAttemptInFlight` and
  // `reconnectTimer` exist so callers can tell "a connection is genuinely
  // being worked on" from "we hold a stale AbortController that will never
  // fire".
  protected connectAttemptInFlight = false;
  protected reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private watchdogTimer: ReturnType<typeof setInterval> | null = null;
  protected lastEventAt = 0;
  private wakeHandlersBound = false;
  private hiddenAt: number | null = null;
  private lastResumeReplaceAt = 0;
  private resumeCheckTimer: ReturnType<typeof setTimeout> | null = null;

  private readonly logPrefix: string;
  private readonly lifecycle: StreamLifecycleCallbacks;

  protected constructor(logPrefix: string, lifecycle: StreamLifecycleCallbacks) {
    this.logPrefix = logPrefix;
    this.lifecycle = lifecycle;
  }

  /** Open the server stream for the connection attempt guarded by `signal`. */
  protected abstract openStream(signal: AbortSignal): AsyncIterable<E>;

  /** Handle one frame of the current connection. */
  protected abstract handleEvent(event: E): void;

  /**
   * A connection attempt failed before (`delivered` false) or after its first
   * frame. Return true when the subclass has dealt with it and the generic
   * retry must not run — a refusal that every retry would repeat.
   */
  protected handleConnectFailure(_error: unknown, _delivered: boolean): boolean {
    return false;
  }

  /** Context for the reconnect log lines. */
  protected describe(): Record<string, unknown> {
    return {};
  }

  isConnected(): boolean {
    return this.isConnected_;
  }

  private setStatus(status: ConnectionStatus): void {
    this.lifecycle.onStatusChange?.(status);
  }

  // --- Wake triggers -------------------------------------------------------
  // Backoff can be sitting at 30s when the user comes back to a slept laptop or
  // a reconnected network. Both signals mean "conditions just changed" — drop
  // the backoff and retry immediately instead of making the user wait out a
  // timer (or reload the page).
  private handleOnline = (): void => {
    if (this.isIntentionallyClosed) return;
    // The network just changed under a stream that still reads "connected":
    // its socket belongs to the old network and will not say so.
    if (this.isConnected_) {
      this.replaceStreamOnResume("online");
      return;
    }
    logger.info(`${this.logPrefix} Network back online — reconnecting now`);
    this.reconnectNow("online");
  };

  private handleVisibilityChange = (): void => {
    if (document.visibilityState !== "visible") {
      this.hiddenAt ??= Date.now();
      return;
    }
    const hiddenFor = this.hiddenAt === null ? 0 : Date.now() - this.hiddenAt;
    this.hiddenAt = null;
    if (this.isIntentionallyClosed) return;
    if (this.isConnected_) {
      if (hiddenFor >= RESUME_REPLACE_AFTER_HIDDEN_MS) {
        this.replaceStreamOnResume("visible", { hiddenForMs: hiddenFor });
        return;
      }
      this.scheduleResumeLivenessCheck();
      return;
    }
    logger.info(`${this.logPrefix} Tab visible again — reconnecting now`);
    this.reconnectNow("visible");
  };

  // Restored from the back/forward cache: the page was frozen, whatever the
  // stream says.
  private handlePageShow = (event: PageTransitionEvent): void => {
    if (!event.persisted || this.isIntentionallyClosed) return;
    this.hiddenAt = null;
    if (this.isConnected_) {
      this.replaceStreamOnResume("visible", { restoredFromCache: true });
    } else {
      this.reconnectNow("visible");
    }
  };

  /**
   * Replace a stream that claims to be connected without waiting to see
   * whether it is. Idempotent across a burst of resume signals, and bounded:
   * at most one replacement per RESUME_REPLACE_MIN_INTERVAL_MS, so a page that
   * flaps visible/hidden cannot turn into a reconnect storm.
   */
  private replaceStreamOnResume(
    reason: "visible" | "online",
    detail: Record<string, unknown> = {},
  ): void {
    const now = Date.now();
    if (now - this.lastResumeReplaceAt < RESUME_REPLACE_MIN_INTERVAL_MS) return;
    this.lastResumeReplaceAt = now;
    if (this.resumeCheckTimer !== null) {
      clearTimeout(this.resumeCheckTimer);
      this.resumeCheckTimer = null;
    }
    logger.warn(
      `${this.logPrefix} Page resumed (${reason}) with a 'connected' stream — replacing it`,
      detail,
    );
    pageActivity().noteConnectionLost();
    this.isConnected_ = false;
    this.setStatus("disconnected");
    this.reconnectNow(reason);
  }

  private scheduleResumeLivenessCheck(): void {
    if (this.resumeCheckTimer !== null) clearTimeout(this.resumeCheckTimer);
    this.resumeCheckTimer = setTimeout(() => {
      this.resumeCheckTimer = null;
      if (this.isIntentionallyClosed || !this.isConnected_) return;
      const silentFor = Date.now() - this.lastEventAt;
      if (silentFor < RESUME_STALE_AFTER_MS) return;
      logger.warn(
        `${this.logPrefix} Tab visible again but the stream has been silent — replacing the connection`,
        { silentForMs: silentFor },
      );
      pageActivity().noteConnectionLost();
      this.isConnected_ = false;
      this.setStatus("disconnected");
      this.reconnectNow("visible");
    }, RESUME_LIVENESS_GRACE_MS);
  }

  protected bindWakeHandlers(): void {
    if (this.wakeHandlersBound || typeof window === "undefined") return;
    window.addEventListener("online", this.handleOnline);
    document.addEventListener("visibilitychange", this.handleVisibilityChange);
    window.addEventListener("pageshow", this.handlePageShow);
    this.wakeHandlersBound = true;
  }

  private unbindWakeHandlers(): void {
    if (!this.wakeHandlersBound || typeof window === "undefined") return;
    window.removeEventListener("online", this.handleOnline);
    document.removeEventListener(
      "visibilitychange",
      this.handleVisibilityChange,
    );
    window.removeEventListener("pageshow", this.handlePageShow);
    this.hiddenAt = null;
    this.wakeHandlersBound = false;
  }

  // --- Liveness watchdog ---------------------------------------------------
  private armWatchdog(): void {
    this.lastEventAt = Date.now();
    if (this.watchdogTimer !== null) return;
    this.watchdogTimer = setInterval(() => {
      if (!this.isConnected_ || this.isIntentionallyClosed) return;

      // A connection that has stayed up this long is genuinely healthy (as
      // opposed to a server accepting and instantly ending each stream), so
      // the next disconnect starts from a fresh backoff.
      if (
        this.reconnectAttempts > 0 &&
        this.connectionOpenedAt > 0 &&
        Date.now() - this.connectionOpenedAt >= SHORT_LIVED_CONNECTION_MS
      ) {
        this.reconnectAttempts = 0;
      }

      const silentFor = Date.now() - this.lastEventAt;
      if (silentFor < STREAM_STALE_TIMEOUT_MS) return;

      logger.warn(
        `${this.logPrefix} No events (not even heartbeats) — connection is half-open, forcing reconnect`,
        { silentForMs: silentFor, staleAfterMs: STREAM_STALE_TIMEOUT_MS },
      );
      // The socket is dead but `for await` will never unblock on its own.
      // reconnectNow() aborts it, which makes the orphaned loop's catch see a
      // superseded controller and bail out quietly.
      pageActivity().noteConnectionLost();
      this.isConnected_ = false;
      this.setStatus("disconnected");
      this.reconnectNow("stale");
    }, WATCHDOG_TICK_MS);
  }

  private disarmWatchdog(): void {
    if (this.watchdogTimer !== null) {
      clearInterval(this.watchdogTimer);
      this.watchdogTimer = null;
    }
  }

  /**
   * Tear down the current connection (if any) and reconnect, preserving the
   * resume cursor, so the server replays from it and nothing is lost.
   */
  protected reconnectNow(reason: string): void {
    if (this.isIntentionallyClosed) return;

    // Cancel a pending backoff timer so we don't end up with two connections.
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    // Abort the old connection. The old stream's catch block will detect
    // that this.abortController has been replaced (by establishConnection)
    // and bail out instead of calling attemptReconnect().
    if (this.abortController) {
      this.abortController.abort();
    }
    this.isConnected_ = false;
    this.disarmWatchdog();

    // Only a user-driven wake (back online, tab visible) may collapse the
    // backoff — those are real signals that the world changed. Internal
    // triggers (resubscribe, gap resync, stale watchdog) must NOT reset it:
    // they fire in exactly the situation the backoff exists to damp, and
    // zeroing it here is what let a resubscribe-per-cycle loop reconnect at
    // full speed indefinitely.
    const isUserWake = reason === "online" || reason === "visible";
    if (isUserWake) {
      this.reconnectAttempts = 0;
    }

    logger.info(`${this.logPrefix} Immediate reconnect`, {
      reason,
      attempts: this.reconnectAttempts,
    });

    // A churning internal trigger goes through the backoff path instead of
    // reconnecting instantly, so a persistent server-side fault degrades to a
    // slow retry rather than a storm.
    if (!isUserWake && this.reconnectAttempts > 0) {
      this.attemptReconnect();
      return;
    }

    // Establish new connection (creates a new AbortController)
    void this.establishConnection();
  }

  protected async establishConnection(): Promise<void> {
    this.setStatus("connecting");
    this.connectAttemptInFlight = true;
    this.abortController = new AbortController();

    // Capture this connection's abort controller so we can detect if it gets
    // replaced by a reconnect before our catch block runs.
    const myAbortController = this.abortController;
    // Whether this attempt got as far as a first event. A refusal happens
    // during stream setup, before anything is sent.
    let delivered = false;

    try {
      for await (const event of this.openStream(myAbortController.signal)) {
        // If our controller was replaced, a new connection took over — bail out.
        if (this.abortController !== myAbortController) {
          logger.info(`${this.logPrefix} Connection superseded, stopping old stream`);
          return;
        }

        // Any event — including a bare heartbeat — proves the socket is alive.
        this.lastEventAt = Date.now();
        delivered = true;

        if (!this.isConnected_) {
          this.isConnected_ = true;
          this.connectAttemptInFlight = false;
          this.connectionOpenedAt = Date.now();
          this.setStatus("connected");
          // Deliberately does NOT reset reconnectAttempts. Reaching
          // "connected" only proves the server accepted the stream, not that
          // it will keep it — a server that ends every stream immediately hits
          // this line on each cycle, and resetting here is what let a
          // server-side fault spin an unthrottled reconnect loop. The reset
          // now happens once the connection has survived
          // SHORT_LIVED_CONNECTION_MS (see the watchdog tick).
          this.armWatchdog();
        }

        this.handleEvent(event);
      }

      // If our controller was replaced, a new connection took over — don't reconnect.
      if (this.abortController !== myAbortController) {
        logger.info(`${this.logPrefix} Connection superseded after stream end`);
        return;
      }

      logger.info(`${this.logPrefix} Stream ended normally`);
      this.isConnected_ = false;
      this.connectAttemptInFlight = false;
      this.disarmWatchdog();
      this.setStatus("disconnected");

      if (!this.isIntentionallyClosed) {
        this.attemptReconnect();
      }
    } catch (error) {
      // If our controller was replaced, a new connection took over — don't reconnect.
      if (this.abortController !== myAbortController) {
        logger.info(`${this.logPrefix} Connection superseded (caught error from old stream)`);
        return;
      }

      this.isConnected_ = false;
      this.connectAttemptInFlight = false;
      this.disarmWatchdog();

      if (this.isIntentionallyClosed || myAbortController.signal.aborted) {
        logger.info(`${this.logPrefix} Stream aborted (intentional)`);
        this.setStatus("disconnected");
        return;
      }

      if (this.handleConnectFailure(error, delivered)) return;

      const errorMessage =
        error instanceof Error ? error.message : String(error);
      logger.error(`${this.logPrefix} Stream error`, {
        error: errorMessage,
      });
      // The connection under the stream died; reads in flight on it will not
      // be answered either. Let the transport replace them now.
      if (isConnectionLevelFailure(error)) {
        pageActivity().noteConnectionLost();
      }
      this.lifecycle.onError?.(errorMessage);
      this.setStatus("error");

      if (!this.isIntentionallyClosed) {
        this.attemptReconnect();
      }
    }
  }

  protected attemptReconnect(): void {
    if (this.isIntentionallyClosed || this.reconnectTimer !== null) {
      return;
    }

    // Deliberately unbounded: "stop retrying" means "the UI is silently stale
    // until the user reloads" — never the right outcome. Backoff is capped at
    // MAX_RECONNECT_DELAY_MS and the online/visible wake triggers
    // short-circuit it, so a server that comes back (a rebuilt dev backend, a
    // finished deploy) is picked up promptly without hammering one that is
    // still down.
    this.reconnectAttempts++;
    const exponent = Math.min(
      this.reconnectAttempts - 1,
      MAX_RECONNECT_EXPONENT,
    );
    const delay = Math.min(
      BASE_RECONNECT_DELAY_MS * Math.pow(2, exponent) + Math.random() * 1000,
      MAX_RECONNECT_DELAY_MS,
    );

    logger.info(`${this.logPrefix} Reconnecting`, {
      attempt: this.reconnectAttempts,
      delayMs: delay,
      ...this.describe(),
    });

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (!this.isIntentionallyClosed) {
        void this.establishConnection();
      }
    }, delay);
  }

  /**
   * Begin streaming: (re)arm the wake handlers and connect — immediately, or
   * after the backoff when recent attempts failed.
   */
  protected open(): void {
    this.isIntentionallyClosed = false;
    this.bindWakeHandlers();
    if (this.reconnectAttempts > 0) {
      this.attemptReconnect();
      return;
    }
    void this.establishConnection();
  }

  /**
   * Take the stream down on purpose: abort the connection, cancel every timer
   * and unbind the wake handlers. The backoff survives, so a stream closed
   * and reopened in a loop still paces itself; `resetBackoff` clears it for a
   * full teardown.
   */
  protected close(resetBackoff: boolean): void {
    this.isIntentionallyClosed = true;
    this.isConnected_ = false;
    this.connectAttemptInFlight = false;
    this.disarmWatchdog();
    this.unbindWakeHandlers();
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.resumeCheckTimer !== null) {
      clearTimeout(this.resumeCheckTimer);
      this.resumeCheckTimer = null;
    }
    if (this.abortController) {
      this.abortController.abort();
      this.abortController = null;
    }
    if (resetBackoff) {
      this.reconnectAttempts = 0;
    }
  }
}
