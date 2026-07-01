import { render, screen, fireEvent } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
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

vi.mock('@/hooks/useCloudBillingQueries', () => ({
  useComputeSubscription: () => query(undefined),
  useWalletOverview: () => query(undefined),
  useComputeUsage: () => query(undefined),
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
})
