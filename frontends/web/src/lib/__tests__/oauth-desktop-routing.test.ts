import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api/settings-grpc', () => ({
  settingsGrpc: {
    completeClaudeOAuth: vi.fn(),
    completeCodexOAuth: vi.fn(),
  },
}))

vi.mock('@/api/daemon-grpc', () => ({
  startOAuthViaDaemon: vi.fn(),
}))

vi.mock('@/lib/oauth-local', () => ({
  startOAuthViaLocalServer: vi.fn(),
}))

import { settingsGrpc } from '@/api/settings-grpc'
import { startOAuthViaDaemon } from '@/api/daemon-grpc'
import { runClaudeOAuthFlow } from '@/lib/claude-oauth'
import { runCodexOAuthFlow } from '@/lib/codex-oauth'

/**
 * The desktop app must NOT run provider sign-in through
 * `DaemonService/StartOAuthFlow`.
 *
 * That RPC binds a port and then blocks on the human, so one request/response
 * call had to span a consent screen. In the packaged app it died at ~15s —
 * `Failed to fetch (api.reliantapi.com)` — and cancelling it tore down the
 * daemon's listener, so the browser's redirect hit a closed port
 * (ERR_CONNECTION_REFUSED). Both reported symptoms, one cause.
 *
 * It is also unreachable by construction in distributed mode: the daemon is a
 * pod, and its loopback listener is on the wrong machine entirely.
 *
 * These tests pin the ROUTING decision. The receiver's own behavior is covered
 * in electron/test/oauth-provider-login.test.js.
 */

const setCryptoMock = () => {
  Object.defineProperty(globalThis, 'crypto', {
    configurable: true,
    writable: true,
    value: {
      getRandomValues: (values: Uint8Array) => {
        for (let i = 0; i < values.length; i += 1) values[i] = (i * 31) % 256
        return values
      },
      subtle: {
        digest: async () => new Uint8Array(32).buffer,
      },
    } as unknown as Crypto,
  })
}

/** An Electron build whose main process has the local receiver. */
const withLocalReceiver = () => {
  const startProviderOAuth = vi.fn(async () => ({
    flowId: 'flow-1',
    redirectUri: 'http://localhost:51000/callback',
  }))
  const waitForProviderOAuth = vi.fn(async () => ({
    code: 'the-code',
    // Echoed back by the flow so state validation passes.
    state: capturedState,
    redirectUri: 'http://localhost:51000/callback',
  }))
  ;(window as unknown as { electronAPI: unknown }).electronAPI = {
    startProviderOAuth,
    waitForProviderOAuth,
    cancelProviderOAuth: vi.fn(),
  }
  return { startProviderOAuth, waitForProviderOAuth }
}

/**
 * The flow generates its own `state` and rejects a response that does not echo
 * it, so the fake receiver has to return whatever was just generated.
 */
let capturedState = ''
const captureStateFrom = (authorizeUrlTemplate: string) => {
  capturedState = new URL(authorizeUrlTemplate).searchParams.get('state') ?? ''
}

describe.each([
  ['Claude', runClaudeOAuthFlow],
  ['Codex', runCodexOAuthFlow],
])('%s provider sign-in on the desktop app', (label, runFlow) => {
  beforeEach(() => {
    vi.clearAllMocks()
    setCryptoMock()
    capturedState = ''
    vi.mocked(settingsGrpc.completeClaudeOAuth).mockResolvedValue({
      success: true,
      message: 'ok',
    } as never)
    vi.mocked(settingsGrpc.completeCodexOAuth).mockResolvedValue({
      success: true,
      message: 'ok',
    } as never)
  })

  afterEach(() => {
    delete (window as unknown as { electronAPI?: unknown }).electronAPI
  })

  it('uses the local receiver and never the daemon RPC', async () => {
    const { startProviderOAuth } = withLocalReceiver()
    startProviderOAuth.mockImplementation(async (template: string) => {
      captureStateFrom(template)
      return { flowId: 'flow-1', redirectUri: 'http://localhost:51000/callback' }
    })

    const result = await runFlow()

    expect(startProviderOAuth).toHaveBeenCalledTimes(1)
    // The whole point: no request/response RPC spans the user's browser trip.
    expect(startOAuthViaDaemon).not.toHaveBeenCalled()
    expect(result.ok).toBe(true)
  })

  it('binds the port BEFORE the wait, so no redirect can arrive early', async () => {
    const { startProviderOAuth, waitForProviderOAuth } = withLocalReceiver()
    const order: string[] = []

    startProviderOAuth.mockImplementation(async (template: string) => {
      captureStateFrom(template)
      order.push('start')
      return { flowId: 'flow-1', redirectUri: 'http://localhost:51000/callback' }
    })
    waitForProviderOAuth.mockImplementation(async () => {
      order.push('wait')
      return {
        code: 'the-code',
        state: capturedState,
        redirectUri: 'http://localhost:51000/callback',
      }
    })

    await runFlow()

    // The old design could not express this: binding and waiting were one
    // call, so there was no moment where the port existed and the browser had
    // not yet been sent.
    expect(order).toEqual(['start', 'wait'])
  })

  it('passes the authorize template through with its placeholder intact', async () => {
    const { startProviderOAuth } = withLocalReceiver()
    startProviderOAuth.mockImplementation(async (template: string) => {
      captureStateFrom(template)
      return { flowId: 'flow-1', redirectUri: 'http://localhost:51000/callback' }
    })

    await runFlow()

    // The main process substitutes the URI it ACTUALLY bound. If the renderer
    // resolved the redirect URI instead, the advertised address and the
    // listening socket could disagree — which is what the reported
    // ERR_CONNECTION_REFUSED looked like.
    const template = startProviderOAuth.mock.calls[0][0] as string
    expect(template).toContain('{redirect_uri}')
  })

  it('exchanges the code with the redirect URI the receiver actually bound', async () => {
    const { startProviderOAuth } = withLocalReceiver()
    startProviderOAuth.mockImplementation(async (template: string) => {
      captureStateFrom(template)
      return { flowId: 'flow-1', redirectUri: 'http://localhost:51000/callback' }
    })

    await runFlow()

    // The provider validates redirect_uri again at token exchange; sending a
    // reconstructed one fails with a mismatch.
    const exchange =
      label === 'Claude'
        ? vi.mocked(settingsGrpc.completeClaudeOAuth)
        : vi.mocked(settingsGrpc.completeCodexOAuth)
    expect(exchange.mock.calls[0][2]).toBe('http://localhost:51000/callback')
  })

  it('falls back to the daemon RPC on builds without the bridge', async () => {
    // Older shipped builds have no local receiver. They must keep the old
    // path rather than throwing — it is degraded, not absent.
    ;(window as unknown as { electronAPI: unknown }).electronAPI = {}
    vi.mocked(startOAuthViaDaemon).mockImplementation(async (template: string) => {
      captureStateFrom(template)
      return {
        code: 'the-code',
        state: capturedState,
        redirectUri: 'http://localhost:51000/callback',
      } as never
    })

    await runFlow()

    expect(startOAuthViaDaemon).toHaveBeenCalledTimes(1)
  })
})
