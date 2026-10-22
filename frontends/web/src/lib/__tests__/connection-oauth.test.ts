// Copyright (c) 2025 Reliant Labs

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { startOAuth, completeOAuth } = vi.hoisted(() => ({
  startOAuth: vi.fn(),
  completeOAuth: vi.fn(),
}))

vi.mock('@/api/connection-grpc', () => ({
  connectionGrpc: { startOAuth, completeOAuth },
}))

import {
  CONNECTION_OAUTH_CALLBACK_PATH,
  ConnectionOAuthCancelled,
  connectWithOAuth,
  connectsThroughSystemBrowser,
} from '@/lib/connection-oauth'

/**
 * The integration OAuth flow must not depend on anything the API sets in the
 * browser. In production the app and the API are different sites, so the old
 * flow — fetch `/integrations/oauth/{p}/start?mode=json` with
 * `credentials: 'include'`, then navigate — lost its binding cookie and always
 * landed on `connection_error=session`. The flow is now two authenticated RPCs
 * with the provider between them; these tests pin which surface does what.
 */

const input = { integrationId: 'slack', name: 'work', redirectAfter: '/workflow/x', params: {} }
const connection = {
  id: 'conn_1', integrationId: 'slack', authKind: 'oauth2', name: 'work',
  accountLabel: 'Acme', status: 'active', isDefault: true,
} as const

const setBridge = (bridge: unknown) => {
  ;(window as unknown as { electronAPI?: unknown }).electronAPI = bridge
}

describe('connectWithOAuth', () => {
  let assign: ReturnType<typeof vi.fn>
  const fetchSpy = vi.fn()

  beforeEach(() => {
    startOAuth.mockReset().mockResolvedValue('https://slack.com/oauth/v2/authorize?state=s')
    completeOAuth.mockReset().mockResolvedValue({ connection, redirectAfter: '/workflow/x' })
    assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign, origin: 'https://app.reliant.example' })
    vi.stubGlobal('fetch', fetchSpy)
  })

  afterEach(() => {
    setBridge(undefined)
    vi.unstubAllGlobals()
  })

  it('pins the web callback route the API relays to', () => {
    // Go: connections.AppCallbackPath. The route in routes.tsx uses this path.
    expect(CONNECTION_OAUTH_CALLBACK_PATH).toBe('/connections/oauth/callback')
  })

  describe('web', () => {
    it('starts over the authenticated RPC and navigates the page to the provider', async () => {
      expect(connectsThroughSystemBrowser()).toBe(false)
      const outcome = await connectWithOAuth(input)

      expect(outcome).toEqual({ kind: 'redirecting' })
      expect(startOAuth).toHaveBeenCalledWith(input)
      expect(startOAuth.mock.calls[0][0]).not.toHaveProperty('loopbackRedirect')
      expect(assign).toHaveBeenCalledWith('https://slack.com/oauth/v2/authorize?state=s')
      expect(completeOAuth).not.toHaveBeenCalled()
      // No cross-site fetch to an API route that would need a cookie.
      expect(fetchSpy).not.toHaveBeenCalled()
    })

    it('surfaces a refused start without navigating', async () => {
      startOAuth.mockRejectedValue(new Error('https://evil.example is not an origin this deployment serves'))
      await expect(connectWithOAuth(input)).rejects.toThrow('not an origin')
      expect(assign).not.toHaveBeenCalled()
    })
  })

  describe('desktop', () => {
    const desktopBridge = () => {
      const bridge = {
        startConnectionOAuthReceiver: vi.fn(async () => ({ flowId: 'f1', redirectUri: 'http://127.0.0.1:51000/callback' })),
        waitForProviderOAuth: vi.fn(async () => ({ code: 'the-code', state: 'the-state' })),
        cancelProviderOAuth: vi.fn(async () => undefined),
        openExternal: vi.fn(async () => undefined),
      }
      setBridge(bridge)
      return bridge
    }

    it('relays to its loopback receiver, opens the system browser, and finishes the flow itself', async () => {
      const bridge = desktopBridge()
      expect(connectsThroughSystemBrowser()).toBe(true)

      const outcome = await connectWithOAuth(input)

      expect(outcome).toEqual({ kind: 'connected', connection })
      // The receiver is bound before the flow starts: the API needs its address.
      expect(bridge.startConnectionOAuthReceiver.mock.invocationCallOrder[0]).toBeLessThan(startOAuth.mock.invocationCallOrder[0])
      expect(startOAuth).toHaveBeenCalledWith({ ...input, loopbackRedirect: 'http://127.0.0.1:51000/callback' })
      expect(bridge.openExternal).toHaveBeenCalledWith('https://slack.com/oauth/v2/authorize?state=s')
      expect(bridge.waitForProviderOAuth).toHaveBeenCalledWith('f1')
      expect(completeOAuth).toHaveBeenCalledWith({ code: 'the-code', state: 'the-state' })
      // The page never navigates away from the app.
      expect(assign).not.toHaveBeenCalled()
      expect(bridge.cancelProviderOAuth).not.toHaveBeenCalled()
    })

    it('reports a refusal the API relayed and releases the receiver', async () => {
      const bridge = desktopBridge()
      bridge.waitForProviderOAuth.mockRejectedValue(new Error('denied'))

      await expect(connectWithOAuth(input)).rejects.toThrow("Access wasn't granted at the provider.")
      expect(completeOAuth).not.toHaveBeenCalled()
      expect(bridge.cancelProviderOAuth).toHaveBeenCalledWith('f1')
    })

    it('releases the receiver when the flow cannot start', async () => {
      const bridge = desktopBridge()
      startOAuth.mockRejectedValue(new Error('Slack is not available'))

      await expect(connectWithOAuth(input)).rejects.toThrow('Slack is not available')
      expect(bridge.openExternal).not.toHaveBeenCalled()
      expect(bridge.cancelProviderOAuth).toHaveBeenCalledWith('f1')
    })

    it('cancels: the receiver is released and nothing is completed', async () => {
      const bridge = desktopBridge()
      let rejectWait: (err: Error) => void = () => {}
      bridge.waitForProviderOAuth.mockImplementation(
        () => new Promise((_, reject) => { rejectWait = reject }),
      )
      bridge.cancelProviderOAuth.mockImplementation(async () => {
        rejectWait(new Error('OAuth flow cancelled'))
      })
      const controller = new AbortController()

      const pending = connectWithOAuth(input, controller.signal)
      await vi.waitFor(() => expect(bridge.waitForProviderOAuth).toHaveBeenCalled())
      controller.abort()

      await expect(pending).rejects.toBeInstanceOf(ConnectionOAuthCancelled)
      expect(bridge.cancelProviderOAuth).toHaveBeenCalledWith('f1')
      expect(completeOAuth).not.toHaveBeenCalled()
    })
  })
})
