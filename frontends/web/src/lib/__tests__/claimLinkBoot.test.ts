/**
 * The boot module wires the capture to two moments: the first load, and a
 * fragment-only change (a second link pasted into a tab already on /claim).
 * Both must scrub the address bar; only a DIFFERENT claim restarts the page.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const TOKEN = 'rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_'
const OTHER = 'rcl_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ'

beforeEach(() => {
  vi.resetModules()
  window.localStorage.clear()
})
afterEach(() => window.history.replaceState(null, '', '/'))

async function boot(url: string) {
  window.history.replaceState(null, '', url)
  const bootModule = await import('../claimLinkBoot')
  const claim = await import('../claimLink')
  return { bootModule, claim }
}

/** What the browser does when only the fragment changes. */
function pasteInPlace(onHashChange: (restart: () => void) => void, url: string, restart: () => void) {
  window.history.replaceState(null, '', url)
  onHashChange(restart)
}

describe('claimLinkBoot', () => {
  it('captures the link on the first load', async () => {
    const { claim } = await boot(`/claim#${TOKEN}`)
    expect(window.location.hash).toBe('')
    expect(claim.readPendingClaim()?.token).toBe(TOKEN)
  })

  it('scrubs the same link pasted again, without a restart', async () => {
    const { bootModule, claim } = await boot(`/claim#${TOKEN}`)
    const restart = vi.fn()
    pasteInPlace(bootModule.onClaimHashChange, `/claim#${TOKEN}`, restart)
    expect(window.location.hash).toBe('')
    expect(claim.readPendingClaim()?.token).toBe(TOKEN)
    expect(restart).not.toHaveBeenCalled()
  })

  it('a DIFFERENT link pasted into the open tab is captured, scrubbed, and restarts the page', async () => {
    const { bootModule, claim } = await boot(`/claim#${TOKEN}`)
    const restart = vi.fn()
    pasteInPlace(bootModule.onClaimHashChange, `/claim#${OTHER}`, restart)
    expect(window.location.hash).toBe('')
    expect(claim.readPendingClaim()?.token).toBe(OTHER)
    expect(restart).toHaveBeenCalledTimes(1)
  })

  it('a mistyped link pasted in is scrubbed and does not restart the page', async () => {
    const { bootModule } = await boot(`/claim#${TOKEN}`)
    const restart = vi.fn()
    pasteInPlace(bootModule.onClaimHashChange, '/claim#rcl_mistyped', restart)
    expect(window.location.hash).toBe('')
    expect(restart).not.toHaveBeenCalled()
  })

  it('the real hashchange event is wired to it', async () => {
    const { claim } = await boot(`/claim#${TOKEN}`)
    window.history.replaceState(null, '', `/claim#${OTHER}`)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    // jsdom does not implement reload; the capture having run is the proof.
    expect(window.location.hash).toBe('')
    expect(claim.readPendingClaim()?.token).toBe(OTHER)
  })
})
