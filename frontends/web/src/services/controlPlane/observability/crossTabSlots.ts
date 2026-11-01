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
 * whole browser holds at most that many reads at once. Without the Web Locks
 * API (an old browser, jsdom) the gate is a pass-through and the page queue
 * alone applies, which is what this was before.
 */

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

/** No coordination: the task runs at once. */
export const passThroughGate: SlotGate = { run: (task) => task() };

/** This browser's gate: Web Locks when the platform has them, otherwise pass-through. */
export function browserSlotGate(slots: number): SlotGate {
  const locks = typeof navigator !== "undefined" ? (navigator as Navigator & { locks?: LockRequester }).locks : undefined;
  return locks && typeof locks.request === "function" ? lockSlotGate(locks, slots) : passThroughGate;
}
