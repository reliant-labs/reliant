// Copyright (c) 2025 Reliant Labs

/**
 * The console's ONE admission queue for observability reads.
 *
 * control-plane lets a console user run SIX observability reads at once per org
 * (obsratelimit.DefaultMaxInFlight), waits two seconds for a slot, then refuses
 * with `in_flight_limit`. Every read that takes one of those slots (see
 * obsClient's READ_SLOT_RPCS) goes through this queue, whichever view asked: a
 * dashboard of twenty panels, the metrics explorer, logs, traces, the service
 * graph. It starts at most OBS_READ_CONCURRENCY at a time, first come first
 * served, and leaves the rest of the server's cap for other tabs and devices.
 *
 * ONE scheduler per page, not per view or per QueryClient: the cap is per
 * user, so every consumer must draw on the same budget or they starve each
 * other.
 *
 * A retry after a refusal goes back to the END of the queue (it is a fresh
 * `run`), so refused reads never jump ahead of ones still waiting their first
 * turn.
 *
 * ── ACROSS TABS ─────────────────────────────────────────────────────────────
 *
 * The cap is per user, and a user has more than one page: nine console tabs on
 * 2026-11-01 were nine queues of four, and the server refused the overflow.
 * Two rules make the queue the browser's rather than the page's:
 *
 *  1. A read that starts takes one of the browser's shared slots first
 *     (crossTabSlots: OBS_READ_CONCURRENCY Web Locks every same-origin tab
 *     draws on), so the browser as a whole stays under the server's cap.
 *     Without Web Locks the limit is per tab instead (crossTabSlots
 *     perTabGate, reported once): each tab holds its own cap, no more.
 *  2. A hidden page starts nothing new. The tab being read goes first, rather
 *     than queueing behind every dashboard opened in a background tab; a
 *     hidden page's reads start when it is shown. Reads already running finish.
 */

import { browserSlotGate, perTabGate, type SlotGate } from "./crossTabSlots";

/** control-plane's per-user, per-org concurrent-read cap (obsratelimit.DefaultMaxInFlight). */
export const SERVER_READ_CAP = 6;

/** At or below the server's cap, for the whole browser (crossTabSlots; per tab without Web Locks); the difference is headroom for the user's other devices. */
export const OBS_READ_CONCURRENCY = 4;

interface Entry {
  task: (signal: AbortSignal) => Promise<unknown>;
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
  signal: AbortSignal | undefined;
  onAbort: (() => void) | undefined;
}

/** Whether the page is hidden, and a way to hear when that changes (document visibility, injectable for tests). */
export interface PageVisibility {
  hidden(): boolean;
  onChange(listener: () => void): void;
}

/** The document's visibility; a context with no document (a worker, SSR) is always visible. */
export const documentVisibility: PageVisibility = {
  hidden: () => typeof document !== "undefined" && document.visibilityState === "hidden",
  onChange: (listener) => {
    if (typeof document !== "undefined") document.addEventListener("visibilitychange", listener);
  },
};

export interface ReadSchedulerOptions {
  /** The browser-wide slots a starting read takes (crossTabSlots). Default: perTabGate, this scheduler's own cap alone. */
  gate?: SlotGate;
  /** Default: always visible. */
  visibility?: PageVisibility;
}

export class ReadScheduler {
  private readonly queue: Entry[] = [];
  private running = 0;
  private readonly maxConcurrent: number;
  private readonly gate: SlotGate;
  private readonly visibility: PageVisibility | undefined;

  constructor(maxConcurrent: number = OBS_READ_CONCURRENCY, options: ReadSchedulerOptions = {}) {
    this.maxConcurrent = Math.max(1, maxConcurrent);
    this.gate = options.gate ?? perTabGate;
    this.visibility = options.visibility;
    // Shown again: start what waited while the page was hidden.
    this.visibility?.onChange(() => this.pump());
  }

  get stats(): { running: number; queued: number } {
    return { running: this.running, queued: this.queue.length };
  }

  /**
   * Runs `task` when a slot is free. `signal` cancels it: a queued task leaves
   * the queue without ever starting, a running one is aborted (it receives the
   * signal) and gives its slot back at once. Either way the returned promise
   * rejects with the signal's reason.
   */
  run<T>(task: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      if (signal?.aborted) {
        reject(signal.reason);
        return;
      }
      const entry: Entry = { task, resolve: resolve as (value: unknown) => void, reject, signal, onAbort: undefined };
      if (signal) {
        entry.onAbort = () => {
          const at = this.queue.indexOf(entry);
          if (at >= 0) {
            this.queue.splice(at, 1);
            reject(signal.reason);
          }
        };
        signal.addEventListener("abort", entry.onAbort, { once: true });
      }
      this.queue.push(entry);
      this.pump();
    });
  }

  private pump(): void {
    if (this.visibility?.hidden()) return;
    while (this.running < this.maxConcurrent && this.queue.length > 0) {
      this.start(this.queue.shift()!);
    }
  }

  private start(entry: Entry): void {
    this.running++;
    const { signal } = entry;
    let released = false;
    const release = (deferPump = false) => {
      if (released) return;
      released = true;
      this.running--;
      if (signal && entry.onAbort) signal.removeEventListener("abort", entry.onAbort);
      // Leaving a page aborts its reads one after another: the next queued read must not start in
      // the gap before its own abort lands, so an abort frees the slot now and pumps a microtask later.
      if (deferPump) queueMicrotask(() => this.pump());
      else this.pump();
    };
    // A running task is cancelled by its own signal; the slot is free as soon as
    // the caller stops waiting, because the aborted request frees the server's.
    const onRunningAbort = () => {
      entry.reject(signal!.reason);
      release(true);
    };
    if (signal) {
      if (entry.onAbort) signal.removeEventListener("abort", entry.onAbort);
      signal.addEventListener("abort", onRunningAbort, { once: true });
    }
    const finish = () => {
      signal?.removeEventListener("abort", onRunningAbort);
      release();
    };
    const taskSignal = signal ?? new AbortController().signal;
    let result: Promise<unknown>;
    try {
      // The browser-wide slot is taken here, after the page admitted the read:
      // waiting for one holds this page's turn, so the page never has more
      // than its own cap waiting on the browser either.
      result = this.gate.run(() => entry.task(taskSignal), signal);
    } catch (error) {
      entry.reject(error);
      finish();
      return;
    }
    result.then(
      (value) => {
        entry.resolve(value);
        finish();
      },
      (error: unknown) => {
        entry.reject(error);
        finish();
      }
    );
  }
}

let shared: ReadScheduler | null = null;

/** The page's scheduler, made on first use: browser-wide slots, and nothing new starts while the page is hidden. */
export function obsReadScheduler(): ReadScheduler {
  shared ??= new ReadScheduler(OBS_READ_CONCURRENCY, { gate: browserSlotGate(OBS_READ_CONCURRENCY), visibility: documentVisibility });
  return shared;
}

/** Tests only: a fresh page. */
export function resetObsReadScheduler(): void {
  shared = null;
}
