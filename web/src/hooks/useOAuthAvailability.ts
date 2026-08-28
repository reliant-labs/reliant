import { useCallback, useEffect, useState } from 'react'
import { OAUTH_LOCAL_SERVER_URL } from '@/lib/oauth-local'

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

async function pingHealth(): Promise<boolean> {
  try {
    const resp = await fetch(`${OAUTH_LOCAL_SERVER_URL}/health`, {
      signal: AbortSignal.timeout(HEALTH_TIMEOUT_MS),
    })
    return resp.ok
  } catch {
    return false
  }
}

/**
 * Determines whether OAuth flows (Claude/Codex) can run.
 *
 * - **Electron**: always available immediately (daemon handles it); no network
 *   probe is ever made.
 * - **Web**: pings `http://127.0.0.1:19284/health` to see if `reliant auth serve`
 *   is running — but ONLY once `enabled` is `true`. Callers set `enabled` while
 *   the local-OAuth UI is on screen, so the probe (and Chrome's Local Network
 *   Access prompt) never fires for users who never chose the local-OAuth path.
 *   While enabled + unavailable, polls every 2s so the UI flips automatically
 *   when the user starts the helper in their terminal.
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
    const ok = await pingHealth()
    setAvailable(ok)
    setLoading(false)
  }, [isElectron])

  // Kick off an immediate check when probing turns on. In Electron we never
  // touch the network; while disabled we stay quiet so no health probe fires.
  useEffect(() => {
    if (isElectron || !enabled) return
    let cancelled = false
    void (async () => {
      setLoading(true)
      const ok = await pingHealth()
      if (cancelled) return
      setAvailable(ok)
      setLoading(false)
    })()
    return () => {
      cancelled = true
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
