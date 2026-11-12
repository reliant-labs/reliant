import { useCallback, useEffect, useState } from 'react'
import {
  probeOAuthHelper,
  releaseOAuthHelper,
  requestOAuthHelper,
  type OAuthHelperOutcome,
  type OAuthHelperProblemReason,
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
   * Check again now and restart the poll from a clean slate (backoff reset,
   * stop lifted) — the panel's Retry button. Only acts while `enabled`.
   */
  recheck: () => void
  /**
   * Why the helper is not usable, from the most recent attempt; null while it
   * is available or before the first answer. Lets the panel say what to DO
   * ("install the desktop app") instead of only that it failed.
   */
  problem: OAuthHelperProblemReason | null
  /**
   * The hook stopped asking because the answer will not change by waiting
   * (the account has no machine). `recheck` starts it again.
   */
  stopped: boolean
}

/**
 * The first re-check after a failure, and the unit the delay doubles from. The
 * longest a helper that just came up can go unnoticed is the current delay, so
 * this stays short; exported so tests can drive the poll on a fake clock.
 */
export const OAUTH_HELPER_POLL_INTERVAL_MS = 2000

/**
 * Ceiling on the delay between re-checks. A failing poll used to fire every 2s
 * forever — ~40 refused RPCs in a 5-minute stay on the model step, 250 in the
 * worst session — so consecutive failures back off to here.
 */
export const OAUTH_HELPER_MAX_POLL_INTERVAL_MS = 30_000

/**
 * Consecutive "no machine is connected to your account" refusals after which
 * the hook stops asking. That answer is about the account, not about timing:
 * nothing changes until the user installs and starts something, and Retry
 * covers the moment they do.
 */
export const OAUTH_HELPER_NO_MACHINE_STOP_AFTER = 2

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
  const [problem, setProblem] = useState<OAuthHelperProblemReason | null>(null)
  const [stopped, setStopped] = useState(false)
  // Bumped by `recheck`: restarts the loop below from a clean slate (backoff
  // reset, stop lifted), which is what an explicit Retry means.
  const [attempt, setAttempt] = useState(0)

  const recheck = useCallback(() => {
    if (isElectron) {
      setAvailable(true)
      setLoading(false)
      return
    }
    setStopped(false)
    setAttempt((n) => n + 1)
  }, [isElectron])

  // Ask the daemon to OPEN the port when the panel appears, confirm it is
  // reachable here, then keep checking — in BOTH directions — for as long as
  // the panel is on screen.
  //
  // Requesting rather than merely probing is what removes the second terminal
  // command: a connected daemon on this machine opens the port on demand, so
  // `reliant auth serve` is only needed when the daemon is remote or absent.
  // The request is also the co-location test — see requestOAuthHelper.
  //
  // The poll runs both ways because `available` is a live signal, not a latch:
  // if the user Ctrl-C's `reliant auth serve`, the panel must fall back to the
  // instructions within one tick instead of offering a login that dies with a
  // raw "Failed to fetch". A failed bare probe is not proof of absence — the
  // port may still be opening, or may have closed on an idle timeout — so the
  // tick ASKS again before it reports false, which is also what reopens it.
  //
  // In Electron we never touch the network; while disabled we stay quiet so
  // nothing fires for users who never chose the local-OAuth path.
  useEffect(() => {
    if (isElectron || !enabled) return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let failures = 0
    let noMachineRefusals = 0
    let heldOpen = false

    const apply = (outcome: OAuthHelperOutcome) => {
      if (outcome.ready) {
        failures = 0
        noMachineRefusals = 0
        heldOpen = true
        setAvailable(true)
        setProblem(null)
        return
      }
      heldOpen = false
      failures += 1
      noMachineRefusals =
        outcome.reason === 'no_machine' ? noMachineRefusals + 1 : 0
      setAvailable(false)
      setProblem(outcome.reason)
    }

    const scheduleNext = () => {
      if (cancelled) return
      if (noMachineRefusals >= OAUTH_HELPER_NO_MACHINE_STOP_AFTER) {
        setStopped(true)
        return
      }
      const delay = Math.min(
        OAUTH_HELPER_POLL_INTERVAL_MS * 2 ** Math.max(0, failures - 1),
        OAUTH_HELPER_MAX_POLL_INTERVAL_MS,
      )
      timer = setTimeout(() => void tick(), delay)
    }

    const tick = async () => {
      const health = await probeOAuthHelper()
      if (cancelled) return
      apply(
        health?.ready ? { ready: true, health } : await requestOAuthHelper(),
      )
      if (cancelled) return
      scheduleNext()
    }

    void (async () => {
      setLoading(true)
      const outcome = await requestOAuthHelper()
      if (cancelled) return
      apply(outcome)
      setLoading(false)
      scheduleNext()
    })()

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
      // Release the port when the panel goes away. The daemon's idle timeout
      // is the backstop for a tab that closes without unmounting; this is the
      // fast path that keeps the listener's life equal to the task. Skipped
      // when nothing was opened: with no machine the release is itself a
      // refused RPC.
      if (heldOpen) void releaseOAuthHelper()
    }
  }, [enabled, isElectron, attempt])

  return { available, loading, recheck, problem, stopped }
}
