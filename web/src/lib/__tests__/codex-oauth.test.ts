import { vi } from 'vitest'

vi.mock('@/api/settings-grpc', () => ({
  settingsGrpc: {
    completeCodexOAuth: vi.fn(),
  },
}))

vi.mock('@/api/daemon-grpc', () => ({
  startOAuthViaDaemon: vi.fn(),
}))

import { settingsGrpc } from '@/api/settings-grpc'
import { startOAuthViaDaemon } from '@/api/daemon-grpc'
import { CODEX_OAUTH_STATE_PREFIX, runCodexOAuthFlow } from '@/lib/codex-oauth'

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
      subtle: {
        digest,
      },
    } as unknown as Crypto,
  })
}

describe('runCodexOAuthFlow', () => {
  const completeCodexOAuthMock = vi.mocked(settingsGrpc.completeCodexOAuth)
  const startOAuthViaDaemonMock = vi.mocked(startOAuthViaDaemon)

  beforeEach(() => {
    vi.clearAllMocks()
    setCryptoMock()
  })

  it('calls daemon, validates state, and exchanges code', async () => {
    // The daemon mock captures the URL template and returns matching state
    let capturedUrlTemplate = ''
    startOAuthViaDaemonMock.mockImplementation(async (urlTemplate: string) => {
      capturedUrlTemplate = urlTemplate
      const url = new URL(urlTemplate.replace('{redirect_uri}', 'http://127.0.0.1:9999/callback'))
      const state = url.searchParams.get('state') || ''
      return {
        code: 'test-code',
        state,
        redirectUri: 'http://127.0.0.1:9999/callback',
        $typeName: 'reliant.v1.StartOAuthFlowResponse' as const,
        $unknown: undefined,
      } as any
    })

    completeCodexOAuthMock.mockResolvedValue({ success: true, message: 'Connected' })

    const result = await runCodexOAuthFlow()

    expect(result).toEqual({ ok: true, message: 'Connected' })

    // Verify the URL template has the placeholder
    expect(capturedUrlTemplate).toContain('{redirect_uri}')

    // Verify Codex-specific params in the URL template
    const parsedUrl = new URL(capturedUrlTemplate.replace('{redirect_uri}', 'http://placeholder'))
    expect(parsedUrl.origin + parsedUrl.pathname).toBe('https://auth.openai.com/oauth/authorize')
    expect(parsedUrl.searchParams.get('response_type')).toBe('code')
    expect(parsedUrl.searchParams.get('code_challenge_method')).toBe('S256')
    expect(parsedUrl.searchParams.get('id_token_add_organizations')).toBe('true')
    expect(parsedUrl.searchParams.get('codex_cli_simplified_flow')).toBe('true')
    expect(parsedUrl.searchParams.get('originator')).toBe('pi')
    expect(parsedUrl.searchParams.get('state')?.startsWith(CODEX_OAUTH_STATE_PREFIX)).toBe(true)

    // Verify completeCodexOAuth was called with the right args
    expect(completeCodexOAuthMock).toHaveBeenCalledWith(
      'test-code',
      expect.any(String),
      'http://127.0.0.1:9999/callback'
    )
  })

  it('fails when daemon returns mismatched state', async () => {
    startOAuthViaDaemonMock.mockResolvedValue({
      code: 'test-code',
      state: 'reliant:oauth:codex:wrong-state',
      redirectUri: 'http://127.0.0.1:9999/callback',
    } as any)

    const result = await runCodexOAuthFlow()
    expect(result).toEqual({
      ok: false,
      errorCode: 'state_mismatch',
      message: 'OAuth state mismatch',
    })
    expect(completeCodexOAuthMock).not.toHaveBeenCalled()
  })

  it('returns daemon_error when daemon RPC throws', async () => {
    startOAuthViaDaemonMock.mockRejectedValue(new Error('Daemon not available'))

    const result = await runCodexOAuthFlow()
    expect(result).toEqual({
      ok: false,
      errorCode: 'daemon_error',
      message: 'Daemon not available',
    })
  })

  it('returns token_exchange_failed when completeCodexOAuth fails', async () => {
    startOAuthViaDaemonMock.mockImplementation(async (urlTemplate: string) => {
      const url = new URL(urlTemplate.replace('{redirect_uri}', 'http://127.0.0.1:9999/callback'))
      const state = url.searchParams.get('state') || ''
      return {
        code: 'test-code',
        state,
        redirectUri: 'http://127.0.0.1:9999/callback',
      } as any
    })

    completeCodexOAuthMock.mockResolvedValue({
      success: false,
      message: 'Token exchange failed',
    })

    const result = await runCodexOAuthFlow()
    expect(result).toEqual({
      ok: false,
      errorCode: 'token_exchange_failed',
      message: 'Token exchange failed',
    })
  })

  it('passes timeout to daemon in seconds', async () => {
    startOAuthViaDaemonMock.mockImplementation(async (urlTemplate: string) => {
      const url = new URL(urlTemplate.replace('{redirect_uri}', 'http://127.0.0.1:9999/callback'))
      const state = url.searchParams.get('state') || ''
      return {
        code: 'test-code',
        state,
        redirectUri: 'http://127.0.0.1:9999/callback',
      } as any
    })

    completeCodexOAuthMock.mockResolvedValue({ success: true, message: 'OK' })

    await runCodexOAuthFlow({ timeoutMs: 30000 })

    expect(startOAuthViaDaemonMock).toHaveBeenCalledWith(
      expect.any(String),
      30 // 30000ms / 1000
    )
  })
})
