import { beforeEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/react'
import type { Session, User } from '@supabase/supabase-js'

// ---------------------------------------------------------------------------
// Every API-key session shares one synthetic user id, so replacing one key
// with another must be a change of account like any other: the session owner
// moves on at once (work captured for the previous key can neither send with
// the next key nor publish into its caches), and the previous account's
// settings and queries are cleared before the next is installed. The same
// holds from a person to a key and from a key to a person.
// ---------------------------------------------------------------------------

const backend = vi.hoisted(() => ({
  fetchSettingsFor: vi.fn(),
  batchUpsertSettings: vi.fn(),
}))
vi.mock('@/hooks/settings-queries', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/hooks/settings-queries')>()),
  fetchSettingsFor: backend.fetchSettingsFor,
  batchUpsertSettings: backend.batchUpsertSettings,
  deleteSetting: vi.fn(async () => undefined),
}))

const resetMock = vi.hoisted(() => vi.fn())
vi.mock('../chatStore', () => ({ useChatStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../projectStore', () => ({ useProjectStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../worktreeStore', () => ({ useWorktreeStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../chatNavigationStore', () => ({ useChatNavigationStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../attachmentStore', () => ({ useAttachmentStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../../hooks/worktree-queries', () => ({ resetWorktreeAnnouncements() {} }))
vi.mock('@/services/controlPlane/onboarding', () => ({ resetUserCache() {} }))
vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: vi.fn(async () => ({ data: { session: null }, error: null })),
      onAuthStateChange: vi.fn(() => ({ data: { subscription: { unsubscribe: vi.fn() } } })),
      signOut: vi.fn(async () => ({ error: null })),
    },
  },
  setAuthStorageUnreadableHandler: vi.fn(),
}))
vi.mock('@/lib/logger', () => ({ logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() } }))
vi.mock('@/lib/telemetry', () => ({ setTelemetryUserId: vi.fn() }))

import { API_KEY_USER_ID } from '@/lib/auth/apiKeyUser'
import { currentSessionOwner, isCurrentSessionOwner } from '@/lib/auth/sessionOwner'
import { readSettingFromServer, writeStringSetting } from '@/lib/settingsPersistence'
import { queryClient } from '@/lib/query-client'
import { settingsSync } from '@/services/settingsSync'
import { useAuthStore } from '../authStore'

const KEY = 'sidebar.projects'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

async function signInWithKey(apiKey: string) {
  useAuthStore.getState().setApiKeySession(apiKey)
  await waitFor(() => expect(useAuthStore.getState().session?.access_token).toBe(apiKey))
  await waitFor(() => expect(currentSessionOwner().userId).toBe(API_KEY_USER_ID))
}

describe('replacing the credential changes the session owner', () => {
  beforeEach(async () => {
    window.localStorage.clear()
    backend.fetchSettingsFor.mockReset()
    backend.batchUpsertSettings.mockReset()
    backend.batchUpsertSettings.mockResolvedValue([])
    vi.restoreAllMocks()
    await useAuthStore.getState().signOut()
    useAuthStore.setState({ user: null, session: null, initialized: false, authError: null })
  })

  it("drops a deferred read made with A's key once B's key replaces it: nothing published", async () => {
    await signInWithKey('key-a')
    const a = currentSessionOwner()
    const answer = deferred<{ settings: Array<{ key: string; value: string }> }>()
    backend.fetchSettingsFor.mockReturnValue(answer.promise)
    const updateCache = vi.spyOn(settingsSync, 'updateCache')
    const removeFromCache = vi.spyOn(settingsSync, 'removeFromCache')

    const read = readSettingFromServer(KEY, a)
    await waitFor(() => expect(backend.fetchSettingsFor).toHaveBeenCalledTimes(1))
    useAuthStore.getState().setApiKeySession('key-b')
    // The owner moves on before the key is stored for the next request.
    expect(isCurrentSessionOwner(a)).toBe(false)

    answer.resolve({ settings: [{ key: KEY, value: '{"a":{"show_all":true}}' }] })
    expect(await read).toEqual({ status: 'error' })
    expect(updateCache).not.toHaveBeenCalled()
    expect(removeFromCache).not.toHaveBeenCalled()
  })

  it("never sends a write captured with A's key once B's key replaces it", async () => {
    await signInWithKey('key-a')
    const a = currentSessionOwner()
    const updateCache = vi.spyOn(settingsSync, 'updateCache')

    // Queued for A; the key changes before it reaches the network.
    const write = writeStringSetting(KEY, '{"a":{"pinned":["p1"]}}', a)
    useAuthStore.getState().setApiKeySession('key-b')
    await expect(write).rejects.toThrow()
    expect(backend.batchUpsertSettings).not.toHaveBeenCalled()
    expect(updateCache).not.toHaveBeenCalled()
  })

  it("clears the previous key's queries and settings before B's key is installed", async () => {
    await signInWithKey('key-a')
    queryClient.setQueryData(['sidebar-project-prefs'], { a: { pinned: ['p1'] } })
    const reset = vi.spyOn(settingsSync, 'reset')
    await signInWithKey('key-b')
    expect(queryClient.getQueryData(['sidebar-project-prefs'])).toBeUndefined()
    expect(reset).toHaveBeenCalled()
  })

  it('keeps the owner when the same key is set again', async () => {
    await signInWithKey('key-a')
    const a = currentSessionOwner()
    useAuthStore.getState().setApiKeySession('key-a')
    await waitFor(() => expect(useAuthStore.getState().initialized).toBe(true))
    expect(isCurrentSessionOwner(a)).toBe(true)
  })

  it('moves the owner from a person to a key, and from a key to a person, clearing state each time', async () => {
    const person = { id: 'alice', is_anonymous: false } as User
    await useAuthStore.getState().installIdentity(person, { user: person, access_token: 'jwt' } as Session)
    const alice = currentSessionOwner()
    queryClient.setQueryData(['sidebar-project-prefs'], { a: {} })

    await signInWithKey('key-a')
    expect(isCurrentSessionOwner(alice)).toBe(false)
    expect(queryClient.getQueryData(['sidebar-project-prefs'])).toBeUndefined()

    const keyA = currentSessionOwner()
    queryClient.setQueryData(['sidebar-project-prefs'], { a: {} })
    window.localStorage.removeItem('reliant-api-key')
    await useAuthStore.getState().installIdentity(person, { user: person, access_token: 'jwt' } as Session)
    expect(isCurrentSessionOwner(keyA)).toBe(false)
    expect(currentSessionOwner().userId).toBe('alice')
    expect(queryClient.getQueryData(['sidebar-project-prefs'])).toBeUndefined()
  })
})
