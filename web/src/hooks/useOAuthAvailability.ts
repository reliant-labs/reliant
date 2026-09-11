import { useCallback, useEffect, useState } from 'react'
import {
  probeOAuthHelper,
  releaseOAuthHelper,
  requestOAuthHelper,
} from '@/lib/oauth-local'

export interface UseOAuthAvailabilityOptions {
  /**
   * Opt in to probing the localhost OAuth helper. Defaults to `false` so the
   * 2s `http://127.0.0.1:19284/health` probe NEVER fires on mount. Probing from
   * a deployed public origin (app.reliantlabs.io) triggers Chrome's "Local
   * Network Access" permission prompt for users who never opted into local
   * OAuth — so callers must flip this to `true` only while the local-OAuth UI
   * (OAuthHelperPanel) is actually on screen. Ignored in Electron, where the
   * helper is always available and no network probe is ever made.
   */
  enabled?: boolean
}

export interface UseOAuthAvailabilityReturn {
  /** Whether the localhost OAuth helper is reachable (or Electron, which always has it). */
  available: boolean
  /** True while a health check is in flight (web mode, while enabled). */
  loading: boolean
  /**
   * Force a one-off availability check on demand (e.g. the panel's Retry
   * button). Works regardless of `enabled` since it's an explicit user action.
   */
  recheck: () => void
}

const POLL_INTERVAL_MS = 2000
const HEALTH_TIMEOUT_MS = 2000

// Identity-checked: a 200 alone proves only that SOMETHING holds port 19284,
// and offering an OAuth flow to an unrelated dev server fails silently. See
// probeOAuthHelper.
async function pingHealth(): Promise<boolean> {
  const health = await probeOAuthHelper(HEALTH_TIMEOUT_MS)
  return !!health?.ready
}

/**
 * Determines whether OAuth flows (Claude/Codex) can run.
 *
 * - **Electron**: always available immediately (daemon handles it); no network
 *   probe is ever made.
 * - **Web**: probes `http://127.0.0.1:19284/health` and requires the response to
 *   identify itself as reliant — served either by `reliant daemon start` running
 *   on THIS machine or by a standalone `reliant auth serve`. Probing only once
 *   `enabled` is `true`: callers set it while the local-OAuth UI is on screen, so
 *   the probe (and Chrome's Local Network Access prompt) never fires for users
 *   who never chose the local-OAuth path. While enabled + unavailable, polls
 *   every 2s so the UI flips automatically when the helper appears.
 *
 * The probe doubles as the CO-LOCATION test. Whether the daemon is on the same
 * machine as the browser cannot be answered from the server side — it knows its
 * hostname and instance id, but not which machine rendered this page, and behind
 * NAT many machines share one address. Reaching it on localhost is the proof.
 */
export function useOAuthAvailability(
  { enabled = false }: UseOAuthAvailabilityOptions = {},
): UseOAuthAvailabilityReturn {
  const isElectron = !!window.electronAPI

  const [available, setAvailable] = useState(isElectron)
  // Not "loading" until we actually probe (web + enabled).
  const [loading, setLoading] = useState(false)

  const check = useCallback(async () => {
    if (isElectron) {
      setAvailable(true)
      setLoading(false)
      return
    }
    setLoading(true)
    // An explicit retry ASKS again: the user may have started a daemon since
    // the last attempt, and re-probing alone would never open a port.
    const health = await requestOAuthHelper()
    setAvailable(!!health?.ready)
    setLoading(false)
  }, [isElectron])

  // Ask the daemon to OPEN the port when the panel appears, then confirm it is
  // reachable here.
  //
  // Requesting rather than merely probing is what removes the second terminal
  // command: a connected daemon on this machine opens the port on demand, so
  // `reliant auth serve` is only needed when the daemon is remote or absent.
  // The request is also the co-location test — see requestOAuthHelper.
  //
  // In Electron we never touch the network; while disabled we stay quiet so
  // nothing fires for users who never chose the local-OAuth path.
  useEffect(() => {
    if (isElectron || !enabled) return
    let cancelled = false
    void (async () => {
      setLoading(true)
      const health = await requestOAuthHelper()
      if (cancelled) return
      setAvailable(!!health?.ready)
      setLoading(false)
    })()
    return () => {
      cancelled = true
      // Release the port when the panel goes away. The daemon's idle timeout
      // is the backstop for a tab that closes without unmounting; this is the
      // fast path that keeps the listener's life equal to the task.
      void releaseOAuthHelper()
    }
  }, [enabled, isElectron])

  // Poll while the panel is on screen, in BOTH directions.
  //
  // This used to stop once the helper answered (`available` was in the guard
  // and the interval only ever set it to true), which made `available` a latch
  // rather than a live signal. If the user then Ctrl-C'd `reliant auth serve`,
  // the panel kept offering "Login with Codex" and the click died with a raw
  // "Failed to fetch" — no explanation and no route back to the instructions
  // that would fix it.
  //
  // `available` is deliberately NOT a dependency: it would tear down and
  // recreate the interval on every flip, and the poll must run at a steady
  // cadence regardless of the current state.
  useEffect(() => {
    if (isElectron || !enabled) return
    const id = setInterval(async () => {
      setAvailable(await pingHealth())
    }, POLL_INTERVAL_MS)
    return () => clearInterval(id)
  }, [isElectron, enabled])

  return { available, loading, recheck: check }
}
