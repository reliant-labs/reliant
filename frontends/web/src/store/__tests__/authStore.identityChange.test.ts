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

const auth = vi.hoisted(() => ({
  listener: null as Listener | null,
  // What supabase's refreshSession / signInWithPassword do: emit the auth
  // event to the listener, then hand the session back to the store action.
  refreshSession: vi.fn(),
  signInWithPassword: vi.fn(),
}))
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
      refreshSession: auth.refreshSession,
      signInWithPassword: auth.signInWithPassword,
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

// supabase emits the event to the listener before the call returns.
const emittingThenReturning = (event: AuthChangeEvent, id: string) => async () => {
  const session = sessionFor(id)
  await auth.listener!(event, session)
  return { data: { user: session.user, session }, error: null }
}

const failCleanup = () =>
  resetMock.mockImplementation(() => {
    throw new Error('reset failed')
  })

const userAAnswer = () => queryClient.getQueryData(['onboarding', 'currentUser'])

beforeEach(async () => {
  resetMock.mockReset()
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

  it('adopts no identity while clearing the previous user\'s state fails', async () => {
    await signedInAs('user-a')
    resetMock.mockImplementationOnce(() => {
      throw new Error('reset failed')
    })

    await auth.listener!('SIGNED_IN', sessionFor('user-b'))

    expect(useAuthStore.getState().user).toBeNull()
    expect(useAuthStore.getState().session).toBeNull()
    expect(useAuthStore.getState().authError).toBeTruthy()
    expect(queryClient.getQueryData(['onboarding', 'currentUser'])).toEqual({
      id: 'user-a',
      onboardingCompleted: true,
    })

    // The store is now signed out, but the cleanup is still owed: the next
    // event for user B retries it before installing user B's session.
    resetMock.mockClear()
    await auth.listener!('TOKEN_REFRESHED', sessionFor('user-b'))

    expect(resetMock).toHaveBeenCalled()
    expect(queryClient.getQueryData(['onboarding', 'currentUser'])).toBeUndefined()
    expect(useAuthStore.getState().user?.id).toBe('user-b')
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

// Every store action that installs a session goes through the same gate as
// the listener: a cleanup failure the listener saw must not be undone by the
// action that emitted the event installing the session anyway.
describe('identity changes installed by a store action', () => {
  it('refreshSession installs no identity while clearing user A\'s state fails', async () => {
    await signedInAs('user-a')
    failCleanup()
    auth.refreshSession.mockImplementation(emittingThenReturning('TOKEN_REFRESHED', 'user-b'))

    await useAuthStore.getState().refreshSession()

    expect(useAuthStore.getState().user).toBeNull()
    expect(useAuthStore.getState().session).toBeNull()
    expect(useAuthStore.getState().authError).toBeTruthy()
    expect(userAAnswer()).toEqual({ id: 'user-a', onboardingCompleted: true })

    resetMock.mockReset()
    await useAuthStore.getState().refreshSession()

    expect(resetMock).toHaveBeenCalled()
    expect(userAAnswer()).toBeUndefined()
    expect(useAuthStore.getState().user?.id).toBe('user-b')
  })

  it('signIn rejects and installs no identity while clearing user A\'s state fails', async () => {
    await signedInAs('user-a')
    failCleanup()
    auth.signInWithPassword.mockImplementation(emittingThenReturning('SIGNED_IN', 'user-b'))

    await expect(useAuthStore.getState().signIn('b@example.com', 'pw')).rejects.toThrow(
      /could not be cleared/,
    )

    expect(useAuthStore.getState().user).toBeNull()
    expect(useAuthStore.getState().session).toBeNull()
    expect(userAAnswer()).toEqual({ id: 'user-a', onboardingCompleted: true })

    resetMock.mockReset()
    await useAuthStore.getState().signIn('b@example.com', 'pw')

    expect(userAAnswer()).toBeUndefined()
    expect(useAuthStore.getState().user?.id).toBe('user-b')
  })

  it('still clears user A\'s state after a failed refresh signed the store out without clearing it', async () => {
    await signedInAs('user-a')
    auth.refreshSession.mockResolvedValue({
      data: { session: null },
      error: new Error('refresh failed'),
    })

    await useAuthStore.getState().refreshSession()
    expect(useAuthStore.getState().user).toBeNull()
    expect(resetMock).not.toHaveBeenCalled()

    await auth.listener!('SIGNED_IN', sessionFor('user-b'))

    expect(resetMock).toHaveBeenCalled()
    expect(userAAnswer()).toBeUndefined()
    expect(useAuthStore.getState().user?.id).toBe('user-b')
  })
})
