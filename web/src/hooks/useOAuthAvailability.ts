import { useCallback, useEffect, useState } from 'react'
import { OAUTH_LOCAL_SERVER_URL } from '@/lib/oauth-local'

export interface UseOAuthAvailabilityReturn {
  /** Whether the localhost OAuth helper is reachable (or Electron, which always has it). */
  available: boolean
  /** True while the initial health check is in flight (web mode only). */
  loading: boolean
  /** Re-check availability on demand. Surfaces `loading` while in flight. */
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
 * - **Electron**: always available immediately (daemon handles it).
 * - **Web**: pings `http://127.0.0.1:19284/health` to see if `reliant auth serve` is running.
 *   While unavailable, polls every 2s so the UI flips automatically when the user starts
 *   the helper in their terminal.
 */
export function useOAuthAvailability(): UseOAuthAvailabilityReturn {
  const isElectron = !!window.electronAPI

  const [available, setAvailable] = useState(isElectron)
  const [loading, setLoading] = useState(!isElectron)

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

  // Initial check on mount.
  useEffect(() => {
    check()
  }, [check])

  // Poll silently while unavailable so the UI flips automatically once the user
  // starts `reliant auth serve`. Stops on success or unmount.
  useEffect(() => {
    if (isElectron || available) return
    const id = setInterval(async () => {
      const ok = await pingHealth()
      if (ok) setAvailable(true)
    }, POLL_INTERVAL_MS)
    return () => clearInterval(id)
  }, [available, isElectron])

  return { available, loading, recheck: check }
}
