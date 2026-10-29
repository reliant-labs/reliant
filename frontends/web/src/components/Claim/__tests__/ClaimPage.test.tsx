/**
 * The /claim page, every state, against a mocked control plane (W5a — the
 * server half of OpenClaimSession/CompleteClaim — is not built yet).
 *
 * The flow under test is the settled one (design §4, path 1):
 *   preview (no sign-in)  →  OpenClaimSession  →  adopt the session
 *   →  /upgrade?returnTo=/claim  →  [back on /claim, now upgraded]  →  CompleteClaim  →  /apps
 *
 * Auth is a controllable store; the control plane is the real generated client
 * over a mock transport, so what is asserted is the traffic, not an internal.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Code, ConnectError } from '@connectrpc/connect'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useLocation: () => ({ pathname: '/claim' }),
}))

vi.mock('@/services/controlPlane/config', () => ({
  CONTROL_PLANE_API_URL: 'https://admin.example.test',
  hasControlPlane: true,
}))

vi.mock('@/lib/logger', () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } }))

const track = vi.fn()
vi.mock('@/lib/analytics', () => ({ trackEvent: (...a: unknown[]) => track(...a) }))

const refreshSession = vi.fn(async () => ({ data: {}, error: null }))
vi.mock('@/lib/supabase', () => ({ supabase: { auth: { refreshSession: () => refreshSession() } } }))

const hardNavigate = vi.fn()
vi.mock('@/lib/hardNavigate', () => ({ hardNavigate: (p: string) => hardNavigate(p) }))

const switchToJoinedOrg = vi.fn(async (_orgId: string) => undefined)
vi.mock('@/services/controlPlane/orgs', () => ({ switchToJoinedOrg: (id: string) => switchToJoinedOrg(id) }))

const invalidateQueries = vi.fn(async (_arg?: unknown) => undefined)
vi.mock('@/lib/query-client', () => ({ queryClient: { invalidateQueries: (arg: unknown) => invalidateQueries(arg) } }))

// ── a controllable auth store ──────────────────────────────────────────────
type TestUser = { id: string; email?: string; is_anonymous?: boolean } | null
const auth = {
  user: null as TestUser,
  initialized: true,
  loading: false,
  initialize: vi.fn(),
  adoptSession: vi.fn(),
  signOut: vi.fn(),
}
vi.mock('@/store/authStore', () => {
  const useAuthStore = Object.assign(
    (selector: (s: typeof auth) => unknown) => selector(auth),
    { getState: () => auth },
  )
  return { useAuthStore }
})

import { ClaimableState } from '@/gen/controlplane/services/claimable/v1/claimable_pb'
import { captureClaimLinkFromUrl, readPendingClaim, resetClaimLinkMemoryForTests, updatePendingClaim } from '@/lib/claimLink'
import { setTransport } from '@/lib/connect'
import { mockTransport, type MockHandlers } from '@/lib/test-utils'
import { ClaimPage } from '../ClaimPage'

const TOKEN = 'rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_'
const EXPIRES = new Date(Date.now() + 70 * 3600_000)
const PURGE = new Date(Date.now() + (70 + 168) * 3600_000)

const LIVE = {
  state: ClaimableState.LIVE,
  claimable: true,
  project: 'storefront',
  env: 'prod',
  hostnames: ['amber-quokka.reliantpreview.dev'],
  expiresAt: timestampFromDate(EXPIRES),
  purgeAfter: timestampFromDate(PURGE),
}

function refused(reason: string, code: Code = Code.FailedPrecondition) {
  return new ConnectError('refused', code, new Headers({ 'x-forge-error-reason': reason }))
}

function arrive(handlers: MockHandlers) {
  setTransport(mockTransport(handlers))
  return render(<ClaimPage />)
}

function openLink() {
  window.history.replaceState(null, '', `/claim#${TOKEN}`)
  captureClaimLinkFromUrl()
}

const rpcNames = (fn: ReturnType<typeof vi.fn>) => fn.mock.calls.map((c) => c[0])

beforeEach(() => {
  vi.clearAllMocks()
  window.localStorage.clear()
  resetClaimLinkMemoryForTests()
  auth.user = null
  auth.initialized = true
  auth.loading = false
  auth.signOut.mockResolvedValue(undefined)
  auth.adoptSession.mockResolvedValue({ id: 'anon-1', is_anonymous: true })
  openLink()
})

afterEach(() => {
  setTransport(null)
  window.history.replaceState(null, '', '/')
})

describe('arriving', () => {
  it('shows what is being claimed BEFORE anyone signs in, and calls only GetClaim', async () => {
    const calls = vi.fn()
    arrive({
      'ClaimableService.GetClaim': (input) => (calls('GetClaim', input), LIVE),
      'ClaimableService.OpenClaimSession': () => (calls('OpenClaimSession'), {}),
    })

    expect(await screen.findByTestId('claim-site')).toHaveAttribute('data-site-state', 'live')
    expect(screen.getByTestId('claim-site-name')).toHaveTextContent('storefront')
    expect(screen.getByTestId('claim-site-host')).toHaveTextContent('amber-quokka.reliantpreview.dev')
    expect(screen.getByTestId('claim-expiry')).toHaveTextContent(/goes offline/i)
    expect(screen.getByTestId('claim-submit')).toHaveTextContent("Claim it, it's free")
    expect(rpcNames(calls)).toEqual(['GetClaim'])
    expect(calls.mock.calls[0][1]).toMatchObject({ claimToken: TOKEN })
  })

  it('never has the token in the URL once the page is up', async () => {
    arrive({ 'ClaimableService.GetClaim': { response: LIVE } })
    await screen.findByTestId('claim-site')
    expect(window.location.href).not.toContain('rcl_')
  })

  it('says a suspended site is offline, with its deletion date', async () => {
    arrive({ 'ClaimableService.GetClaim': { response: { ...LIVE, state: ClaimableState.SUSPENDED } } })
    expect(await screen.findByTestId('claim-site')).toHaveAttribute('data-site-state', 'suspended')
    expect(screen.getByTestId('claim-expiry')).toHaveTextContent(/offline now/i)
    expect(screen.getByTestId('claim-expiry')).toHaveTextContent(/deleted/i)
    // An offline site is not a link.
    expect(screen.getByTestId('claim-site-host').querySelector('a')).toBeNull()
  })

  it('shows the preview once for the funnel, not on every reload', async () => {
    const { unmount } = arrive({ 'ClaimableService.GetClaim': { response: LIVE } })
    await screen.findByTestId('claim-site')
    unmount()
    render(<ClaimPage />)
    await screen.findByTestId('claim-site')

    const opened = track.mock.calls.filter((c) => c[0] === 'claim_link_opened')
    expect(opened).toHaveLength(1)
    expect(opened[0][1]).toMatchObject({ result: 'live', situation: 'signed-out' })
  })
})

describe('every refusal the proto names', () => {
  it.each([
    ['claimable_claimed', Code.FailedPrecondition, /already been claimed/i],
    ['claimable_expired', Code.FailedPrecondition, /has expired/i],
    ['claimable_taken_down', Code.FailedPrecondition, /was removed/i],
    ['claimable_not_found', Code.NotFound, /can't find this claim/i],
  ] as const)('%s as an error from GetClaim', async (reason, code, title) => {
    arrive({ 'ClaimableService.GetClaim': { error: refused(reason, code) } })
    const card = await screen.findByTestId(`claim-refused-${reason}`)
    expect(card).toHaveTextContent(title)
    // Nothing to claim: no button, and no session was asked for.
    expect(screen.queryByTestId('claim-submit')).toBeNull()
  })

  it.each([
    [ClaimableState.CLAIMED, 'claimable_claimed'],
    [ClaimableState.EXPIRED, 'claimable_expired'],
    [ClaimableState.TAKEN_DOWN, 'claimable_taken_down'],
  ] as const)('state %s as an OK answer', async (state, reason) => {
    arrive({ 'ClaimableService.GetClaim': { response: { ...LIVE, state, claimable: false } } })
    expect(await screen.findByTestId(`claim-refused-${reason}`)).toBeInTheDocument()
  })

  it('claimable_session_released after "Claim it": continue elsewhere, or sign in', async () => {
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.OpenClaimSession': { error: refused('claimable_session_released') },
    })
    fireEvent.click(await screen.findByTestId('claim-submit'))

    const card = await screen.findByTestId('claim-refused-claimable_session_released')
    expect(card).toHaveTextContent(/another browser/i)
    expect(card.querySelector('a')?.getAttribute('href')).toBe('/auth?redirect=%2Fclaim')
    expect(auth.adoptSession).not.toHaveBeenCalled()
  })

  it('a link opened without its fragment explains what to do', async () => {
    window.localStorage.clear()
    resetClaimLinkMemoryForTests()
    arrive({})
    expect(await screen.findByText(/Open the claim link from your agent/i)).toBeInTheDocument()
  })
})

describe('"Claim it, it\'s free" (a new human)', () => {
  it('releases the session once, installs it, then sends them to the existing upgrade flow', async () => {
    const order: string[] = []
    auth.adoptSession.mockImplementation(async (s) => {
      order.push(`adopt:${s.accessToken}`)
      return { id: 'anon-1', is_anonymous: true }
    })
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.OpenClaimSession': () => (order.push('open'), { accessToken: 'a.b.c', refreshToken: 'r1', expiresIn: 3600n }),
    })

    fireEvent.click(await screen.findByTestId('claim-submit'))

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/upgrade', search: { returnTo: '/claim' } }))
    expect(order).toEqual(['open', 'adopt:a.b.c'])
    // The browser now remembers WHOSE session it holds, so the return can tell
    // "the claim's own user" from anyone else.
    expect(readPendingClaim()?.anonUserId).toBe('anon-1')
    expect(track).toHaveBeenCalledWith('claim_started', { situation: 'signed-out' })
  })

  it('retries a failed install with the SAME session, not a second open (the server releases it once)', async () => {
    const open = vi.fn(() => ({ accessToken: 'a.b.c', refreshToken: 'r1', expiresIn: 3600n }))
    auth.adoptSession.mockRejectedValueOnce(new Error('storage full'))
    arrive({ 'ClaimableService.GetClaim': { response: LIVE }, 'ClaimableService.OpenClaimSession': open })

    fireEvent.click(await screen.findByTestId('claim-submit'))
    expect(await screen.findByTestId('claim-error')).toHaveTextContent(/could not sign this browser in/i)
    expect(track).toHaveBeenCalledWith('claim_failed', expect.objectContaining({ stage: 'adopt_session' }))

    fireEvent.click(screen.getByTestId('claim-submit'))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/upgrade', search: { returnTo: '/claim' } }))
    expect(open).toHaveBeenCalledTimes(1)
    expect(auth.adoptSession).toHaveBeenCalledTimes(2)
  })

  it('a transport failure on open stays on the page with a retry, and records why', async () => {
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.OpenClaimSession': { error: new ConnectError('down', Code.Unavailable) },
    })
    fireEvent.click(await screen.findByTestId('claim-submit'))
    expect(await screen.findByTestId('claim-error')).toBeInTheDocument()
    expect(screen.getByTestId('claim-submit')).toBeEnabled()
    expect(track).toHaveBeenCalledWith('claim_failed', { stage: 'open_session', reason: 'unavailable' })
    expect(navigate).not.toHaveBeenCalled()
  })

  it('a browser that left mid-upgrade resumes it without asking the server for the session again', async () => {
    updatePendingClaim({ anonUserId: 'anon-1' })
    auth.user = { id: 'anon-1', is_anonymous: true }
    const open = vi.fn()
    arrive({ 'ClaimableService.GetClaim': { response: LIVE }, 'ClaimableService.OpenClaimSession': open })

    const button = await screen.findByTestId('claim-submit')
    expect(button).toHaveTextContent('Continue claiming')
    fireEvent.click(button)

    expect(navigate).toHaveBeenCalledWith({ to: '/upgrade', search: { returnTo: '/claim' } })
    expect(open).not.toHaveBeenCalled()
  })

  it('warns before replacing a DIFFERENT anonymous account, then signs it out first', async () => {
    auth.user = { id: 'someone-else', is_anonymous: true }
    const order: string[] = []
    auth.signOut.mockImplementation(async () => void order.push('signOut'))
    auth.adoptSession.mockImplementation(async () => (order.push('adopt'), { id: 'anon-1', is_anonymous: true }))
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.OpenClaimSession': () => (order.push('open'), { accessToken: 'a', refreshToken: 'r', expiresIn: 1n }),
    })

    expect(await screen.findByTestId('claim-replace-anonymous')).toHaveTextContent(/temporary account/i)
    fireEvent.click(screen.getByTestId('claim-submit'))

    await waitFor(() => expect(navigate).toHaveBeenCalled())
    expect(order).toEqual(['open', 'signOut', 'adopt'])
  })
})

describe('an existing signed-in user (W11: claim into my account)', () => {
  beforeEach(() => {
    auth.user = { id: 'real-user', email: 'me@example.com', is_anonymous: false }
  })

  const MEMBERSHIP = { orgId: 'org_claimed', claimPath: 'membership', hostnames: [], holds: [], exceedsFreeSiteAllowance: false }

  it('is offered to claim into their account, says what that means, and is not sent to /upgrade', async () => {
    arrive({ 'ClaimableService.GetClaim': { response: LIVE } })

    const block = await screen.findByTestId('claim-into-account')
    expect(screen.getByTestId('claim-signed-in-as')).toHaveTextContent('me@example.com')
    expect(block).toHaveTextContent(/you'll own it/i)
    expect(block).toHaveTextContent(/beside your other organizations/i)
    expect(block).toHaveTextContent(/one static site/i)
    expect(screen.getByTestId('claim-into-account-button')).toBeEnabled()
    expect(screen.queryByTestId('claim-submit')).toBeNull()
    expect(screen.getByTestId('claim-site')).toBeInTheDocument()
  })

  it('confirming calls CompleteClaim as themselves, joins the org, and lands on /apps with the app highlighted', async () => {
    const rpcs = vi.fn()
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.OpenClaimSession': () => (rpcs('OpenClaimSession'), {}),
      'ClaimableService.CompleteClaim': (input) => (rpcs('CompleteClaim', input), MEMBERSHIP),
    })
    fireEvent.click(await screen.findByTestId('claim-into-account-button'))

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
    // No session was released: this person is not the anonymous user.
    expect(rpcNames(rpcs)).toEqual(['CompleteClaim'])
    expect(auth.adoptSession).not.toHaveBeenCalled()
    expect(auth.signOut).not.toHaveBeenCalled()
    // The org they joined is the one the page acts in, before the list reloads.
    expect(switchToJoinedOrg).toHaveBeenCalledWith('org_claimed')
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['forge'] })
    expect(readPendingClaim()).toBeNull()
    expect(track).toHaveBeenCalledWith('claim_started', { situation: 'existing-account' })
    expect(track).toHaveBeenCalledWith('claim_completed', { claimPath: 'membership', holds: 0, exceedsFreeSiteAllowance: false })
  })

  it('a failure to switch org does not undo or hide the claim', async () => {
    switchToJoinedOrg.mockRejectedValueOnce(new Error('list failed'))
    arrive({ 'ClaimableService.GetClaim': { response: LIVE }, 'ClaimableService.CompleteClaim': { response: MEMBERSHIP } })
    fireEvent.click(await screen.findByTestId('claim-into-account-button'))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
    expect(readPendingClaim()).toBeNull()
  })

  it('an in-place upgrade does not switch org', async () => {
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.CompleteClaim': { response: { ...MEMBERSHIP, claimPath: 'upgrade' } },
    })
    fireEvent.click(await screen.findByTestId('claim-into-account-button'))
    await waitFor(() => expect(navigate).toHaveBeenCalled())
    expect(switchToJoinedOrg).not.toHaveBeenCalled()
  })

  it('reports it when the claimer is over the free site allowance, and still lands on /apps', async () => {
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.CompleteClaim': { response: { ...MEMBERSHIP, exceedsFreeSiteAllowance: true } },
    })
    fireEvent.click(await screen.findByTestId('claim-into-account-button'))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront', overAllowance: true }, replace: true }))
    expect(track).toHaveBeenCalledWith('claim_completed', { claimPath: 'membership', holds: 0, exceedsFreeSiteAllowance: true })
  })

  it('a lost race shows the claimed card and keeps nothing half done', async () => {
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.CompleteClaim': { error: refused('claimable_claimed') },
    })
    fireEvent.click(await screen.findByTestId('claim-into-account-button'))
    expect(await screen.findByTestId('claim-refused-claimable_claimed')).toBeInTheDocument()
    expect(switchToJoinedOrg).not.toHaveBeenCalled()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('a server error keeps the claim and offers a retry that does not need a new link', async () => {
    let attempts = 0
    arrive({
      'ClaimableService.GetClaim': { response: LIVE },
      'ClaimableService.CompleteClaim': () => {
        attempts += 1
        if (attempts === 1) throw new ConnectError('db down', Code.Unavailable)
        return MEMBERSHIP
      },
    })
    fireEvent.click(await screen.findByTestId('claim-into-account-button'))
    fireEvent.click(await screen.findByRole('button', { name: /try again/i }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
    expect(attempts).toBe(2)
  })

  it('"use a different account" signs out and returns here after signing in, keeping the claim', async () => {
    arrive({ 'ClaimableService.GetClaim': { response: LIVE } })
    fireEvent.click(await screen.findByTestId('claim-switch-account'))
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith('/auth?redirect=%2Fclaim'))
    expect(auth.signOut).toHaveBeenCalled()
    expect(readPendingClaim()?.token).toBe(TOKEN)
  })
})

describe('someone with an account who is not signed in to it', () => {
  it('is offered to sign in and come back, beside the new-account claim', async () => {
    arrive({ 'ClaimableService.GetClaim': { response: LIVE } })
    expect(await screen.findByTestId('claim-submit')).toBeInTheDocument()
    const block = screen.getByTestId('claim-existing-account')
    expect(block).toHaveTextContent('Already on Reliant?')
    expect(block).toHaveTextContent(/as an organization you own/i)

    fireEvent.click(screen.getByTestId('claim-existing-account-button'))
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith('/auth?redirect=%2Fclaim'))
    // Nothing to sign out of, no session was released, and the link is still good.
    expect(auth.signOut).not.toHaveBeenCalled()
    expect(readPendingClaim()?.token).toBe(TOKEN)
  })

  it('holding the temporary account says that signing in leaves it, and signs out first', async () => {
    auth.user = { id: 'anon-1', is_anonymous: true }
    updatePendingClaim({ anonUserId: 'anon-1' })
    arrive({ 'ClaimableService.GetClaim': { response: LIVE } })

    expect(await screen.findByTestId('claim-existing-account')).toHaveTextContent(/signs out of the site's temporary account/i)
    fireEvent.click(screen.getByTestId('claim-existing-account-button'))
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith('/auth?redirect=%2Fclaim'))
    expect(auth.signOut).toHaveBeenCalled()
  })
})

describe('coming back upgraded (same user id) → CompleteClaim → /apps', () => {
  beforeEach(() => {
    updatePendingClaim({ anonUserId: 'anon-1', project: 'storefront' })
    auth.user = { id: 'anon-1', is_anonymous: false }
  })

  it('completes the claim and lands on /apps with the app highlighted', async () => {
    const complete = vi.fn(() => ({ orgId: 'org_1', claimPath: 'upgrade', hostnames: ['a.sites.reliantlabs.dev'], holds: [] }))
    const preview = vi.fn(() => LIVE)
    arrive({ 'ClaimableService.CompleteClaim': complete, 'ClaimableService.GetClaim': preview })

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
    expect(complete).toHaveBeenCalledWith(expect.objectContaining({ claimToken: TOKEN }))
    // No preview on the way back: CompleteClaim answers for the claim itself.
    expect(preview).not.toHaveBeenCalled()
    expect(readPendingClaim()).toBeNull()
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['forge'] })
    expect(track).toHaveBeenCalledWith('claim_completed', { claimPath: 'upgrade', holds: 0, exceedsFreeSiteAllowance: false })
  })

  it('refreshes the session first, so a JWT that still says anonymous is re-issued', async () => {
    const order: string[] = []
    refreshSession.mockImplementationOnce(async () => (order.push('refresh'), { data: {}, error: null }))
    arrive({
      'ClaimableService.CompleteClaim': () => (order.push('complete'), { orgId: 'o', claimPath: 'upgrade', hostnames: [], holds: [] }),
    })
    await waitFor(() => expect(navigate).toHaveBeenCalled())
    expect(order).toEqual(['refresh', 'complete'])
  })

  it('does not call CompleteClaim until the refresh has settled (the server reads the verified email off the refreshed token)', async () => {
    let release: () => void = () => undefined
    const gate = new Promise<void>((resolve) => { release = resolve })
    const complete = vi.fn(() => ({ orgId: 'o', claimPath: 'upgrade', hostnames: [], holds: [] }))
    refreshSession.mockImplementationOnce(async () => (await gate, { data: {}, error: null }))
    arrive({ 'ClaimableService.CompleteClaim': complete })

    await screen.findByText(/making this site yours/i)
    await new Promise((r) => setTimeout(r, 20))
    expect(refreshSession).toHaveBeenCalledTimes(1)
    expect(complete).not.toHaveBeenCalled()

    release()
    await waitFor(() => expect(navigate).toHaveBeenCalled())
    expect(complete).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['returns an error', async () => ({ data: {}, error: { message: 'refresh denied' } }) as never],
    ['throws', async () => { throw new Error('network') }],
  ])('still asks CompleteClaim when the refresh %s, and lets its answer decide', async (_name, failing) => {
    refreshSession.mockImplementationOnce(failing)
    const complete = vi.fn(() => ({ orgId: 'o', claimPath: 'upgrade', hostnames: [], holds: [] }))
    arrive({ 'ClaimableService.CompleteClaim': complete })

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
    expect(complete).toHaveBeenCalledTimes(1)
  })

  it('a failed refresh followed by a refusal shows the refusal, not a refresh error', async () => {
    refreshSession.mockImplementationOnce(async () => ({ data: {}, error: { message: 'refresh denied' } }) as never)
    arrive({ 'ClaimableService.CompleteClaim': { error: refused('claimable_claimed') } })
    expect(await screen.findByTestId('claim-refused-claimable_claimed')).toBeInTheDocument()
  })

  it('claimable_upgrade_required: back to /upgrade?returnTo=/claim, keeping the claim', async () => {
    arrive({ 'ClaimableService.CompleteClaim': { error: refused('claimable_upgrade_required', Code.PermissionDenied) } })

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/upgrade', search: { returnTo: '/claim' } }))
    expect(navigate).not.toHaveBeenCalledWith(expect.objectContaining({ to: '/apps' }))
    expect(readPendingClaim()?.token).toBe(TOKEN)
    expect(track).toHaveBeenCalledWith('claim_failed', { stage: 'complete', reason: 'claimable_upgrade_required' })
  })

  it('claimable_upgrade_required twice does not loop: it asks the person to verify, with a retry', async () => {
    updatePendingClaim({ upgradeSent: true })
    arrive({ 'ClaimableService.CompleteClaim': { error: refused('claimable_upgrade_required', Code.PermissionDenied) } })

    expect(await screen.findByTestId('claim-finish-failed')).toHaveTextContent(/verified/i)
    expect(navigate).not.toHaveBeenCalled()
  })

  it('a retired reason (claimable_existing_account) is just an unknown refusal now', async () => {
    arrive({
      'ClaimableService.CompleteClaim': { error: refused('claimable_existing_account', Code.PermissionDenied) },
      'ClaimableService.GetClaim': { response: LIVE },
    })
    expect(await screen.findByTestId('claim-finish-declined')).toBeInTheDocument()
    expect(screen.queryByTestId('claim-existing-account')).toBeNull()
  })

  it('an unknown reason shows a generic screen, keeps the claim, and offers a retry', async () => {
    let attempts = 0
    arrive({
      'ClaimableService.CompleteClaim': () => {
        attempts += 1
        if (attempts === 1) throw refused('claimable_something_new', Code.PermissionDenied)
        return { orgId: 'o', claimPath: 'upgrade', hostnames: [], holds: [] }
      },
    })

    const card = await screen.findByTestId('claim-finish-declined')
    expect(card).toHaveTextContent(/couldn't complete this claim/i)
    expect(card).toHaveTextContent('claimable_something_new')
    expect(navigate).not.toHaveBeenCalled()
    expect(readPendingClaim()?.token).toBe(TOKEN)

    fireEvent.click(screen.getByRole('button', { name: /try again/i }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
  })

  it('a PermissionDenied with no reason at all gets the same generic screen', async () => {
    arrive({ 'ClaimableService.CompleteClaim': { error: new ConnectError('no', Code.PermissionDenied) } })
    expect(await screen.findByTestId('claim-finish-declined')).toBeInTheDocument()
  })

  it('reports a lost race as claimed (someone else completed it first)', async () => {
    arrive({ 'ClaimableService.CompleteClaim': { error: refused('claimable_claimed') } })
    expect(await screen.findByTestId('claim-refused-claimable_claimed')).toBeInTheDocument()
    expect(track).toHaveBeenCalledWith('claim_failed', { stage: 'complete', reason: 'claimable_claimed' })
    expect(navigate).not.toHaveBeenCalled()
  })

  it('keeps the claim and offers a retry when completion fails for another reason', async () => {
    let attempts = 0
    arrive({
      'ClaimableService.CompleteClaim': () => {
        attempts += 1
        if (attempts === 1) throw new ConnectError('overloaded', Code.Unavailable)
        return { orgId: 'o', claimPath: 'upgrade', hostnames: [], holds: [] }
      },
    })

    const failed = await screen.findByTestId('claim-finish-failed')
    expect(failed).toHaveTextContent(/still waiting for you/i)
    expect(readPendingClaim()?.token).toBe(TOKEN)

    fireEvent.click(screen.getByRole('button', { name: /try again/i }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: { claimed: 'storefront' }, replace: true }))
  })
})

describe('the control plane cannot be reached', () => {
  it('shows an outage with a retry, not a verdict on the claim', async () => {
    let up = false
    arrive({
      'ClaimableService.GetClaim': () => {
        if (!up) throw new ConnectError('down', Code.Unavailable)
        return LIVE
      },
    })

    const card = await screen.findByTestId('claim-unavailable')
    expect(card).toHaveTextContent(/could not be reached/i)
    expect(readPendingClaim()?.token).toBe(TOKEN)

    up = true
    fireEvent.click(screen.getByRole('button', { name: /try again/i }))
    expect(await screen.findByTestId('claim-site')).toBeInTheDocument()
  })
})
