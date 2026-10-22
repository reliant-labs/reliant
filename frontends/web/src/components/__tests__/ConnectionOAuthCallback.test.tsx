// Copyright (c) 2025 Reliant Labs

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

const { completeOAuth, search } = vi.hoisted(() => ({
  completeOAuth: vi.fn(),
  search: { current: {} as Record<string, string | undefined> },
}))

vi.mock('@/api/connection-grpc', () => ({
  connectionGrpc: { completeOAuth },
  connectionErrorMessage: (err: unknown) => (err instanceof Error ? err.message : String(err)),
}))

vi.mock('@tanstack/react-router', () => ({
  useSearch: () => search.current,
}))

import { ConnectionOAuthCallback } from '@/components/ConnectionOAuthCallback'

/**
 * The web app's end of an integration OAuth flow: the API relays the code
 * here, and the page finishes it as the signed-in user, then returns to where
 * the user connected from.
 */
describe('ConnectionOAuthCallback', () => {
  let replace: ReturnType<typeof vi.fn>

  beforeEach(() => {
    completeOAuth.mockReset()
    replace = vi.fn()
    vi.stubGlobal('location', { ...window.location, replace, origin: 'https://app.reliant.example' })
  })

  afterEach(() => vi.unstubAllGlobals())

  it('finishes the flow and returns to the page the user connected from', async () => {
    search.current = { code: 'the-code', state: 'the-state', redirect_after: '/workflow/x?tab=1' }
    completeOAuth.mockResolvedValue({ connection: { id: 'conn_1' }, redirectAfter: '/workflow/x?tab=1' })

    render(<ConnectionOAuthCallback />)

    await waitFor(() => expect(replace).toHaveBeenCalledWith('/workflow/x?tab=1&connection=conn_1'))
    expect(completeOAuth).toHaveBeenCalledTimes(1)
    expect(completeOAuth).toHaveBeenCalledWith({ state: 'the-state', code: 'the-code' })
  })

  it('never follows an off-origin redirect_after', async () => {
    search.current = { code: 'c', state: 's' }
    completeOAuth.mockResolvedValue({ connection: { id: 'conn_1' }, redirectAfter: '//evil.example/x' })

    render(<ConnectionOAuthCallback />)

    await waitFor(() => expect(replace).toHaveBeenCalledWith('/?connection=conn_1'))
  })

  it('shows why when the server refuses the code', async () => {
    search.current = { code: 'c', state: 's', redirect_after: '/workflow/x' }
    completeOAuth.mockRejectedValue(new Error('this authorization was started by a different account'))

    render(<ConnectionOAuthCallback />)

    expect(await screen.findByRole('alert')).toHaveTextContent('started by a different account')
    expect(replace).not.toHaveBeenCalled()
    screen.getByRole('button', { name: 'Back to Reliant' }).click()
    expect(replace).toHaveBeenCalledWith('/workflow/x')
  })

  it('shows a relayed refusal without calling the server', async () => {
    search.current = { error: 'denied', redirect_after: '/workflow/x' }

    render(<ConnectionOAuthCallback />)

    expect(await screen.findByRole('alert')).toHaveTextContent("Access wasn't granted")
    expect(completeOAuth).not.toHaveBeenCalled()
  })

  it('refuses a link without a code', async () => {
    search.current = { state: 's' }
    render(<ConnectionOAuthCallback />)
    expect(await screen.findByRole('alert')).toHaveTextContent('incomplete')
    expect(completeOAuth).not.toHaveBeenCalled()
  })
})
