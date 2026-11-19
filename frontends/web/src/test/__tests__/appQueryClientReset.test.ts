// Copyright (c) 2025 Reliant Labs

/**
 * Pins "The app's QueryClient does not outlive a test" in src/test/setup.ts: a
 * query a test leaves retrying on the app's singleton QueryClient is cancelled
 * and dropped before the next test runs, so its retry can never fire there.
 *
 * No duration is waited on. The retry is scheduled a minute out, so it cannot
 * fire during the run either way; what the next test checks is that the fetch
 * was CANCELLED. A cancelled retryer never calls its queryFn again, so that
 * is the guarantee, and it is already settled when the next test starts.
 */

import { CancelledError } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { queryClient } from '@/lib/query-client'

const queryKey = ['appQueryClientReset', 'left-retrying']
const failing = vi.fn(() => Promise.reject(new Error('unavailable')))
const PENDING = Symbol('pending')
let outcome: Promise<unknown> = Promise.resolve(PENDING)

describe('a query a test leaves retrying on the app QueryClient', () => {
  it('fails once and is waiting to retry when its test ends', async () => {
    outcome = queryClient.fetchQuery({ queryKey, queryFn: failing, retry: 5, retryDelay: 60_000 }).then(
      () => 'resolved',
      (error: unknown) => error,
    )
    await vi.waitFor(() => expect(queryClient.getQueryCache().find({ queryKey })?.state.fetchFailureCount).toBe(1))
    expect(queryClient.getQueryCache().find({ queryKey })?.state.fetchStatus).toBe('fetching')
    expect(failing).toHaveBeenCalledTimes(1)
  })

  it('is gone before the next test starts, its retry cancelled', async () => {
    expect(queryClient.getQueryCache().getAll()).toEqual([])
    // Both are settled if the reset ran, and the first settled promise in the
    // list wins: an outcome still pending means the retry is still scheduled.
    const settled = await Promise.race([outcome, Promise.resolve(PENDING)])
    expect(settled).toBeInstanceOf(CancelledError)
    expect(failing).toHaveBeenCalledTimes(1)
  })
})
