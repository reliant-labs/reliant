/**
 * Sign-in reaches browser telemetry. The runtime's one-time `user_identified`
 * event (and the userId on every span) fires only from setTelemetryUser, which
 * the store calls from its auth-state listener — so a sign-in that skipped it
 * would leave the funnel with no identified users at all.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AuthChangeEvent, Session } from '@supabase/supabase-js'

type Listener = (event: AuthChangeEvent, session: Session | null) => Promise<void>

const auth = vi.hoisted(() => ({ listener: null as Listener | null }))
const telemetry = vi.hoisted(() => ({ setTelemetryUserId: vi.fn() }))

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

vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: {
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/lib/telemetry', () => telemetry)
vi.mock('@/lib/query-client', () => ({ queryClient: { clear: vi.fn() } }))

const { useAuthStore } = await import('@/store/authStore')

const USER = { id: 'u1', email: 'someone@example.com', is_anonymous: false }
const SESSION = { access_token: 't', user: USER } as unknown as Session

beforeEach(async () => {
  localStorage.clear()
  useAuthStore.setState({ user: null, session: null, initialized: false })
  await useAuthStore.getState().initialize()
  telemetry.setTelemetryUserId.mockClear()
})

describe('auth state reaches telemetry', () => {
  it('names the user, by id only, on sign-in', async () => {
    expect(auth.listener).not.toBeNull()
    await auth.listener!('SIGNED_IN', SESSION)
    expect(telemetry.setTelemetryUserId).toHaveBeenCalledWith({ id: 'u1' })
  })

  it('clears the user on sign-out', async () => {
    await auth.listener!('SIGNED_OUT', null)
    expect(telemetry.setTelemetryUserId).toHaveBeenCalledWith(null)
  })
})
