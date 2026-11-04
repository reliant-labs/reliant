// Copyright (c) 2025 Reliant Labs

import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  saveCredential: vi.fn(),
  setQueryData: vi.fn(),
  invalidateQueries: vi.fn(),
}))

vi.mock('@/services/controlPlane/git', () => ({ gitService: { saveCredential: mocks.saveCredential } }))
vi.mock('@/services/controlPlane/config', () => ({ hasControlPlane: true }))
vi.mock('@/lib/query-client', () => ({
  queryClient: { setQueryData: mocks.setQueryData, invalidateQueries: mocks.invalidateQueries },
}))
vi.mock('@/lib/logger', () => ({ logger: { warn: vi.fn(), debug: vi.fn(), info: vi.fn() } }))
vi.mock('@/lib/analytics', () => ({ trackEvent: vi.fn() }))
vi.mock('@/lib/events', () => ({ getEventBus: () => ({ emit: vi.fn() }) }))
vi.mock('@/hooks/useGitHubCredential', () => ({ GITHUB_CREDENTIAL_QUERY_KEY: ['gitCredential', 'github'] }))

import { githubCredentialSync } from '@/lib/githubCredentialSync'

beforeEach(() => {
  Object.values(mocks).forEach((m) => m.mockReset())
  mocks.saveCredential.mockResolvedValue(undefined)
  mocks.invalidateQueries.mockResolvedValue(undefined)
})

describe('syncProviderTokenFromSession', () => {
  it('persists a GitHub App user token with its refresh token', async () => {
    const result = await githubCredentialSync.syncProviderTokenFromSession({
      provider_token: 'ghu_abc',
      provider_refresh_token: 'ghr_def',
    })

    expect(result).toEqual({ ok: true })
    expect(mocks.saveCredential).toHaveBeenCalledWith('github', 'ghu_abc', '', 'ghr_def')
  })

  it('persists a ghu_ token that arrived without a refresh token', async () => {
    await githubCredentialSync.syncProviderTokenFromSession({ provider_token: 'ghu_abc' })
    expect(mocks.saveCredential).toHaveBeenCalledWith('github', 'ghu_abc', '', '')
  })

  it.each([
    ['a classic OAuth token', { provider_token: 'gho_abc', provider_refresh_token: null }],
    ['a PAT', { provider_token: 'ghp_abc' }],
    ['no provider token', { provider_token: null }],
    ['no session', null],
  ])('does nothing for %s', async (_name, session) => {
    expect(await githubCredentialSync.syncProviderTokenFromSession(session)).toBeNull()
    expect(mocks.saveCredential).not.toHaveBeenCalled()
  })
})

describe('sync', () => {
  it('still defaults the refresh token to empty for the existing callers', async () => {
    await githubCredentialSync.sync('ghp_x', 'repo', 'link')
    expect(mocks.saveCredential).toHaveBeenCalledWith('github', 'ghp_x', 'repo', '')
  })
})
