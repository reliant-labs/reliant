/**
 * Which stage of the settled flow a browser is at, from who is signed in and
 * the pending claim. Every row of the table in claimSituation.ts.
 */
import { describe, expect, it } from 'vitest'
import { claimSituation } from '../claimSituation'

const released = { anonUserId: 'anon-1' }
const fresh = {}

describe('claimSituation', () => {
  it('nobody signed in: open the session', () => {
    expect(claimSituation(null, fresh)).toBe('signed-out')
    expect(claimSituation(null, released)).toBe('signed-out')
  })

  it("the claim's own user, still anonymous: go on to the upgrade", () => {
    expect(claimSituation({ id: 'anon-1', is_anonymous: true }, released)).toBe('resume-upgrade')
  })

  it("the claim's own user, upgraded in place (same id): finish", () => {
    expect(claimSituation({ id: 'anon-1', is_anonymous: false }, released)).toBe('finish')
  })

  it('a different anonymous user: warn, then replace', () => {
    expect(claimSituation({ id: 'someone-else', is_anonymous: true }, released)).toBe('replace-anonymous')
    expect(claimSituation({ id: 'someone-else', is_anonymous: true }, fresh)).toBe('replace-anonymous')
  })

  it('a different, upgraded user: the existing-account branch', () => {
    expect(claimSituation({ id: 'someone-else', is_anonymous: false }, released)).toBe('existing-account')
    expect(claimSituation({ id: 'someone-else' }, fresh)).toBe('existing-account')
  })

  it('a signed-in user is never treated as the claim\'s own before a session was released', () => {
    // No anonUserId yet, so no id can match it (not even an empty one).
    expect(claimSituation({ id: '', is_anonymous: false }, fresh)).toBe('existing-account')
  })
})
