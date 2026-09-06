import { render, screen, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Resilient smoke tests for the end-user BillingSection.
 *
 * The whole `useCloudBillingQueries` hook module is mocked so the section
 * renders deterministically with empty data — no network, no QueryClient
 * fetching. Assertions target STABLE structure (the PageHeader h1, the tab
 * buttons, and per-tab empty states) rather than marketing copy, which other
 * agents are actively editing.
 */

// Minimal react-query-shaped stand-ins. `data: undefined` exercises every
// `?? null` / `?? []` default path in the component.
const query = (data?: unknown) => ({
  data,
  isLoading: false,
  error: null,
  refetch: vi.fn(),
})
const mutation = () => ({
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
  isPending: false,
})

// Mutable so individual tests can vary the compute-usage payload. `vi.hoisted`
// is required because `vi.mock`'s factory is hoisted above normal `const`s.
const usageState = vi.hoisted(() => ({ current: undefined as unknown }))

/**
 * Stand-in router. The billing sub-tab moved out of `useState` and into the URL
 * (`?tab=`) so a link can target it, which means these tests need somewhere for
 * that param to live — and it has to be STATEFUL, because the tab-switch tests
 * click a tab and then assert on what rendered. So `navigate` writes to a tiny
 * store and every `useSearch` consumer subscribes to it.
 */
const routerState = vi.hoisted(() => {
  const listeners = new Set<() => void>()
  const state = {
    search: {} as Record<string, unknown>,
    subscribe(fn: () => void) {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    get() {
      return state.search
    },
    navigate({ search }: { search?: unknown }) {
      const next =
        typeof search === 'function'
          ? (search as (p: Record<string, unknown>) => Record<string, unknown>)(
              state.search,
            )
          : (search as Record<string, unknown>) ?? {}
      state.search = next
      listeners.forEach((fn) => fn())
    },
    reset() {
      state.search = {}
      listeners.clear()
    },
  }
  return state
})

vi.mock('@tanstack/react-router', async () => {
  const { useSyncExternalStore } = await import('react')
  return {
    useNavigate: () => routerState.navigate,
    useSearch: () =>
      useSyncExternalStore(routerState.subscribe, routerState.get, routerState.get),
  }
})

// Only `redeemCoupon` is replaced; the rest of the service module (notably the
// `RedeemedCouponKind` re-export and the `reliantAIAvailable` flag) stays real,
// so the mock cannot drift from the module's actual shape.
const couponState = vi.hoisted(() => ({ redeem: vi.fn() }))

vi.mock('@/services/controlPlane/reliantAI', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  return { ...actual, redeemCoupon: (code: string) => couponState.redeem(code) }
})

vi.mock('@/hooks/useCloudBillingQueries', () => ({
  // Real object, not a stub: `useRedeemCoupon` (from the OTHER hook module,
  // which is not mocked) dereferences these keys in its own onSuccess to
  // invalidate the billing reads. Omitting them makes redemption throw before
  // the form's success handler ever runs.
  cloudBillingKeys: {
    all: ['cloud-billing'],
    computeSubscription: ['cloud-billing', 'compute-subscription'],
    walletOverview: ['cloud-billing', 'wallet-overview'],
    computeUsage: (period: string) => ['cloud-billing', 'compute-usage', period],
    plans: ['cloud-billing', 'plans'],
    invoices: ['cloud-billing', 'invoices'],
    billingEmail: ['cloud-billing', 'billing-email'],
  },
  useComputeSubscription: () => query(undefined),
  useWalletOverview: () => query(undefined),
  useComputeUsage: () => query(usageState.current),
  usePlans: () => query(undefined),
  useCurrentUserInvoices: () => query(undefined),
  useBillingEmail: () => query(undefined),
  useSetComputeOverage: () => mutation(),
  useCreateCheckoutSession: () => mutation(),
  useCreateWalletTopupSession: () => mutation(),
  useCreateBillingPortalSession: () => mutation(),
  useUpdateBillingEmail: () => mutation(),
}))

import { RedeemedCouponKind } from '@/gen/controlplane/v1/public/billing_service_pb'
import { BillingSection } from '@/components/Settings/cloud/billing'

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <BillingSection />
    </QueryClientProvider>,
  )
}

