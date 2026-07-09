import { useCallback, useEffect, useRef, useState } from 'react'
import {
  runCopilotDeviceFlow,
  type CopilotOAuthResult,
} from '@/lib/copilot-oauth'

/**
 * Phases of the Copilot device-authorization flow:
 * - `idle`          — nothing started yet.
 * - `starting`      — requesting a device code from the backend.
 * - `awaiting_user` — device code issued; waiting for the user to authorize.
 * - `polling`       — actively polling GitHub for completion.
 * - `success`       — authorized; credential stored.
 * - `error`         — expired / denied / cancelled / failed.
 *
 * `awaiting_user` and `polling` both display the user code + a spinner; the
 * distinction is informational (the loop toggles between them).
 */
export type CopilotOAuthPhase =
  | 'idle'
  | 'starting'
  | 'awaiting_user'
  | 'polling'
  | 'success'
  | 'error'

export interface UseCopilotOAuthReturn {
  phase: CopilotOAuthPhase
  /** True while the flow is in flight (starting / awaiting_user / polling). */
  isActive: boolean
  /** Short user-facing code to display, once issued. */
  userCode: string | null
  /** URL the user opens to enter the code (github.com/login/device). */
  verificationUri: string | null
  /** Success or error message, set when the flow settles. */
  message: string | null
  lastResult: CopilotOAuthResult | null
  start: () => Promise<CopilotOAuthResult>
  cancel: () => void
  reset: () => void
}

export function useCopilotOAuth(): UseCopilotOAuthReturn {
  const [phase, setPhase] = useState<CopilotOAuthPhase>('idle')
  const [userCode, setUserCode] = useState<string | null>(null)
  const [verificationUri, setVerificationUri] = useState<string | null>(null)
  const [message, setMessage] = useState<string | null>(null)
  const [lastResult, setLastResult] = useState<CopilotOAuthResult | null>(null)

  const abortControllerRef = useRef<AbortController | null>(null)
  const runIdRef = useRef(0)

  const cancel = useCallback(() => {
    abortControllerRef.current?.abort()
  }, [])

  const reset = useCallback(() => {
    abortControllerRef.current?.abort()
    setPhase('idle')
    setUserCode(null)
    setVerificationUri(null)
    setMessage(null)
    setLastResult(null)
  }, [])

  const start = useCallback(
    async (): Promise<CopilotOAuthResult> => {
      // Cancel any in-flight run before starting a new one.
      abortControllerRef.current?.abort()

      const abortController = new AbortController()
      const runId = ++runIdRef.current
      abortControllerRef.current = abortController

      const isCurrent = () => runId === runIdRef.current

      setUserCode(null)
      setVerificationUri(null)
      setMessage(null)
      setLastResult(null)
      setPhase('starting')

      try {
        const result = await runCopilotDeviceFlow({
          signal: abortController.signal,
          onDeviceCode: (info) => {
            if (!isCurrent()) return
            setUserCode(info.userCode)
            setVerificationUri(info.verificationUri)
            setPhase('awaiting_user')
          },
          onPolling: () => {
            if (!isCurrent()) return
            setPhase('polling')
          },
        })

        if (isCurrent()) {
          setLastResult(result)
          setMessage(result.message)
          setPhase(result.ok ? 'success' : 'error')
        }
        return result
      } finally {
        if (abortControllerRef.current === abortController) {
          abortControllerRef.current = null
        }
      }
    },
    [],
  )

  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort()
    }
  }, [])

  return {
    phase,
    isActive: phase === 'starting' || phase === 'awaiting_user' || phase === 'polling',
    userCode,
    verificationUri,
    message,
    lastResult,
    start,
    cancel,
    reset,
  }
}
