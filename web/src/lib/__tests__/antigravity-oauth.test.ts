import { vi } from 'vitest'

vi.mock('@/api/settings-grpc', () => ({
  settingsGrpc: {
    completeAntigravityOAuth: vi.fn(),
  },
}))

vi.mock('@/api/daemon-grpc', () => ({
  startOAuthViaDaemon: vi.fn(),
}))

import { settingsGrpc } from '@/api/settings-grpc'
import { startOAuthViaDaemon } from '@/api/daemon-grpc'
import {
  ANTIGRAVITY_OAUTH_STATE_PREFIX,
  runAntigravityOAuthFlow,
} from '@/lib/antigravity-oauth'

const setCryptoMock = () => {
  const getRandomValues = vi.fn((values: Uint8Array) => {
    for (let i = 0; i < values.length; i += 1) {
      values[i] = (i * 31) % 256
    }
    return values
  })

  const digest = vi.fn(async (_algorithm: string, data: BufferSource) => {
    const bytes =
      data instanceof ArrayBuffer
        ? new Uint8Array(data)
        : new Uint8Array(data.buffer, data.byteOffset, data.byteLength)

    const hash = new Uint8Array(32)
    for (let i = 0; i < hash.length; i += 1) {
      hash[i] = bytes[i % bytes.length] ?? i
    }

    return hash.buffer
  })

  Object.defineProperty(globalThis, 'crypto', {
    configurable: true,
    writable: true,
    value: {
      getRandomValues,
      subtle: { digest },
    } as unknown as Crypto,
  })
}

describe('runAntigravityOAuthFlow', () => {
  const completeAntigravityOAuthMock = vi.mocked(settingsGrpc.completeAntigravityOAuth)
  const startOAuthViaDaemonMock = vi.mocked(startOAuthViaDaemon)

  beforeEach(() => {
    vi.clearAllMocks()
    setCryptoMock()
    // Simulate Electron so the daemon path (which we mock) is taken
    ;(window as any).electronAPI = {}
  })

  afterEach(() => {
    delete (window as any).electronAPI
  })

  const echoStateDaemon = () => {
    let capturedUrlTemplate = ''
    startOAuthViaDaemonMock.mockImplementation(async (urlTemplate: string) => {
      capturedUrlTemplate = urlTemplate
      const url = new URL(
        urlTemplate.replace('{redirect_uri}', 'http://127.0.0.1:9999/callback'),
      )
      return {
        code: 'test-code',
        state: url.searchParams.get('state') || '',
        redirectUri: 'http://127.0.0.1:9999/callback',
      } as any
    })
    return () => capturedUrlTemplate
  }

  it('builds a Google authorize URL with PKCE, offline access and forced consent', async () => {
    const captured = echoStateDaemon()
    completeAntigravityOAuthMock.mockResolvedValue({ success: true, message: 'Connected' })

    const result = await runAntigravityOAuthFlow()
    expect(result).toEqual({ ok: true, message: 'Connected' })

    const template = captured()
    expect(template).toContain('{redirect_uri}')

    const parsedUrl = new URL(template.replace('{redirect_uri}', 'http://placeholder'))
    expect(parsedUrl.origin + parsedUrl.pathname).toBe(
      'https://accounts.google.com/o/oauth2/auth',
    )
    expect(parsedUrl.searchParams.get('response_type')).toBe('code')
    expect(parsedUrl.searchParams.get('client_id')).toBe(
      '1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com',
    )
    expect(parsedUrl.searchParams.get('code_challenge_method')).toBe('S256')
    expect(parsedUrl.searchParams.get('code_challenge')).toBeTruthy()
    // Without these two Google returns no refresh token, and the connection
    // dies silently about an hour after it appeared to work.
    expect(parsedUrl.searchParams.get('access_type')).toBe('offline')
    expect(parsedUrl.searchParams.get('prompt')).toBe('consent')

    const scopes = (parsedUrl.searchParams.get('scope') || '').split(' ')
    expect(scopes).toEqual([
      'https://www.googleapis.com/auth/cloud-platform',
      'https://www.googleapis.com/auth/userinfo.email',
      'https://www.googleapis.com/auth/userinfo.profile',
      'https://www.googleapis.com/auth/cclog',
      'https://www.googleapis.com/auth/experimentsandconfigs',
      'https://www.googleapis.com/auth/aicode',
      'openid',
    ])

    expect(
      parsedUrl.searchParams.get('state')?.startsWith(ANTIGRAVITY_OAUTH_STATE_PREFIX),
    ).toBe(true)

    // The wire contract carries NO state — code, verifier and redirect only.
    expect(completeAntigravityOAuthMock).toHaveBeenCalledWith(
      'test-code',
      expect.any(String),
      'http://127.0.0.1:9999/callback',
    )
    expect(completeAntigravityOAuthMock.mock.calls[0]).toHaveLength(3)
  })

  it('still verifies state locally even though the exchange does not carry it', async () => {
    startOAuthViaDaemonMock.mockResolvedValue({
      code: 'test-code',
      state: 'reliant:oauth:antigravity:wrong-state',
      redirectUri: 'http://127.0.0.1:9999/callback',
    } as any)

    const result = await runAntigravityOAuthFlow()
    expect(result).toEqual({
      ok: false,
      errorCode: 'state_mismatch',
      message: 'OAuth state mismatch',
    })
    expect(completeAntigravityOAuthMock).not.toHaveBeenCalled()
  })

  it('returns daemon_error when the daemon RPC throws', async () => {
    startOAuthViaDaemonMock.mockRejectedValue(new Error('Daemon not available'))

    const result = await runAntigravityOAuthFlow()
    expect(result).toEqual({
      ok: false,
      errorCode: 'daemon_error',
      message: 'Daemon not available',
    })
  })

  it('returns token_exchange_failed when the backend rejects the code', async () => {
    echoStateDaemon()
    completeAntigravityOAuthMock.mockResolvedValue({
      success: false,
      message: 'Token exchange failed',
    })

    const result = await runAntigravityOAuthFlow()
    expect(result).toEqual({
      ok: false,
      errorCode: 'token_exchange_failed',
      message: 'Token exchange failed',
    })
  })
})
