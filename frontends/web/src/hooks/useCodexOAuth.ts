import { useCallback, useEffect, useRef, useState } from 'react'
import {
  runCodexOAuthFlow,
  type CodexOAuthOptions,
  type CodexOAuthResult,
} from '@/lib/codex-oauth'

export interface UseCodexOAuthReturn {
  isRunning: boolean
  lastResult: CodexOAuthResult | null
  start: (options?: Omit<CodexOAuthOptions, 'signal'>) => Promise<CodexOAuthResult>
  cancel: () => void
  reset: () => void
}

export function useCodexOAuth(): UseCodexOAuthReturn {
  const [isRunning, setIsRunning] = useState(false)
  const [lastResult, setLastResult] = useState<CodexOAuthResult | null>(null)
  const abortControllerRef = useRef<AbortController | null>(null)
  const runIdRef = useRef(0)

  const cancel = useCallback(() => {
    abortControllerRef.current?.abort()
  }, [])

  const start = useCallback(async (options?: Omit<CodexOAuthOptions, 'signal'>): Promise<CodexOAuthResult> => {
    if (abortControllerRef.current && isRunning) {
      abortControllerRef.current.abort()
    }

    const abortController = new AbortController()
    const runId = ++runIdRef.current
    abortControllerRef.current = abortController
    setIsRunning(true)

    try {
      const result = await runCodexOAuthFlow({
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