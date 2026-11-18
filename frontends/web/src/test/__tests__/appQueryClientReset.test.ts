// Copyright (c) 2025 Reliant Labs

/**
 * Pins "The app's QueryClient does not outlive a test" in src/test/setup.ts: a
 * query a test leaves retrying on the app's singleton QueryClient is cancelled
 * and dropped before the next test runs, so its retry can never fire there.
 */

import { describe, expect, it, vi } from 'vitest'
import { queryClient } from '@/lib/query-client'

const queryKey = ['appQueryClientReset', 'left-retrying']
const failing = vi.fn(() => Promise.reject(new Error('unavailable')))

describe('a query a test leaves retrying on the app QueryClient', () => {
  it('fails once and is waiting to retry when its test ends', async () => {
    void queryClient.fetchQuery({ queryKey, queryFn: failing, retry: 5, retryDelay: 20 }).catch(() => {})
    await vi.waitFor(() => expect(failing).toHaveBeenCalledTimes(1))
    expect(queryClient.getQueryCache().find({ queryKey })?.state.fetchStatus).toBe('fetching')
  })

  it('is gone before the next test starts, and never retries in it', async () => {
    expect(queryClient.getQueryCache().getAll()).toEqual([])
    const calls = failing.mock.calls.length
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(failing).toHaveBeenCalledTimes(calls)
  })
})
