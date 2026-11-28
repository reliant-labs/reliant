import { describe, expect, it } from 'vitest'
import { defaultParseSearch, defaultStringifySearch } from '@tanstack/react-router'
import { mobileNewChatSearchSchema } from '@/routeSchemas'
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

  it('leaves Projects on a phone, and the retired /apps and /sites that redirect there (U6)', () => {
    // The drawer's Apps opens Projects narrowed to the apps; an old /apps link
    // redirects there. Handing either off would land the phone on the chat list.
    for (const pathname of ['/projects', '/o/acme/projects', '/apps', '/o/acme/apps', '/sites', '/o/acme/sites']) {
      expect(shouldRedirectToMobile(phone({ pathname, search: '?show=apps' })), pathname).toBe(false)
    }
    expect(shouldRedirectToMobile(phone({ pathname: '/projectsx' }))).toBe(true)
  })

  it('leaves Projects and a project page on a phone: one responsive page, no /m twin', () => {
    for (const pathname of ['/projects', '/projects/p-1', '/projects/p-1/workspaces', '/o/acme/projects/p-1/settings']) {
      expect(shouldRedirectToMobile(phone({ pathname })), pathname).toBe(false)
    }
    // The chat home filtered to a project is still the chat surface.
    expect(shouldRedirectToMobile(phone({ pathname: '/project/p-1' }))).toBe(true)
  })
})

