/**
 * Dev sign-in (lib/devSignIn.ts): a locally minted token becomes the app's
 * supabase-js session, only in a dev server built with VITE_DEV_SIGN_IN=true,
 * and the link that carries it leaves the address bar at once.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const NOW = 1_800_000_000_000

function b64url(value: unknown): string {
  return btoa(JSON.stringify(value)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** A token shaped like control-plane's `auth mint-dev-token` (unsigned: nothing here verifies). */
function devToken(claims: Record<string, unknown>): string {
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(claims)}.c2lnbmF0dXJl`
}

const minted = {
  sub: 'dev-smoke-user',
  email: 'dev-smoke-user@dev.test',
  email_verified: true,
  role: 'authenticated',
  aud: 'authenticated',
  iat: NOW / 1000,
  exp: NOW / 1000 + 7200,
}

beforeEach(() => {
  vi.resetModules()
  vi.unstubAllEnvs()
})
afterEach(() => {
  vi.unstubAllEnvs()
  window.history.replaceState(null, '', '/')
})

describe('devSignInEnabled', () => {
  it('is off unless the build sets VITE_DEV_SIGN_IN=true', async () => {
    const { devSignInEnabled } = await import('../devSignIn')
    expect(devSignInEnabled()).toBe(false)
    vi.stubEnv('VITE_DEV_SIGN_IN', 'yes')
    expect(devSignInEnabled()).toBe(false)
    vi.stubEnv('VITE_DEV_SIGN_IN', 'true')
    expect(devSignInEnabled()).toBe(true)
  })

  it('is off in a production build even with the flag', async () => {
    vi.stubEnv('VITE_DEV_SIGN_IN', 'true')
    vi.stubEnv('DEV', false)
    const { devSignInEnabled } = await import('../devSignIn')
    expect(devSignInEnabled()).toBe(false)
  })
})

describe('sessionFromDevToken', () => {
  it('is a signed-in, verified, unexpired supabase-js session for the token', async () => {
    const { sessionFromDevToken, DEV_REFRESH_TOKEN } = await import('../devSignIn')
    const token = devToken(minted)
    const session = sessionFromDevToken(`  ${token}\n`, NOW)
    expect(session.access_token).toBe(token)
    expect(session.refresh_token).toBe(DEV_REFRESH_TOKEN)
    expect(session.expires_at).toBe(minted.exp)
    expect(session.expires_in).toBe(7200)
    expect(session.user.id).toBe('dev-smoke-user')
    expect(session.user.email).toBe('dev-smoke-user@dev.test')
    expect(session.user.email_confirmed_at).toBeTruthy()
    expect(session.user.is_anonymous).toBe(false)
  })

  it.each([
    ['not a JWT', 'nope', /three dot-separated parts/],
    ['no sub', devToken({ ...minted, sub: undefined }), /no sub/],
    ['no exp', devToken({ ...minted, exp: undefined }), /no exp/],
    ['expired', devToken({ ...minted, exp: NOW / 1000 - 1 }), /expired/],
  ])('refuses a token that could never work: %s', async (_name, token, message) => {
    const { sessionFromDevToken } = await import('../devSignIn')
    expect(() => sessionFromDevToken(token, NOW)).toThrow(message)
  })
})

describe('adoptDevToken', () => {
  it('stores the session, then restarts the app on the path', async () => {
    vi.stubEnv('VITE_DEV_SIGN_IN', 'true')
    const { adoptDevToken } = await import('../devSignIn')
    const write = vi.fn(async () => {})
    const restart = vi.fn()
    const token = devToken({ ...minted, exp: Math.floor(Date.now() / 1000) + 3600 })
    await adoptDevToken(token, '/chats', { write, restart })
    expect(write).toHaveBeenCalledWith(expect.objectContaining({ access_token: token }))
    expect(restart).toHaveBeenCalledWith('/chats')
  })

  it('stores nothing when dev sign-in is off', async () => {
    const { adoptDevToken } = await import('../devSignIn')
    const write = vi.fn(async () => {})
    const restart = vi.fn()
    await expect(adoptDevToken(devToken(minted), '/', { write, restart })).rejects.toThrow(/off in this build/)
    expect(write).not.toHaveBeenCalled()
    expect(restart).not.toHaveBeenCalled()
  })
})

describe('takeDevTokenFromUrl', () => {
  it('takes the token and scrubs the fragment, keeping the path and query', async () => {
    const { takeDevTokenFromUrl } = await import('../devSignIn')
    window.history.replaceState(null, '', '/chats?x=1#dev_sign_in=abc.def.ghi')
    expect(takeDevTokenFromUrl()).toBe('abc.def.ghi')
    expect(window.location.hash).toBe('')
    expect(window.location.pathname + window.location.search).toBe('/chats?x=1')
  })

  it('leaves any other fragment alone', async () => {
    const { takeDevTokenFromUrl } = await import('../devSignIn')
    window.history.replaceState(null, '', '/claim#rcl_token')
    expect(takeDevTokenFromUrl()).toBeNull()
    expect(window.location.hash).toBe('#rcl_token')
  })
})

describe('devSignInBoot', () => {
  it('does not touch the URL when dev sign-in is off', async () => {
    window.history.replaceState(null, '', '/#dev_sign_in=abc.def.ghi')
    const { bootDevSignIn } = await import('../devSignInBoot')
    expect(bootDevSignIn()).toBeNull()
    expect(window.location.hash).toBe('#dev_sign_in=abc.def.ghi')
  })
})
