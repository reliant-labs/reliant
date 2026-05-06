import { useCallback, useEffect, useState } from 'react'
import { OAUTH_LOCAL_SERVER_URL } from '@/lib/oauth-local'

export interface UseOAuthAvailabilityReturn {
  /** Whether the localhost OAuth helper is reachable (or Electron, which always has it). */
  available: boolean
  /** True while the initial health check is in flight (web mode only). */
  loading: boolean
  /** Re-check availability on demand. */
  recheck: () => void
}

/**
 * Determines whether OAuth flows (Claude/Codex) can run.
 *
 * - **Electron**: always available immediately (daemon handles it).
 * - **Web**: pings `http://127.0.0.1:19284/health` to see if `reliant auth serve` is running.
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
    try {
      const resp = await fetch(`${OAUTH_LOCAL_SERVER_URL}/health`, {
        signal: AbortSignal.timeout(2000),
      })
      setAvailable(resp.ok)
    } catch {
      setAvailable(false)
    } finally {
      setLoading(false)
    }
  }, [isElectron])

  useEffect(() => {
    check()
  }, [check])

  return { available, loading, recheck: check }
}