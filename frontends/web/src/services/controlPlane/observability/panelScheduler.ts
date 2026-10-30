// Copyright (c) 2025 Reliant Labs

/**
 * The console's ONE admission queue for dashboard panel reads.
 *
 * control-plane lets a console user run SIX observability reads at once per org
 * (obsratelimit.DefaultMaxInFlight), waits two seconds for a slot, then refuses
 * with `in_flight_limit`. A dashboard of twenty panels used to fire all twenty
 * the moment it mounted, so most were refused. This queue starts at most
 * PANEL_CONCURRENCY at a time, first come first served, and leaves the rest of
 * the server's cap for the logs, traces and metrics views and other tabs.
 *
 * One scheduler per QueryClient, not per dashboard: the cap is per user, so the
 * dashboard, the home strip and a drawer preview must share it.
 *
 * A retry after a refusal goes back to the END of the queue (it is a fresh
 * `run`), so refused panels never jump ahead of ones still waiting their first
 * turn.
 */

import type { QueryClient } from "@tanstack/react-query";

/** Below the server's cap of 6: the difference is headroom for every other view. */
export const PANEL_CONCURRENCY = 3;

interface Entry {
  task: (signal: AbortSignal) => Promise<unknown>;
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
  signal: AbortSignal | undefined;
  onAbort: (() => void) | undefined;
}

export class PanelScheduler {
  private readonly queue: Entry[] = [];
  private running = 0;
  private readonly maxConcurrent: number;

  constructor(maxConcurrent: number = PANEL_CONCURRENCY) {
    this.maxConcurrent = Math.max(1, maxConcurrent);
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
    let result: Promise<unknown>;
    try {
      result = entry.task(signal ?? new AbortController().signal);
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

const schedulers = new WeakMap<QueryClient, PanelScheduler>();

/** The scheduler of a query client, made on first use. */
export function panelSchedulerFor(client: QueryClient): PanelScheduler {
  let scheduler = schedulers.get(client);
  if (!scheduler) {
    scheduler = new PanelScheduler();
    schedulers.set(client, scheduler);
  }
  return scheduler;
}
