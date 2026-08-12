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

vi.mock('@/hooks/useCloudBillingQueries', () => ({
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
  })

  it('renders the section header and the four sub-tabs', () => {
    renderSection()

    expect(
      screen.getByRole('heading', { level: 1, name: /billing/i }),
    ).toBeInTheDocument()

    for (const label of ['Overview', 'Plans', 'Invoices', 'Usage']) {
      expect(screen.getByRole('button', { name: new RegExp(label, 'i') })).toBeInTheDocument()
    }
  })

  it('shows the plans empty state when no plans are configured', () => {
    renderSection()

    fireEvent.click(screen.getByRole('button', { name: /plans/i }))

    expect(
      screen.getByRole('heading', { level: 3, name: /no plans available/i }),
    ).toBeInTheDocument()
  })

  it('shows the invoices empty state when there are no invoices', () => {
    renderSection()

    fireEvent.click(screen.getByRole('button', { name: /invoices/i }))

    expect(
      screen.getByRole('heading', { level: 3, name: /no invoices yet/i }),
    ).toBeInTheDocument()
  })

  it('shows the usage empty state when there is no usage data', () => {
    renderSection()

    fireEvent.click(screen.getByRole('button', { name: /^usage$/i }))

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

      fireEvent.click(screen.getByRole('button', { name: /^usage$/i }))

      expect(screen.getByText(/coupon minutes remaining/i)).toBeInTheDocument()
      expect(screen.getByText('90 min (1.5 h)')).toBeInTheDocument()
      // Included stays the plan's own 600 min / 10 h — the grant is not folded in.
      expect(screen.getByText('10 h')).toBeInTheDocument()
    })

    it('omits the row entirely when no coupon has been redeemed', () => {
      usageState.current = usage(0)
      renderSection()

      expect(screen.queryByText(/coupon minutes/i)).not.toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: /^usage$/i }))

      expect(screen.queryByText(/coupon minutes/i)).not.toBeInTheDocument()
    })
  })
})
