// Copyright (c) 2025 Reliant Labs

/**
 * The read slots EVERY tab of this browser shares.
 *
 * control-plane caps a console user at six observability reads in flight
 * (obsratelimit.DefaultMaxInFlight, per user and org) and refuses the seventh
 * with `in_flight_limit` after two seconds. readScheduler keeps one page under
 * that cap, but a page is not a user: on 2026-11-01 the owner had nine console
 * tabs open — six dashboards opened at once — each running its own four reads,
 * and admin-server refused 28 of them in two minutes. Nothing bypassed a page's
 * queue; the other tabs were simply other queues.
 *
 * So a read takes one of OBS_READ_CONCURRENCY named Web Locks before it goes
 * out. A lock is held by one context at a time across every same-origin tab and
 * window, and is released when the read settles or its tab goes away, so the
 * whole browser holds at most that many reads at once.
 *
 * ── WITHOUT WEB LOCKS: A PER-TAB LIMIT, NOT A BROWSER-WIDE ONE ───────────────
 *
 * Web Locks ship in every browser the console supports (Chrome/Edge 69+,
 * Firefox 96+, Safari 15.4+, and Electron), so this is the old-browser and
 * jsdom case. There the limit is explicitly per tab (perTabGate): each tab's
 * own ReadScheduler holds it to OBS_READ_CONCURRENCY, and N tabs can have N
 * times that in flight. Nothing claims otherwise: the fallback is logged once,
 * and a read the server refuses for it (`in_flight_limit`) is re-queued after
 * the server's Retry-After rather than surfaced (errors.ts obsRetry), which is
 * how every read behaved before the browser-wide limit existed. A storage or
 * BroadcastChannel lease would only add expiry and recovery logic to serve
 * browsers the console does not otherwise support.
 */

import { logger } from "@/lib/logger";

/** The lock names: `<prefix><slot>`. Versioned so a future change of meaning cannot share slots with an older tab. */
export const OBS_READ_LOCK_PREFIX = "reliant.obs-read-slot.v1.";

/** What `run` needs of navigator.locks (LockManager), so tests can pass a fake. */
export interface LockRequester {
  request<T>(name: string, options: { ifAvailable?: boolean; signal?: AbortSignal }, callback: (lock: unknown) => T | Promise<T>): Promise<T>;
}

export interface SlotGate {
  /** Runs `task` while holding one shared slot; `signal` gives up the wait (rejecting with its reason). */
  run<T>(task: () => Promise<T>, signal?: AbortSignal): Promise<T>;
}

const NOT_FREE: unique symbol = Symbol("not-free");

/** The gate over `slots` named locks of `locks`. */
export function lockSlotGate(locks: LockRequester, slots: number): SlotGate {
  const count = Math.max(1, slots);
  let next = 0;
  return {
    async run<T>(task: () => Promise<T>, signal?: AbortSignal): Promise<T> {
      if (signal?.aborted) throw signal.reason;
      // A slot no tab holds, if there is one. Probing from a rotating start
      // spreads the waits below across every slot instead of piling on slot 0.
      const start = next++ % count;
      for (let i = 0; i < count; i++) {
        const name = OBS_READ_LOCK_PREFIX + ((start + i) % count);
        const outcome = await locks.request<T | typeof NOT_FREE>(name, { ifAvailable: true }, (lock) => (lock ? task() : NOT_FREE));
        if (outcome !== NOT_FREE) return outcome;
      }
      // Every slot is held: wait in line for one. A lock's waiters are served
      // in order, so a read never starves behind later ones on its slot.
      try {
        return await locks.request<T>(OBS_READ_LOCK_PREFIX + start, { signal }, () => {
          if (signal?.aborted) throw signal.reason;
          return task();
        });
      } catch (error) {
        // An abort while waiting surfaces as the caller's own reason, not the lock manager's AbortError.
        if (signal?.aborted) throw signal.reason;
        throw error;
      }
    },
  };
}

/**
 * The per-tab limit: no cross-tab slot is taken, so the task runs as soon as
 * this tab's own ReadScheduler admits it. That scheduler's cap is the only
 * limit, and it holds per tab, not for the browser.
 */
export const perTabGate: SlotGate = { run: (task) => task() };

/** What browserSlotGate reads of the platform; tests pass their own. */
export interface SlotPlatform {
  /** navigator.locks, when the platform has the Web Locks API. */
  locks: LockRequester | undefined;
  /** Where the per-tab fallback is reported (once per page). */
  warn: (message: string) => void;
}

const navigatorPlatform = (): SlotPlatform => ({
  locks: typeof navigator !== "undefined" ? (navigator as Navigator & { locks?: LockRequester }).locks : undefined,
  warn: (message) => logger.warn(message),
});

let reportedPerTab = false;

/**
 * This browser's gate: browser-wide slots over Web Locks when the platform has
 * them, otherwise the per-tab limit alone, reported once.
 */
export function browserSlotGate(slots: number, platform: SlotPlatform = navigatorPlatform()): SlotGate {
  const { locks } = platform;
  if (locks && typeof locks.request === "function") return lockSlotGate(locks, slots);
  if (!reportedPerTab) {
    reportedPerTab = true;
    platform.warn(
      `[obs] This browser has no Web Locks API: observability reads are limited per tab (${slots} at a time), not across tabs. ` +
        "Several open console tabs can exceed the server's in-flight cap; refused reads are retried after the server's Retry-After."
    );
  }
  return perTabGate;
}

/** Forget that the per-tab fallback was reported. */
export function resetPerTabReportForTests(): void {
  reportedPerTab = false;
}
