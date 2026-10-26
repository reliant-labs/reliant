// Copyright (c) 2025 Reliant Labs

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getSession: vi.fn(),
  refreshSession: vi.fn(),
  buildOAuthURL: vi.fn(),
  exchangeGithubOAuthCode: vi.fn(),
  invalidateQueries: vi.fn(),
  toastSuccess: vi.fn(),
}))

vi.mock('@/lib/supabase', () => ({
  supabase: { auth: { getSession: mocks.getSession, refreshSession: mocks.refreshSession } },
}))
vi.mock('@/services/controlPlane/git', () => ({
  gitService: {
    buildOAuthURL: mocks.buildOAuthURL,
    exchangeGithubOAuthCode: mocks.exchangeGithubOAuthCode,
  },
}))
vi.mock('@/lib/query-client', () => ({ queryClient: { invalidateQueries: mocks.invalidateQueries } }))
vi.mock('sonner', () => ({ toast: { success: mocks.toastSuccess } }))
vi.mock('@/lib/logger', () => ({ logger: { warn: vi.fn(), info: vi.fn(), error: vi.fn() } }))
vi.mock('@/hooks/useGitHubCredential', () => ({ GITHUB_CREDENTIAL_QUERY_KEY: ['gitCredential', 'github'] }))

import { connectGitHub } from '@/lib/github-connect'

const nowSeconds = () => Math.floor(Date.now() / 1000)
const setBridge = (bridge: unknown) => {
  ;(window as unknown as { electronAPI?: unknown }).electronAPI = bridge
}

