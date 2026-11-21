/**
 * Is this build below the minimum the reliant API publishes for it?
 *
 * The server refuses a build below its minimum (`upgrade_required`, handled in
 * api/transport.ts), but only on the next call, and only once it enforces. It
 * also publishes the minimums on SystemService.Version (`client_minimums`,
 * public, no sign-in), so the app asks at start, when the tab regains focus or
 * becomes visible, and on a slow interval, and opens the forced upgrade before
 * the user hits a refused call. Same cadence as the new-build poll
 * (lib/deployedBuild.ts).
 *
 * Every failure to read the minimums means "no news": an older server that
 * publishes none, a network error, a server that is not configured yet.
 */

import type { ClientMinimum, ClientMinimums } from "../gen/reliant/v1/system_pb";
import { type ClientIdentity, isBelowMinimum, minimumFor } from "./clientVersion";
import { MIN_CHECK_GAP_MS, POLL_INTERVAL_MS } from "./deployedBuild";

export interface ClientMinimumWatchOptions {
  identity: ClientIdentity;
  /** Reads the served minimums; resolves undefined when the server sends none. */
  fetchMinimums: () => Promise<ClientMinimums | undefined>;
  /** Called once, the first time this build is found below its minimum. */
  onBelowMinimum: (minimum: ClientMinimum) => void;
  intervalMs?: number;
  minGapMs?: number;
  now?: () => number;
}

/** Starts watching; returns the function that stops it. */
export function watchClientMinimum(opts: ClientMinimumWatchOptions): () => void {
  const {
    identity,
    fetchMinimums,
    onBelowMinimum,
    intervalMs = POLL_INTERVAL_MS,
    minGapMs = MIN_CHECK_GAP_MS,
    now = Date.now,
  } = opts;

  let stopped = false;
  let inFlight = false;
  let lastCheck = Number.NEGATIVE_INFINITY;

  const check = async () => {
    if (stopped || inFlight || document.visibilityState === "hidden") return;
    if (now() - lastCheck < minGapMs) return;
    lastCheck = now();
    inFlight = true;
    try {
      const minimum = minimumFor(identity.kind, await fetchMinimums().catch(() => undefined));
      if (stopped || !minimum || !isBelowMinimum(identity.version, minimum.minVersion)) return;
      stop();
      onBelowMinimum(minimum);
    } finally {
      inFlight = false;
    }
  };

  const onWake = () => void check();
  window.addEventListener("focus", onWake);
  document.addEventListener("visibilitychange", onWake);
  const timer = setInterval(onWake, intervalMs);
  const stop = () => {
    stopped = true;
    window.removeEventListener("focus", onWake);
    document.removeEventListener("visibilitychange", onWake);
    clearInterval(timer);
  };
  void check();

  return stop;
}