describe('mobilePathFor', () => {
  it('carries an id over as the desktop path had it, and a return path in URLSearchParams bytes', () => {
    expect(mobilePathFor('/workflows/runs/a%2Fb')).toBe('/m/chats/a%2Fb')
    expect(mobilePathFor('/workflows/runs/a%252Fb')).toBe('/m/chats/a%252Fb')
    expect(mobilePathFor('/workflows/automations/t%3A1')).toBe('/m/automations/t%3A1')
    expect(mobilePathFor('/settings/billing', '?returnTo=%2Fclaim%3Fapp%3Dshop%26x%3D1%23rcl')).toBe(
      `/m/settings?${new URLSearchParams({ section: 'billing', returnTo: '/claim?app=shop&x=1#rcl' })}`,
    )
  })

  it('sends a desktop link to its mobile twin, not always the chat list', () => {
    expect(mobilePathFor('/inbox')).toBe('/m/inbox')
    expect(mobilePathFor('/inbox/')).toBe('/m/inbox')
    // A run is a chat.
    expect(mobilePathFor('/workflows/runs/chat_123')).toBe('/m/chats/chat_123')
    // A chat, a thread of it (the phone picks threads in its own sheet), a draft; either URL form.
    expect(mobilePathFor('/c/chat_123')).toBe('/m/chats/chat_123')
    expect(mobilePathFor('/o/acme/c/chat_123/t/thread_9')).toBe('/m/chats/chat_123')
    expect(mobilePathFor('/c/a%2Fb')).toBe('/m/chats/a%2Fb')
    expect(mobilePathFor('/o/acme/new')).toBe('/m/new')
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
    // A section with a phone screen opens it; one without lands on the list.
    expect(mobilePathFor('/settings/ai')).toBe('/m/settings?section=ai')
    expect(mobilePathFor('/settings/notifications')).toBe('/m/settings?section=notifications')
    expect(mobilePathFor('/settings/git-connections')).toBe('/m/settings?section=github')
    expect(mobilePathFor('/settings/branches')).toBe('/m/settings?section=workspace')
    expect(mobilePathFor('/settings/mcp')).toBe('/m/settings')
    expect(mobilePathFor('/settings/machines/m_1')).toBe('/m/daemons')
    expect(mobilePathFor('/settings/environments/m_1')).toBe('/m/daemons')
  })

  it('keeps the way back when a billing visit has one (the claim page sends people here to subscribe)', () => {
    expect(mobilePathFor('/settings/billing', 'tab=plans&from=claim&returnTo=%2Fclaim')).toBe(
      '/m/settings?section=billing&returnTo=%2Fclaim',
    )
    // No way back, or one that leaves the app: billing, with no way back.
    expect(mobilePathFor('/settings/billing', 'tab=plans')).toBe('/m/settings?section=billing')
    expect(mobilePathFor('/settings/billing', 'returnTo=%2F%2Fevil.example')).toBe('/m/settings?section=billing')
    expect(mobilePathFor('/settings/billing', 'returnTo=https%3A%2F%2Fevil.example')).toBe('/m/settings?section=billing')
  })

  it('opens billing on the App hosting card for a hosting link, with the org it was for', () => {
    // "Set up hosting billing" after creating an org, a deploy queued on billing.
    expect(mobilePathFor('/settings/billing', 'tab=hosting&org=org-b')).toBe(
      '/m/settings?section=billing&tab=hosting&org=org-b',
    )
    // A hosting checkout's return: the marker is the desktop checkout window's,
    // not the phone's, so it is not carried.
    expect(mobilePathFor('/settings/billing', 'tab=hosting&checkout=success')).toBe(
      '/m/settings?section=billing&tab=hosting',
    )
    expect(mobilePathFor('/settings/billing', 'tab=hosting&from=forge&returnTo=%2Fforge%2Fenv%2Fprod')).toBe(
      '/m/settings?section=billing&returnTo=%2Fforge%2Fenv%2Fprod&tab=hosting',
    )
    // Only the hosting tab is kept: the phone's billing screen has no others.
    expect(mobilePathFor('/settings/billing', 'tab=plans&org=org-b')).toBe('/m/settings?section=billing')
  })

  it('lands everything without a twin on the chat list', () => {
    for (const pathname of ['/', '/workflow/new', '/project/abc/settings']) {
      expect(mobilePathFor(pathname), pathname).toBe('/m/chats')
    }
  })

  // The project switcher's pick off a console page lands on `/project/<id>`:
  // the phone's list scoped to it, never a bare list that drops the pick.
  it("opens a project's chat home as the chat list scoped to it", () => {
    expect(mobilePathFor('/project/abc')).toBe('/m/chats?projects=abc')
    expect(mobilePathFor('/o/acme/project/abc/')).toBe('/m/chats?projects=abc')
    expect(mobilePathFor('/project/a%2Fb')).toBe('/m/chats?projects=a%2Fb')
    // The path names the project: a scope carried with it is not the page's.
    expect(mobilePathFor('/project/abc', '?projects=all&focus=app%3Ashop')).toBe('/m/chats?projects=abc')
  })

  it.each(['p1', '123', 'true', 'null', 'p1,p2'])('round-trips scope and draft strings through the router: %s', (id) => {
    const parseDestination = (url: string) => defaultParseSearch(new URL(url, 'http://phone').search)
    expect(parseDestination(mobilePathFor(`/project/${encodeURIComponent(id)}`))).toEqual({ projects: id })
    const scope = { projects: id, focus: id }
    expect(parseDestination(mobilePathFor('/', defaultStringifySearch(scope)))).toEqual(scope)
    // Raw external links and router-generated links both remain valid drafts.
    for (const search of [new URLSearchParams({ project: id, machine: id, ws: id }).toString(), defaultStringifySearch({ project: id, machine: id, ws: id })]) {
      const parsed = parseDestination(mobilePathFor('/new', search))
      expect(mobileNewChatSearchSchema.parse(parsed)).toMatchObject({ project: id, machine: id, worktreeId: id })
    }
  })

  it("opens a draft with its project, machine and workspace", () => {
    expect(mobilePathFor('/new', '?project=p1&machine=d1&ws=w1')).toBe('/m/new?project=p1&machine=d1&worktreeId=w1')
    expect(mobilePathFor('/o/acme/new', 'project=p1&app=shop')).toBe('/m/new?project=p1')
    expect(mobilePathFor('/new', 'project=p1&projects=p2')).toBe('/m/new?project=p1&projects=p2')
  })

  it('keeps the shell scope on every twin', () => {
    expect(mobilePathFor('/', '?projects=p1')).toBe('/m/chats?projects=p1')
    expect(mobilePathFor('/', 'projects=all&focus=p2')).toBe('/m/chats?projects=all&focus=p2')
    expect(mobilePathFor('/inbox', 'projects=p1%2Cp2&tour=x')).toBe('/m/inbox?projects=p1%2Cp2')
    expect(mobilePathFor('/c/chat_1', 'projects=p1&panel=files')).toBe('/m/chats/chat_1?projects=p1')
    expect(mobilePathFor('/settings/ai', 'projects=p1')).toBe('/m/settings?section=ai&projects=p1')
    expect(mobilePathFor('/settings/billing', 'returnTo=%2Fclaim&projects=all')).toBe(
      '/m/settings?section=billing&returnTo=%2Fclaim&projects=all',
    )
    expect(mobilePathFor('/workflows/runs/r%2F1', 'projects=p1')).toBe('/m/chats/r%2F1?projects=p1')
  })
})