describe('connectGitHub', () => {
  let assign: ReturnType<typeof vi.fn>

  beforeEach(() => {
    Object.values(mocks).forEach((m) => m.mockReset())
    mocks.getSession.mockResolvedValue({
      data: { session: { access_token: 'tok-1', expires_at: nowSeconds() + 3600 } },
    })
    mocks.buildOAuthURL.mockImplementation(
      (a: { token: string; returnTo: string; loopback?: string }) =>
        `https://cp.example/auth/github/authorize?token=${a.token}&returnTo=${encodeURIComponent(a.returnTo)}` +
        (a.loopback ? `&loopback=${encodeURIComponent(a.loopback)}` : ''),
    )
    mocks.invalidateQueries.mockResolvedValue(undefined)
    assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign, pathname: '/settings', search: '?tab=git' })
  })

  afterEach(() => {
    setBridge(undefined)
    vi.unstubAllGlobals()
  })

  describe('web', () => {
    it('navigates to the authorize URL for the current location', async () => {
      const outcome = await connectGitHub()

      expect(outcome).toEqual({ kind: 'redirecting' })
      expect(mocks.buildOAuthURL).toHaveBeenCalledWith({ token: 'tok-1', returnTo: '/settings?tab=git' })
      expect(assign).toHaveBeenCalledWith(
        'https://cp.example/auth/github/authorize?token=tok-1&returnTo=%2Fsettings%3Ftab%3Dgit',
      )
    })

    it('honours an explicit returnTo', async () => {
      await connectGitHub({ returnTo: '/onboarding?plan=x' })
      expect(mocks.buildOAuthURL).toHaveBeenCalledWith({ token: 'tok-1', returnTo: '/onboarding?plan=x' })
    })

    it('refreshes a token with under a minute left, and uses the new one', async () => {
      mocks.getSession.mockResolvedValue({
        data: { session: { access_token: 'stale', expires_at: nowSeconds() + 20 } },
      })
      mocks.refreshSession.mockResolvedValue({ data: { session: { access_token: 'fresh' } }, error: null })

      await connectGitHub()

      expect(mocks.refreshSession).toHaveBeenCalledTimes(1)
      expect(mocks.buildOAuthURL).toHaveBeenCalledWith(expect.objectContaining({ token: 'fresh' }))
    })

    it('does not refresh a token with time to spare', async () => {
      await connectGitHub()
      expect(mocks.refreshSession).not.toHaveBeenCalled()
    })

    it('reports an error rather than navigating when the refresh fails', async () => {
      mocks.getSession.mockResolvedValue({
        data: { session: { access_token: 'stale', expires_at: nowSeconds() - 5 } },
      })
      mocks.refreshSession.mockResolvedValue({ data: { session: null }, error: new Error('nope') })

      const outcome = await connectGitHub()

      expect(outcome).toMatchObject({ kind: 'error' })
      expect(assign).not.toHaveBeenCalled()
    })

    it('reports an error when signed out', async () => {
      mocks.getSession.mockResolvedValue({ data: { session: null } })
      expect(await connectGitHub()).toEqual({ kind: 'error', message: 'Not signed in' })
    })

    it('reports an error when no control plane is configured', async () => {
      mocks.buildOAuthURL.mockReturnValue(null)
      expect(await connectGitHub()).toEqual({ kind: 'error', message: 'Control plane URL not configured' })
      expect(assign).not.toHaveBeenCalled()
    })
  })

  describe('desktop bridge', () => {
    const exchangeOk = { ok: true, returnTo: '/', error: '', needsInstall: false, installUrl: '' }
    let bridge: {
      startConnectionOAuthReceiver: ReturnType<typeof vi.fn>
      waitForProviderOAuth: ReturnType<typeof vi.fn>
      cancelProviderOAuth: ReturnType<typeof vi.fn>
      openExternal: ReturnType<typeof vi.fn>
    }

    beforeEach(() => {
      bridge = {
        startConnectionOAuthReceiver: vi.fn().mockResolvedValue({ flowId: 'f1', redirectUri: 'http://127.0.0.1:5555/callback' }),
        waitForProviderOAuth: vi.fn().mockResolvedValue({ code: 'c', state: 's' }),
        cancelProviderOAuth: vi.fn().mockResolvedValue(undefined),
        openExternal: vi.fn().mockResolvedValue(undefined),
      }
      setBridge(bridge)
      mocks.exchangeGithubOAuthCode.mockResolvedValue(exchangeOk)
    })

    it('opens the system browser with the loopback, exchanges in the renderer, and refreshes queries', async () => {
      const outcome = await connectGitHub()

      expect(outcome).toEqual({ kind: 'connected', needsInstall: false })
      expect(assign).not.toHaveBeenCalled()
      expect(mocks.buildOAuthURL).toHaveBeenCalledWith({
        token: 'tok-1',
        returnTo: '/settings?tab=git',
        loopback: 'http://127.0.0.1:5555/callback',
      })
      expect(bridge.openExternal).toHaveBeenCalledTimes(1)
      expect(bridge.openExternal.mock.calls[0][0]).toContain('loopback=http%3A%2F%2F127.0.0.1%3A5555%2Fcallback')
      expect(mocks.exchangeGithubOAuthCode).toHaveBeenCalledWith('c', 's')
      const keys = mocks.invalidateQueries.mock.calls.map((c) => (c[0] as { queryKey: unknown }).queryKey)
      expect(keys).toContainEqual(['gitCredential', 'github'])
      expect(keys).toContainEqual(['onboarding', 'gitRepos'])
      expect(mocks.toastSuccess).toHaveBeenCalledWith('GitHub connected successfully')
    })

    it('opens the App install page when the account has no installation', async () => {
      mocks.exchangeGithubOAuthCode.mockResolvedValue({
        ...exchangeOk,
        needsInstall: true,
        installUrl: 'https://github.com/apps/reliant-labs/installations/new',
      })

      const outcome = await connectGitHub()

      expect(outcome).toEqual({ kind: 'connected', needsInstall: true })
      expect(bridge.openExternal).toHaveBeenLastCalledWith('https://github.com/apps/reliant-labs/installations/new')
      expect(bridge.openExternal).toHaveBeenCalledTimes(2)
    })

    it('ignores needsInstall without an install URL', async () => {
      mocks.exchangeGithubOAuthCode.mockResolvedValue({ ...exchangeOk, needsInstall: true, installUrl: '' })
      expect(await connectGitHub()).toEqual({ kind: 'connected', needsInstall: false })
      expect(bridge.openExternal).toHaveBeenCalledTimes(1)
    })

    it('surfaces a relayed GitHub denial and releases the receiver', async () => {
      bridge.waitForProviderOAuth.mockRejectedValue(new Error('access_denied: The user denied'))

      const outcome = await connectGitHub()

      expect(outcome).toEqual({ kind: 'error', message: "Access wasn't granted on GitHub." })
      expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled()
      expect(bridge.cancelProviderOAuth).toHaveBeenCalledWith('f1')
    })

    it('reports a failed exchange', async () => {
      mocks.exchangeGithubOAuthCode.mockResolvedValue({ ...exchangeOk, ok: false, error: 'bad_state' })
      expect(await connectGitHub()).toMatchObject({ kind: 'error' })
    })

    it('cancels through the AbortSignal: releases the receiver and reports cancelled', async () => {
      const controller = new AbortController()
      bridge.waitForProviderOAuth.mockImplementation(
        () =>
          new Promise((_, reject) => {
            controller.signal.addEventListener('abort', () => reject(new Error('cancelled')))
          }),
      )

      const pending = connectGitHub({ signal: controller.signal })
      await vi.waitFor(() => expect(bridge.waitForProviderOAuth).toHaveBeenCalled())
      controller.abort()

      expect(await pending).toEqual({ kind: 'cancelled' })
      expect(bridge.cancelProviderOAuth).toHaveBeenCalledWith('f1')
      expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled()
    })

    it('does not open a browser when already aborted', async () => {
      const controller = new AbortController()
      controller.abort()
      expect(await connectGitHub({ signal: controller.signal })).toEqual({ kind: 'cancelled' })
      expect(bridge.openExternal).not.toHaveBeenCalled()
    })

    it('falls back to navigation on a bridge without the receiver', async () => {
      setBridge({ openExternal: vi.fn() })
      expect(await connectGitHub()).toEqual({ kind: 'redirecting' })
      expect(assign).toHaveBeenCalled()
    })
  })
})
