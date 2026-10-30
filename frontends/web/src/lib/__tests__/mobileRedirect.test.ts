import { describe, expect, it } from 'vitest'
import {
  MOBILE_MAX_WIDTH,
  mobilePathFor,
  shouldRedirectToMobile,
  type MobileRedirectEnv,
} from '../mobileRedirect'

/** A phone: narrow and touch. */
function phone(over: Partial<MobileRedirectEnv> = {}): MobileRedirectEnv {
  return {
    pathname: '/',
    search: '',
    width: 390,
    coarsePointer: true,
    optedOutOfMobile: false,
    ...over,
  }
}

describe('shouldRedirectToMobile', () => {
  it('sends a phone to the mobile surface', () => {
    // The bug this exists to prevent: /m/* was unreachable without typing the
    // URL, so every phone got the full desktop ADE at 390px.
    expect(shouldRedirectToMobile(phone())).toBe(true)
    expect(shouldRedirectToMobile(phone({ pathname: '/project/abc' }))).toBe(true)
  })

  it('leaves a desktop browser alone', () => {
    expect(shouldRedirectToMobile(phone({ width: 1440, coarsePointer: false }))).toBe(
      false,
    )
  })

  it('requires BOTH narrow width and a coarse pointer', () => {
    // A narrow desktop window still has a mouse, so the desktop layout and its
    // resize handles remain usable — redirecting it would be wrong.
    expect(shouldRedirectToMobile(phone({ coarsePointer: false }))).toBe(false)
    // A touch device with a large screen (tablet, kiosk) fits the desktop shell.
    expect(shouldRedirectToMobile(phone({ width: 1024 }))).toBe(false)
  })

  it('treats the breakpoint as inclusive', () => {
    expect(shouldRedirectToMobile(phone({ width: MOBILE_MAX_WIDTH }))).toBe(true)
    expect(shouldRedirectToMobile(phone({ width: MOBILE_MAX_WIDTH + 1 }))).toBe(
      false,
    )
  })

  it('does not redirect when already on the mobile surface', () => {
    // Guards against a navigate loop: the effect re-runs on every pathname
    // change, so /m/* must be a fixed point.
    expect(shouldRedirectToMobile(phone({ pathname: '/m' }))).toBe(false)
    expect(shouldRedirectToMobile(phone({ pathname: '/m/chats' }))).toBe(false)
    expect(shouldRedirectToMobile(phone({ pathname: '/m/chats/abc' }))).toBe(false)
  })

  it('never hijacks auth, OAuth, or onboarding', () => {
    // These carry state in the URL that a redirect would drop, and onboarding
    // is a prerequisite for the mobile surface having anything to show.
    for (const pathname of [
      '/auth',
      '/auth/callback',
      '/auth/github/callback',
      '/oauth/consent',
      '/connections/oauth/callback',
      '/onboarding',
      '/reset-password',
      '/verify-email',
      '/upgrade',
    ]) {
      expect(shouldRedirectToMobile(phone({ pathname })), pathname).toBe(false)
    }
  })

  it('honours an explicit opt-out', () => {
    // "Show me the real app" is legitimate — a redirect with no exit is worse
    // than no redirect.
    expect(shouldRedirectToMobile(phone({ optedOutOfMobile: true }))).toBe(false)
  })

  it('does not treat desktop routes starting with m as mobile', () => {
    // `/migrate` is a desktop route; the fixed-point check must not match it,
    // or a phone would sit on the desktop shell believing it had arrived.
    expect(shouldRedirectToMobile(phone({ pathname: '/migrate' }))).toBe(true)
  })

  it('redirects the destination of the sign-in hop, not its origin', () => {
    // The strand this exists to prevent: the router commits `/auth` → `/`
    // about 17ms before `window.location` catches up. A caller keyed on the
    // router pathname but reading the live URL evaluated `/auth` — a
    // preserved path — so it declined to redirect, and because the router
    // pathname never changed again the check was never re-asked. The phone sat
    // on the desktop ADE with no recovery short of a reload.
    //
    // Passing the router's own location makes the post-sign-in `/` the thing
    // under test, which does redirect.
    expect(shouldRedirectToMobile(phone({ pathname: '/auth' }))).toBe(false)
    expect(shouldRedirectToMobile(phone({ pathname: '/' }))).toBe(true)
  })

  it('leaves the Apps console on a phone', () => {
    // The console's chrome collapses to an off-canvas nav at phone width, and
    // it has no /m/* twin: redirecting it made Apps and Observability
    // unreachable from a phone.
    for (const pathname of ['/forge', '/forge/observability', '/forge/p/my-app/observability/logs']) {
      expect(shouldRedirectToMobile(phone({ pathname })), pathname).toBe(false)
    }
    // A prefix match, not a substring one.
    expect(shouldRedirectToMobile(phone({ pathname: '/forgery' }))).toBe(true)
  })
})

describe('mobilePathFor', () => {
  it('sends a desktop link to its mobile twin, not always the chat list', () => {
    expect(mobilePathFor('/inbox')).toBe('/m/inbox')
    expect(mobilePathFor('/inbox/')).toBe('/m/inbox')
    // A run is a chat.
    expect(mobilePathFor('/workflows/runs/chat_123')).toBe('/m/chats/chat_123')
    expect(mobilePathFor('/workflows')).toBe('/m/workflows')
    expect(mobilePathFor('/workflows/library')).toBe('/m/workflows')
    // A workflow's page — where an automation link lands — is its viewer.
    expect(mobilePathFor('/workflows/library/builtin%3A%2F%2Fagent')).toBe('/m/workflows/builtin%3A%2F%2Fagent')
    // The run log has a phone twin; an automation id is resolved to its workflow.
    expect(mobilePathFor('/workflows/runs')).toBe('/m/runs')
    expect(mobilePathFor('/workflows/automations/t1')).toBe('/m/automations/t1')
    expect(mobilePathFor('/automations/t1')).toBe('/m/automations/t1')
    expect(mobilePathFor('/workflows/automations')).toBe('/m/workflows')
    expect(mobilePathFor('/settings')).toBe('/m/settings')
    expect(mobilePathFor('/settings/ai')).toBe('/m/settings')
    expect(mobilePathFor('/settings/machines/m_1')).toBe('/m/daemons')
    expect(mobilePathFor('/settings/environments/m_1')).toBe('/m/daemons')
  })

  it('keeps the way back when a billing visit has one (the claim page sends people here to subscribe)', () => {
    expect(mobilePathFor('/settings/billing', 'tab=plans&from=claim&returnTo=%2Fclaim')).toBe(
      '/m/settings?section=billing&returnTo=%2Fclaim',
    )
    // No way back, or one that leaves the app: the plain settings list, as before.
    expect(mobilePathFor('/settings/billing', 'tab=plans')).toBe('/m/settings')
    expect(mobilePathFor('/settings/billing', 'returnTo=%2F%2Fevil.example')).toBe('/m/settings')
    expect(mobilePathFor('/settings/billing', 'returnTo=https%3A%2F%2Fevil.example')).toBe('/m/settings')
  })

  it('lands everything without a twin on the chat list', () => {
    for (const pathname of ['/', '/project/abc', '/workflow/new']) {
      expect(mobilePathFor(pathname), pathname).toBe('/m/chats')
    }
  })
})
