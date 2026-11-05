/**
 * `tourEvents`: a thin typed wrapper over the app's event bus (`@/lib/events`)
 * for the signals journey steps wait on (journeys/signals.ts):
 *
 *   tourEvents.emit("workflow:nodeAdded", { nodeId, type: "approval", anchor: "splice" });
 *
 * Why the wrapper: it narrows the bus to tour signals, initialises the bus on
 * first use (a shell that never mounted Providers — the forge shell's public
 * demo — has none yet, and `getEventBus()` would throw), and isolates
 * listeners, so a broken step predicate can never break the action that
 * emitted. Call sites emit unconditionally; they never ask whether a tour
 * is running.
 */

import { initEventBus } from "../../lib/events";
import type { TourSignalName, TourSignals } from "./journeys/signals";

export type TourEventMap = TourSignals;
export type TourEventName = TourSignalName;

export const tourEvents = {
  emit<K extends TourEventName>(name: K, payload: TourEventMap[K]): void {
    (initEventBus().emit as (event: K, payload: TourEventMap[K]) => void)(name, payload);
  },

  /** Subscribe; returns the unsubscribe. */
  on<K extends TourEventName>(name: K, listener: (payload: TourEventMap[K]) => void): () => void {
    return initEventBus().on(name, ((payload: TourEventMap[K]) => {
      try {
        listener(payload);
      } catch {
        // A broken step predicate must never break the action that emitted.
      }
    }) as never);
  },
};
