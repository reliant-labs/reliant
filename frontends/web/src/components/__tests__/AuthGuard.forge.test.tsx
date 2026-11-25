/**
 * AuthGuard with forge sign-in (AUTH_PROVIDER "forge"): the route guard's
 * semantics. The auth pages are public; every other page needs an identity
 * and sends a signed-out visitor to /auth with the way back. No anonymous
 * sign-in, no email-confirmation stop, and no API-key screen: reliant-api's
 * /health says "apikey" in dev, and forge mode decides by identity alone.
 */
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const navigate = vi.fn()
let location = { pathname: '/inbox', href: '/inbox?tab=all' }
let search: { redirect?: string } = {}
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useSearch: () => search,
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) => select({ location, matches: [] }),
}))

vi.mock('@/lib/auth/forge-api', () => ({ isForgeAuth: () => true }))

let auth = { identity: null as unknown, signedIn: false, loading: false, initialized: true }
vi.mock('@/lib/auth/use-auth', () => ({ useAuth: () => auth }))

const initialize = vi.fn()
const signInAnonymously = vi.fn()
vi.mock('@/store/authStore', () => ({
  useAuthStore: (select?: (s: unknown) => unknown) => {
    const state = { initialize, signInAnonymously, user: null, loading: false, initialized: true }
    return select ? select(state) : state
  },
}))

vi.mock('@/hooks/useAuthMode', () => ({ useAuthMode: () => ({ authMode: 'apikey', loading: false }) }))
vi.mock('../ApiKeyLogin', () => ({ ApiKeyLogin: () => <div>api key login</div> }))
vi.mock('../Layout/LoadingSpinner', () => ({ LoadingSpinner: () => <div>loading</div> }))
vi.mock('@/lib/shell', () => ({ isForgeShell: () => false }))

import { AuthGuard } from '../AuthGuard'

const IDENTITY = { id: 'u1', email: 'a@example.com', provider: 'forge', isAnonymous: false, emailVerified: true }

beforeEach(() => {
  vi.clearAllMocks()
  location = { pathname: '/inbox', href: '/inbox?tab=all' }
  search = {}
  auth = { identity: null, signedIn: false, loading: false, initialized: true }
  window.history.replaceState({}, '', '/inbox?tab=all')
})

describe('AuthGuard — forge sign-in', () => {
  it('sends a signed-out visitor to /auth with the way back, never as a guest', () => {
    render(
      <AuthGuard requireAuth requireEmailVerification>
        <div>app</div>
      </AuthGuard>,
    )
    expect(navigate).toHaveBeenCalledWith({ href: '/auth?redirect=%2Finbox%3Ftab%3Dall' })
    expect(screen.queryByText('app')).toBeNull()
    expect(screen.queryByText('api key login')).toBeNull()
    expect(signInAnonymously).not.toHaveBeenCalled()
  })

  it('renders the page for an identity, with no email-confirmation stop', () => {
    auth = { identity: IDENTITY, signedIn: true, loading: false, initialized: true }
    render(
      <AuthGuard requireAuth requireEmailVerification>
        <div>app</div>
      </AuthGuard>,
    )
    expect(screen.getByText('app')).toBeTruthy()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('waits while the session is being restored', () => {
    auth = { identity: null, signedIn: false, loading: false, initialized: false }
    render(
      <AuthGuard requireAuth>
        <div>app</div>
      </AuthGuard>,
    )
    expect(screen.getByText('loading')).toBeTruthy()
    expect(initialize).toHaveBeenCalled()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('renders an auth page without a session', () => {
    location = { pathname: '/auth/verify', href: '/auth/verify?userID=u&code=c' }
    render(
      <AuthGuard requireAuth>
        <div>verify</div>
      </AuthGuard>,
    )
    expect(screen.getByText('verify')).toBeTruthy()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('shows the sign-in page to a signed-out visitor', () => {
    location = { pathname: '/auth', href: '/auth' }
    render(
      <AuthGuard requireAuth={false}>
        <div>sign in</div>
      </AuthGuard>,
    )
    expect(screen.getByText('sign in')).toBeTruthy()
  })

  it('sends a signed-in visitor on from the sign-in page, home for an off-site redirect', () => {
    location = { pathname: '/auth', href: '/auth' }
    search = { redirect: 'https://evil.example/' }
    auth = { identity: IDENTITY, signedIn: true, loading: false, initialized: true }
    render(
      <AuthGuard requireAuth={false}>
        <div>sign in</div>
      </AuthGuard>,
    )
    expect(navigate).toHaveBeenCalledWith({ to: '/', search: {} })
    expect(screen.queryByText('sign in')).toBeNull()
  })
})
