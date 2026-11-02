/**
 * The /claim page's three rpcs, through the real generated client and the real
 * Connect request/response types over a mock transport (W5a, the server half,
 * is not built yet: these pin the contract as the proto states it).
 *
 * What is asserted is the page's vocabulary, not the transport: every refusal
 * the proto names arrives as a ClaimRefusal, and a failure that is NOT one of
 * them stays an error (the page shows "try again", not "expired").
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Code, ConnectError } from '@connectrpc/connect'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'

vi.mock('@/services/controlPlane/config', () => ({
  CONTROL_PLANE_API_URL: 'https://admin.example.test',
  hasControlPlane: true,
}))

import {
  ClaimableState,
  FreeSiteChoice,
  FreeSiteChoiceRequiredSchema,
  FreeSiteOutcome,
} from '@/gen/controlplane/services/claimable/v1/claimable_pb'
import { create } from '@bufbuild/protobuf'
import { setTransport } from '@/lib/connect'
import { mockTransport } from '@/lib/test-utils'
import {
  claimRefusalOf,
  completeActionOf,
  isDeclined,
  completeClaim,
  failureReasonOf,
  freeSiteConflictOf,
  getClaim,
  getClaimFreeSite,
  openClaimSession,
  type ClaimRefusal,
} from '../claim'

const TOKEN = 'rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_'
const EXPIRES = new Date('2026-10-29T14:02:00Z')
const PURGE = new Date('2026-11-05T14:02:00Z')

function refused(reason: string, code: Code = Code.FailedPrecondition): ConnectError {
  return new ConnectError('refused', code, new Headers({ 'x-forge-error-reason': reason }))
}

afterEach(() => setTransport(null))

describe('getClaim', () => {
  const live = {
    state: ClaimableState.LIVE,
    claimable: true,
    project: 'storefront',
    env: 'prod',
    hostnames: ['amber-quokka.reliantpreview.dev'],
    expiresAt: timestampFromDate(EXPIRES),
    purgeAfter: timestampFromDate(PURGE),
  }

  it('sends the token in the request BODY (the URL and headers never carry it)', async () => {
    const seen = vi.fn(() => live)
    setTransport(mockTransport({ 'ClaimableService.GetClaim': seen }))

    await getClaim(TOKEN)

    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ claimToken: TOKEN }))
  })

  it('answers a live deploy as a claimable site', async () => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { response: live } }))

    expect(await getClaim(TOKEN)).toEqual({
      claimable: true,
      site: {
        state: 'live',
        project: 'storefront',
        env: 'prod',
        hostnames: ['amber-quokka.reliantpreview.dev'],
        expiresAt: EXPIRES,
        purgeAfter: PURGE,
      },
    })
  })

  it('a suspended deploy is still claimable, and says it is offline', async () => {
    setTransport(
      mockTransport({ 'ClaimableService.GetClaim': { response: { ...live, state: ClaimableState.SUSPENDED } } }),
    )
    const preview = await getClaim(TOKEN)
    expect(preview).toMatchObject({ claimable: true, site: { state: 'suspended' } })
  })

  it.each([
    [ClaimableState.CLAIMED, 'claimable_claimed'],
    [ClaimableState.TAKEN_DOWN, 'claimable_taken_down'],
    [ClaimableState.EXPIRED, 'claimable_expired'],
  ] as const)('state %s is refused as %s (the server answered OK with a state)', async (state, refusal) => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { response: { ...live, state, claimable: false } } }))
    expect(await getClaim(TOKEN)).toEqual({ claimable: false, refusal })
  })

  it('a live deploy the server says is not claimable is not offered', async () => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { response: { ...live, claimable: false } } }))
    expect(await getClaim(TOKEN)).toEqual({ claimable: false, refusal: 'claimable_expired' })
  })

  it('an unspecified state is a failure, not a claim', async () => {
    setTransport(
      mockTransport({ 'ClaimableService.GetClaim': { response: { ...live, state: ClaimableState.UNSPECIFIED } } }),
    )
    await expect(getClaim(TOKEN)).rejects.toThrow(/unknown state/)
  })

  it.each([
    ['claimable_claimed', Code.FailedPrecondition],
    ['claimable_expired', Code.FailedPrecondition],
    ['claimable_taken_down', Code.FailedPrecondition],
    ['claimable_not_found', Code.NotFound],
  ] as const)('the %s error becomes a refusal', async (reason, code) => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { error: refused(reason, code) } }))
    expect(await getClaim(TOKEN)).toEqual({ claimable: false, refusal: reason })
  })

  it('a NotFound with no reason is still "no such claim"', async () => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { error: new ConnectError('nope', Code.NotFound) } }))
    expect(await getClaim(TOKEN)).toEqual({ claimable: false, refusal: 'claimable_not_found' })
  })

  it('an outage is rethrown, never shown as a refusal', async () => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { error: new ConnectError('down', Code.Unavailable) } }))
    await expect(getClaim(TOKEN)).rejects.toBeInstanceOf(ConnectError)
  })

  it('the stub the server answers until W5a/W2 land is an outage, not a refusal', async () => {
    setTransport(mockTransport({ 'ClaimableService.GetClaim': { error: new ConnectError('stub', Code.Unimplemented) } }))
    await expect(getClaim(TOKEN)).rejects.toBeInstanceOf(ConnectError)
  })
})

describe('openClaimSession', () => {
  it('returns the released session, with expires_in as a number', async () => {
    const seen = vi.fn(() => ({ accessToken: 'a.b.c', refreshToken: 'r-1', expiresIn: 3600n }))
    setTransport(mockTransport({ 'ClaimableService.OpenClaimSession': seen }))

    expect(await openClaimSession(TOKEN)).toEqual({ accessToken: 'a.b.c', refreshToken: 'r-1', expiresIn: 3600 })
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ claimToken: TOKEN }))
  })

  it('a second open is refused with claimable_session_released', async () => {
    setTransport(
      mockTransport({ 'ClaimableService.OpenClaimSession': { error: refused('claimable_session_released') } }),
    )
    const err = await openClaimSession(TOKEN).catch((e) => e)
    expect(claimRefusalOf(err)).toBe('claimable_session_released')
  })
})

describe('completeClaim', () => {
  it('returns the org, the path taken and the held deploys', async () => {
    setTransport(
      mockTransport({
        'ClaimableService.CompleteClaim': {
          response: { orgId: 'org_1', claimPath: 'upgrade', hostnames: ['a.sites.reliantlabs.dev'], holds: [{ id: 'h1' }] },
        },
      }),
    )
    expect(await completeClaim(TOKEN)).toEqual({
      orgId: 'org_1',
      claimPath: 'upgrade',
      hostnames: ['a.sites.reliantlabs.dev'],
      holds: 1,
      freeSite: 'none',
      pausedSite: null,
    })
  })

  it('reports what the claim did about the free site, and the site it paused', async () => {
    setTransport(
      mockTransport({
        'ClaimableService.CompleteClaim': {
          response: {
            orgId: 'org_1',
            claimPath: 'membership',
            hostnames: [],
            holds: [],
            freeSiteOutcome: FreeSiteOutcome.CLAIMED_SITE_LIVE,
            pausedSite: { deploymentId: 'dep_a', project: 'blog', env: 'prod', name: 'web', url: 'https://old.sites.test' },
          },
        },
      }),
    )
    expect(await completeClaim(TOKEN, 'claimed')).toMatchObject({
      claimPath: 'membership',
      freeSite: 'claimed-live',
      pausedSite: { deploymentId: 'dep_a', url: 'https://old.sites.test' },
    })
  })

  it.each([
    ['claimed', FreeSiteChoice.CLAIMED],
    ['existing', FreeSiteChoice.EXISTING],
    [undefined, FreeSiteChoice.UNSPECIFIED],
  ] as const)('sends the choice %s as %s, and never invents one', async (keep, wire) => {
    const seen = vi.fn()
    setTransport(
      mockTransport({
        'ClaimableService.CompleteClaim': (input: unknown) => (seen((input as { freeSite: number }).freeSite), { orgId: 'o' }),
      }),
    )
    await completeClaim(TOKEN, keep)
    expect(seen).toHaveBeenCalledWith(wire)
  })

  it('a lost race is refused as claimed', async () => {
    setTransport(mockTransport({ 'ClaimableService.CompleteClaim': { error: refused('claimable_claimed') } }))
    expect(claimRefusalOf(await completeClaim(TOKEN).catch((e) => e))).toBe('claimable_claimed')
  })
})

describe('claimRefusalOf / failureReasonOf', () => {
  it.each<ClaimRefusal>([
    'claimable_claimed',
    'claimable_expired',
    'claimable_taken_down',
    'claimable_not_found',
    'claimable_session_released',
  ])('reads %s', (reason) => {
    expect(claimRefusalOf(refused(reason))).toBe(reason)
  })

  it('does not claim an error with another reason, or none', () => {
    expect(claimRefusalOf(refused('claimable_throttled', Code.ResourceExhausted))).toBeNull()
    expect(claimRefusalOf(new ConnectError('x', Code.Internal))).toBeNull()
    expect(claimRefusalOf(new Error('plain'))).toBeNull()
  })

  it('names a failure for the funnel: the reason, else the code, else unknown', () => {
    expect(failureReasonOf(refused('claimable_claimed'))).toBe('claimable_claimed')
    expect(failureReasonOf(new ConnectError('x', Code.Unavailable))).toBe('unavailable')
    expect(failureReasonOf(new Error('plain'))).toBe('unknown')
  })
})

describe('completeActionOf / isDeclined', () => {
  it('reads claimable_upgrade_required as an action, never a refusal', () => {
    const err = refused('claimable_upgrade_required', Code.PermissionDenied)
    expect(completeActionOf(err)).toBe('claimable_upgrade_required')
    expect(claimRefusalOf(err)).toBeNull()
  })

  it('reads claimable_free_site_choice_required as an action: the page asks, the claim is not over', () => {
    const err = refused('claimable_free_site_choice_required', Code.FailedPrecondition)
    expect(completeActionOf(err)).toBe('claimable_free_site_choice_required')
    expect(claimRefusalOf(err)).toBeNull()
  })

  it('no longer knows claimable_existing_account: the server joins the org instead of asking', () => {
    const err = refused('claimable_existing_account', Code.PermissionDenied)
    expect(completeActionOf(err)).toBeNull()
    expect(claimRefusalOf(err)).toBeNull()
  })

  it('names no action for another reason, or none', () => {
    expect(completeActionOf(refused('claimable_something_new', Code.PermissionDenied))).toBeNull()
    expect(completeActionOf(new ConnectError('x', Code.PermissionDenied))).toBeNull()
    expect(completeActionOf(new Error('plain'))).toBeNull()
  })

  it('a decline is the server answering no; an outage is not one', () => {
    expect(isDeclined(new ConnectError('x', Code.PermissionDenied))).toBe(true)
    expect(isDeclined(new ConnectError('x', Code.FailedPrecondition))).toBe(true)
    expect(isDeclined(new ConnectError('x', Code.Unavailable))).toBe(false)
    expect(isDeclined(new Error('plain'))).toBe(false)
  })
})

beforeEach(() => vi.clearAllMocks())

describe('the free static site choice', () => {
  const existing = { deploymentId: 'dep_mine', project: 'blog', env: 'prod', name: 'web', url: 'https://mine.sites.test' }
  const claimed = { deploymentId: 'dep_new', project: 'shop', env: 'prod', name: 'web', url: 'https://new.sites.test' }

  it('reads the two sites out of a claimable_free_site_choice_required refusal', () => {
    const err = new ConnectError(
      'choose',
      Code.FailedPrecondition,
      new Headers({ 'x-forge-error-reason': 'claimable_free_site_choice_required' }),
      [{ desc: FreeSiteChoiceRequiredSchema, value: create(FreeSiteChoiceRequiredSchema, { existingSite: existing, claimedSite: claimed }) }],
    )
    expect(freeSiteConflictOf(err)).toEqual({
      existing: { ...existing },
      claimed: { ...claimed },
    })
  })

  it('is null for any other error, and for the refusal without its detail', () => {
    expect(freeSiteConflictOf(refused('claimable_claimed'))).toBeNull()
    expect(freeSiteConflictOf(refused('claimable_free_site_choice_required'))).toBeNull()
    expect(freeSiteConflictOf(new Error('plain'))).toBeNull()
  })

  it('GetClaimFreeSite returns the pair only when a choice is needed', async () => {
    setTransport(
      mockTransport({
        'ClaimableService.GetClaimFreeSite': { response: { choiceRequired: true, existingSite: existing, claimedSite: claimed } },
      }),
    )
    expect(await getClaimFreeSite(TOKEN)).toMatchObject({ existing: { url: 'https://mine.sites.test' }, claimed: { name: 'web' } })

    setTransport(mockTransport({ 'ClaimableService.GetClaimFreeSite': { response: { choiceRequired: false } } }))
    expect(await getClaimFreeSite(TOKEN)).toBeNull()
  })
})
