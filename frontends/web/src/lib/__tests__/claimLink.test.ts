/**
 * The claim link: the token's journey from the address bar to storage.
 *
 * The property that matters is that the fragment is GONE from the URL before
 * anything else can read it (telemetry stamps `location.href`; the router
 * keeps the location) while the token survives for the page that needs it.
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import {
  captureClaimLinkFromUrl,
  clearPendingClaim,
  isClaimPath,
  isClaimReturnTo,
  parseClaimToken,
  readPendingClaim,
  resetClaimLinkMemoryForTests,
  updatePendingClaim,
} from '../claimLink'

const TOKEN = 'rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_'
const OTHER = 'rcl_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ'

function open(path: string) {
  window.history.replaceState(null, '', path)
}

beforeEach(() => {
  window.localStorage.clear()
  resetClaimLinkMemoryForTests()
  open('/')
})
afterEach(() => open('/'))

describe('parseClaimToken', () => {
  it('accepts the rcl_ token with or without the leading #', () => {
    expect(parseClaimToken(`#${TOKEN}`)).toBe(TOKEN)
    expect(parseClaimToken(TOKEN)).toBe(TOKEN)
  })

  it('rejects anything that is not a claim token', () => {
    for (const hash of ['', '#', '#rcl_', '#rcl_short', '#abc', '#rcl_has space-in-it-xxxxxxxx', '#rcl_ok_but_trailing/slash_xxxx', '#?rcl_xxxxxxxxxxxxxxxxxxxx']) {
      expect(parseClaimToken(hash), hash).toBeNull()
    }
  })
})

describe('captureClaimLinkFromUrl', () => {
  it('moves the token into storage and removes the fragment from the address bar', () => {
    open(`/claim#${TOKEN}`)

    captureClaimLinkFromUrl()

    expect(window.location.hash).toBe('')
    expect(window.location.href).not.toContain('rcl_')
    expect(readPendingClaim()?.token).toBe(TOKEN)
  })

  it('keeps the query string and the path when it strips the fragment', () => {
    open(`/claim?utm_source=agent#${TOKEN}`)
    captureClaimLinkFromUrl()
    expect(window.location.pathname + window.location.search).toBe('/claim?utm_source=agent')
  })

  it('scrubs a malformed fragment too, and stores nothing', () => {
    open('/claim#rcl_mistyped')
    captureClaimLinkFromUrl()
    expect(window.location.hash).toBe('')
    expect(readPendingClaim()).toBeNull()
  })

  it('leaves every other page alone, including one that carries a #rcl_ fragment', () => {
    open(`/apps#${TOKEN}`)
    captureClaimLinkFromUrl()
    expect(window.location.hash).toBe(`#${TOKEN}`)
    expect(readPendingClaim()).toBeNull()
  })

  it('accepts a trailing slash on the claim path', () => {
    open(`/claim/#${TOKEN}`)
    captureClaimLinkFromUrl()
    expect(readPendingClaim()?.token).toBe(TOKEN)
  })

  it('does nothing on /claim without a fragment (a reload keeps the stored claim)', () => {
    open(`/claim#${TOKEN}`)
    captureClaimLinkFromUrl()
    open('/claim')
    captureClaimLinkFromUrl()
    expect(readPendingClaim()?.token).toBe(TOKEN)
  })

  it('opening the SAME link again keeps the progress already made on it', () => {
    open(`/claim#${TOKEN}`)
    captureClaimLinkFromUrl()
    updatePendingClaim({ anonUserId: 'anon-1', project: 'storefront', opened: true })

    open(`/claim#${TOKEN}`)
    captureClaimLinkFromUrl()

    expect(readPendingClaim()).toMatchObject({ token: TOKEN, anonUserId: 'anon-1', project: 'storefront', opened: true })
  })

  it('a DIFFERENT link replaces the pending claim and its progress', () => {
    open(`/claim#${TOKEN}`)
    captureClaimLinkFromUrl()
    updatePendingClaim({ anonUserId: 'anon-1' })

    open(`/claim#${OTHER}`)
    captureClaimLinkFromUrl()

    const pending = readPendingClaim()
    expect(pending?.token).toBe(OTHER)
    expect(pending?.anonUserId).toBeUndefined()
  })

  it('works when storage refuses writes (private mode): the claim lives in memory', () => {
    const setItem = window.localStorage.setItem
    const getItem = window.localStorage.getItem
    window.localStorage.setItem = () => {
      throw new Error('QuotaExceededError')
    }
    window.localStorage.getItem = () => {
      throw new Error('SecurityError')
    }
    try {
      open(`/claim#${TOKEN}`)
      captureClaimLinkFromUrl()
      expect(window.location.hash).toBe('')
      expect(readPendingClaim()?.token).toBe(TOKEN)
    } finally {
      window.localStorage.setItem = setItem
      window.localStorage.getItem = getItem
    }
  })
})

describe('the pending claim', () => {
  it('is dropped once too old to be claimable', () => {
    open(`/claim#${TOKEN}`)
    captureClaimLinkFromUrl()
    const elevenDays = Date.now() + 11 * 24 * 60 * 60 * 1000
    expect(readPendingClaim(elevenDays)).toBeNull()
    expect(readPendingClaim()).toBeNull() // and gone, not just hidden
  })

  it('ignores a stored record whose token is not a claim token', () => {
    window.localStorage.setItem('reliant:claim', JSON.stringify({ token: 'nope', savedAt: Date.now() }))
    expect(readPendingClaim()).toBeNull()
  })

  it('ignores corrupt storage', () => {
    window.localStorage.setItem('reliant:claim', '{not json')
    expect(readPendingClaim()).toBeNull()
  })

  it('clearPendingClaim forgets it everywhere', () => {
    open(`/claim#${TOKEN}`)
    captureClaimLinkFromUrl()
    clearPendingClaim()
    expect(readPendingClaim()).toBeNull()
    expect(window.localStorage.getItem('reliant:claim')).toBeNull()
  })

  it('updatePendingClaim is a no-op without a claim', () => {
    updatePendingClaim({ anonUserId: 'x' })
    expect(readPendingClaim()).toBeNull()
  })
})

describe('path helpers', () => {
  it('isClaimPath matches /claim and /claim/ only', () => {
    expect(isClaimPath('/claim')).toBe(true)
    expect(isClaimPath('/claim/')).toBe(true)
    expect(isClaimPath('/claimed')).toBe(false)
    expect(isClaimPath('/apps')).toBe(false)
  })

  it("isClaimReturnTo matches the claim page's own returnTo", () => {
    expect(isClaimReturnTo('/claim')).toBe(true)
    expect(isClaimReturnTo('/settings')).toBe(false)
    expect(isClaimReturnTo(undefined)).toBe(false)
  })
})

describe('captureClaimLinkFromUrl — what it reports', () => {
  it('returns the token only when it stored a DIFFERENT claim', () => {
    open(`/claim#${TOKEN}`)
    expect(captureClaimLinkFromUrl()).toBe(TOKEN)

    open(`/claim#${TOKEN}`) // the same link again
    expect(captureClaimLinkFromUrl()).toBeNull()

    open(`/claim#${OTHER}`)
    expect(captureClaimLinkFromUrl()).toBe(OTHER)
  })

  it('returns null for a malformed or off-page fragment', () => {
    open('/claim#rcl_mistyped')
    expect(captureClaimLinkFromUrl()).toBeNull()
    open(`/apps#${TOKEN}`)
    expect(captureClaimLinkFromUrl()).toBeNull()
  })
})