describe('BillingSection', () => {
  beforeEach(() => {
    usageState.current = undefined
    routerState.reset()
  })

  it('renders the section header and the three sub-tabs', () => {
    renderSection()

    expect(
      screen.getByRole('heading', { level: 1, name: /billing/i }),
    ).toBeInTheDocument()

    // Exactly three, in order. An inclusion-only check would still pass if a
    // fourth tab came back, and the whole point of the merge was that the
    // strip stops being a filing cabinet.
    expect(screen.getAllByRole('tab').map((t) => t.textContent?.trim())).toEqual([
      'Overview',
      'Change plan',
      'Usage & invoices',
    ])
  })

  it('shows the plans empty state when no plans are configured', () => {
    renderSection()

    fireEvent.click(screen.getByRole('tab', { name: /change plan/i }))

    expect(
      screen.getByRole('heading', { level: 3, name: /no plans available/i }),
    ).toBeInTheDocument()
  })

  it('shows the invoices empty state on the merged tab', () => {
    renderSection()

    fireEvent.click(screen.getByRole('tab', { name: /usage & invoices/i }))

    expect(
      screen.getByRole('heading', { level: 3, name: /no invoices yet/i }),
    ).toBeInTheDocument()
  })

  it('shows the usage empty state on the merged tab', () => {
    renderSection()

    fireEvent.click(screen.getByRole('tab', { name: /usage & invoices/i }))

    expect(
      screen.getByRole('heading', { level: 3, name: /no usage data/i }),
    ).toBeInTheDocument()
  })

  // Redeemed compute-coupon minutes. These are reported ALONGSIDE the plan's
  // included minutes, never summed into them: included minutes refill every
  // billing period, a grant depletes once and does not renew.
  describe('coupon (granted) minutes', () => {
    const usage = (grantedMinutesRemaining: number) => ({
      includedMinutes: 600,
      usedMinutes: 60,
      overageMinutes: 0,
      estimatedOverageCostCents: 0,
      byWorkspace: [],
      byDay: [],
      grantedMinutesRemaining,
    })

    it('shows remaining coupon minutes, with hours, on the overview tab', () => {
      usageState.current = usage(120)
      renderSection()

      expect(screen.getByText(/coupon minutes/i)).toBeInTheDocument()
      expect(screen.getByText('120 min (2 h)')).toBeInTheDocument()
      // The distinction from included minutes has to be legible, not implied.
      expect(screen.getByText(/does not renew/i)).toBeInTheDocument()
    })

    it('shows remaining coupon minutes as its own stat on the usage tab', () => {
      usageState.current = usage(90)
      renderSection()

      fireEvent.click(screen.getByRole('tab', { name: /usage & invoices/i }))

      expect(screen.getByText(/coupon minutes remaining/i)).toBeInTheDocument()
      expect(screen.getByText('90 min (1.5 h)')).toBeInTheDocument()
      // Included stays the plan's own 600 min / 10 h — the grant is not folded in.
      expect(screen.getByText('10 h')).toBeInTheDocument()
    })

    it('omits the row entirely when no coupon has been redeemed', () => {
      usageState.current = usage(0)
      renderSection()

      expect(screen.queryByText(/coupon minutes/i)).not.toBeInTheDocument()

      fireEvent.click(screen.getByRole('tab', { name: /usage & invoices/i }))

      expect(screen.queryByText(/coupon minutes/i)).not.toBeInTheDocument()
    })
  })

  // The user's report was "not sure if there's a place to add coupon codes?"
  // — the field existed but only behind a "Have a coupon code?" disclosure
  // inside a card about usage. These tests pin the fix: the input is on screen
  // with nothing to click first, and one box handles BOTH grant kinds.
  describe('coupon redemption', () => {
    beforeEach(() => {
      couponState.redeem.mockReset()
    })

    it('shows the coupon input on the overview tab without clicking a disclosure', () => {
      renderSection()

      expect(screen.getByLabelText(/coupon code/i)).toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: /^redeem$/i }),
      ).toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: /have a coupon code/i }),
      ).not.toBeInTheDocument()
    })

    it('explains that one code covers either credit or machine minutes', () => {
      renderSection()

      expect(
        screen.getByText(/add account credit or machine minutes/i),
      ).toBeInTheDocument()
    })

    it('reports a wallet-credit redemption in dollars', async () => {
      couponState.redeem.mockResolvedValue({
        amountCents: 2500,
        code: 'WELCOME25',
        newBalanceCents: 2500,
        kind: RedeemedCouponKind.WALLET_CREDIT,
        computeMinutes: 0,
        newComputeMinutesRemaining: 0,
      })
      renderSection()

      fireEvent.change(screen.getByLabelText(/coupon code/i), {
        target: { value: 'welcome25' },
      })
      fireEvent.click(screen.getByRole('button', { name: /^redeem$/i }))

      expect(
        await screen.findByText(/added \$25\.00 to your balance/i),
      ).toBeInTheDocument()
      expect(couponState.redeem).toHaveBeenCalledWith('welcome25')
    })

    it('reports a compute-minutes redemption in machine minutes, from the same box', async () => {
      couponState.redeem.mockResolvedValue({
        amountCents: 0,
        code: 'MACHINE120',
        newBalanceCents: 0,
        kind: RedeemedCouponKind.COMPUTE_MINUTES,
        computeMinutes: 120,
        newComputeMinutesRemaining: 180,
      })
      renderSection()

      fireEvent.change(screen.getByLabelText(/coupon code/i), {
        target: { value: 'MACHINE120' },
      })
      fireEvent.click(screen.getByRole('button', { name: /^redeem$/i }))

      expect(
        await screen.findByText(/added 120 machine minutes \(2 hours\)/i),
      ).toBeInTheDocument()
      expect(
        screen.getByText(/180 machine minutes \(3 hours\) available/i),
      ).toBeInTheDocument()
    })

    it("surfaces the server's own message when a code is rejected", async () => {
      couponState.redeem.mockRejectedValue(
        new Error('[not_found] That coupon code does not exist.'),
      )
      renderSection()

      fireEvent.change(screen.getByLabelText(/coupon code/i), {
        target: { value: 'nope' },
      })
      fireEvent.click(screen.getByRole('button', { name: /^redeem$/i }))

      expect(
        await screen.findByText('That coupon code does not exist.'),
      ).toBeInTheDocument()
    })
  })
})
