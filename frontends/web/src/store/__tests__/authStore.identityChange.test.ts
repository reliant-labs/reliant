import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AuthChangeEvent, Session } from '@supabase/supabase-js'

// ---------------------------------------------------------------------------
// An identity change that did not come from this tab's signOut() must clear
// the previous user's state too.
//
// With the shared session cookie (lib/auth/sharedSession.ts), signing out or
// signing in as another user on forge.* reaches app.* only as an auth event
// (SIGNED_OUT, or SIGNED_IN for a different user). The listener used to just
// replace user/session, so app.* went on showing user A's chats, projects and
// cached onboarding answers to user B.
// ---------------------------------------------------------------------------

type Listener = (event: AuthChangeEvent, session: Session | null) => Promise<void>

const auth = vi.hoisted(() => ({ listener: null as Listener | null }))
const resetMock = vi.hoisted(() => vi.fn())

vi.mock('../chatStore', () => ({ useChatStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../projectStore', () => ({ useProjectStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../worktreeStore', () => ({ useWorktreeStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../chatNavigationStore', () => ({ useChatNavigationStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../attachmentStore', () => ({ useAttachmentStore: { getState: () => ({ reset: resetMock }) } }))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: vi.fn(async () => ({ data: { session: null }, error: null })),
      onAuthStateChange: vi.fn((cb: Listener) => {
        auth.listener = cb
        return { data: { subscription: { unsubscribe: vi.fn() } } }
      }),
    },
  },
  setAuthStorageUnreadableHandler: vi.fn(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

const { queryClient } = await import('@/lib/query-client')
const { useAuthStore } = await import('@/store/authStore')

const sessionFor = (id: string) => ({ access_token: `t-${id}`, user: { id } }) as unknown as Session

const signedInAs = async (id: string) => {
  await auth.listener!('SIGNED_IN', sessionFor(id))
  queryClient.setQueryData(['onboarding', 'currentUser'], { id, onboardingCompleted: true })
  resetMock.mockClear()
}

beforeEach(async () => {
  queryClient.clear()
  useAuthStore.setState({ user: null, session: null, initialized: false })
  await useAuthStore.getState().initialize()
  resetMock.mockClear()
})

describe('identity changes observed by the auth listener', () => {
  it('clears user A\'s stores and cached answers when user B signs in elsewhere', async () => {
    await signedInAs('user-a')

    await auth.listener!('SIGNED_IN', sessionFor('user-b'))

    expect(resetMock).toHaveBeenCalled()
    expect(queryClient.getQueryData(['onboarding', 'currentUser'])).toBeUndefined()
    expect(useAuthStore.getState().user?.id).toBe('user-b')
  })

  it('clears them on a sign-out made by another host', async () => {
    await signedInAs('user-a')

    await auth.listener!('SIGNED_OUT', null)

    expect(resetMock).toHaveBeenCalled()
    expect(queryClient.getQueryData(['onboarding', 'currentUser'])).toBeUndefined()
    expect(useAuthStore.getState().user).toBeNull()
  })

  it('keeps them across a token refresh of the same user', async () => {
    await signedInAs('user-a')

    await auth.listener!('TOKEN_REFRESHED', sessionFor('user-a'))

    expect(resetMock).not.toHaveBeenCalled()
    expect(queryClient.getQueryData(['onboarding', 'currentUser'])).toEqual({
      id: 'user-a',
      onboardingCompleted: true,
    })
  })
})
