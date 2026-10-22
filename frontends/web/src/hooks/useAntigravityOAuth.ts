import { useCallback, useEffect, useRef, useState } from 'react'
import {
  runAntigravityOAuthFlow,
  type AntigravityOAuthOptions,
  type AntigravityOAuthResult,
} from '@/lib/antigravity-oauth'

export interface UseAntigravityOAuthReturn {
  isRunning: boolean
  lastResult: AntigravityOAuthResult | null
  start: (options?: Omit<AntigravityOAuthOptions, 'signal'>) => Promise<AntigravityOAuthResult>
  cancel: () => void
  reset: () => void
}

export function useAntigravityOAuth(): UseAntigravityOAuthReturn {
  const [isRunning, setIsRunning] = useState(false)
  const [lastResult, setLastResult] = useState<AntigravityOAuthResult | null>(null)
  const abortControllerRef = useRef<AbortController | null>(null)
  const runIdRef = useRef(0)

  const cancel = useCallback(() => {
    abortControllerRef.current?.abort()
  }, [])

  const start = useCallback(
    async (options?: Omit<AntigravityOAuthOptions, 'signal'>): Promise<AntigravityOAuthResult> => {
      if (abortControllerRef.current && isRunning) {
        abortControllerRef.current.abort()
      }

      const abortController = new AbortController()
      const runId = ++runIdRef.current
      abortControllerRef.current = abortController
      setIsRunning(true)

      try {
        const result = await runAntigravityOAuthFlow({
          ...options,
          signal: abortController.signal,
        })

        setLastResult(result)
        return result
      } finally {
        if (abortControllerRef.current === abortController) {
          abortControllerRef.current = null
        }
        if (runId === runIdRef.current) {
          setIsRunning(false)
        }
      }
    },
    [isRunning],
  )

  const reset = useCallback(() => {
    setLastResult(null)
  }, [])

  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort()
    }
  }, [])

  return {
    isRunning,
    lastResult,
    start,
    cancel,
    reset,
  }
}
