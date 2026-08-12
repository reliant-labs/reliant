/**
 * `main.tsx` preloads Monaco at startup, before React mounts. Monaco is
 * fetched from a CDN (see `lib/monacoManager`), so on a phone that is multiple
 * megabytes for something no `/m/*` screen ever renders — mobile diffs and file
 * views go through the Prism-based `LightweightDiffViewer`.
 *
 * The gate uses `surfaceForPath(window.location.pathname)` rather than the
 * router, because it runs before the router exists. These tests pin the
 * predicate against the real mobile URLs, since a wrong answer here is either
 * a silent multi-MB download on mobile or a missing editor on desktop —
 * neither of which surfaces as a test failure anywhere else.
 */

import { describe, expect, it } from 'vitest'
import { shouldPreloadMonaco } from '../monacoPreload'

describe('Monaco preload gate', () => {
  it('skips the preload on every mobile route', () => {
    // Deep links matter: a notification or a home-screen shortcut can open any
    // of these directly as the very first navigation.
    for (const path of [
      '/m',
      '/m/chats',
      '/m/chats/abc-123',
      '/m/new',
      '/m/daemons',
      '/m/daemons/daemon-1',
      '/m/account',
    ]) {
      expect(shouldPreloadMonaco(path), `${path} should skip Monaco`).toBe(
        false,
      )
    }
  })

  it('still preloads on desktop routes', () => {
    // The workflow builder and file viewers depend on Monaco being warm; this
    // is the regression that would make the desktop editor feel slow.
    for (const path of [
      '/',
      '/project/abc',
      '/workflow/build',
      '/settings',
      '/onboarding',
    ]) {
      expect(shouldPreloadMonaco(path), `${path} should preload Monaco`).toBe(
        true,
      )
    }
  })

  it('preloads for desktop routes that merely start with the letter m', () => {
    // A bare `startsWith('/m')` would strip Monaco from these.
    expect(shouldPreloadMonaco('/migrate')).toBe(true)
    expect(shouldPreloadMonaco('/mcp')).toBe(true)
  })

  it('skips the preload on unauthenticated entry routes', () => {
    // /oauth/consent is the one that matters most: it is reached mid-OAuth
    // from a third-party client, often on a phone, by someone who has never
    // opened the app. A multi-MB CDN fetch for a consent form is pure latency
    // in someone else's flow.
    for (const path of [
      '/oauth/consent',
      '/auth',
      '/auth/callback',
      '/auth/github/callback',
      '/reset-password',
      '/verify-email',
      '/upgrade',
    ]) {
      expect(shouldPreloadMonaco(path), `${path} should skip Monaco`).toBe(
        false,
      )
    }
  })

  it('matches excluded prefixes on segment boundaries only', () => {
    // `startsWith('/upgrade')` would wrongly strip Monaco from a future
    // desktop route whose name merely begins with an excluded prefix.
    expect(shouldPreloadMonaco('/upgraded-plans')).toBe(true)
    expect(shouldPreloadMonaco('/authoring')).toBe(true)
  })
})
