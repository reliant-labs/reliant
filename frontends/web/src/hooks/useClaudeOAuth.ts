import { useCallback, useEffect, useRef, useState } from 'react'
import {
  runClaudeOAuthFlow,
  type ClaudeOAuthOptions,
  type ClaudeOAuthResult,
} from '@/lib/claude-oauth'

export interface UseClaudeOAuthReturn {
  isRunning: boolean
  lastResult: ClaudeOAuthResult | null
  start: (options?: Omit<ClaudeOAuthOptions, 'signal'>) => Promise<ClaudeOAuthResult>
  cancel: () => void
  reset: () => void
}

export function useClaudeOAuth(): UseClaudeOAuthReturn {
  const [isRunning, setIsRunning] = useState(false)
  const [lastResult, setLastResult] = useState<ClaudeOAuthResult | null>(null)
  const abortControllerRef = useRef<AbortController | null>(null)
  const runIdRef = useRef(0)

  const cancel = useCallback(() => {
    abortControllerRef.current?.abort()
  }, [])

  const start = useCallback(async (options?: Omit<ClaudeOAuthOptions, 'signal'>): Promise<ClaudeOAuthResult> => {
    if (abortControllerRef.current && isRunning) {
      abortControllerRef.current.abort()
    }

    const abortController = new AbortController()
    const runId = ++runIdRef.current
    abortControllerRef.current = abortController
    setIsRunning(true)

    try {
      const result = await runClaudeOAuthFlow({
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
  }, [isRunning])

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