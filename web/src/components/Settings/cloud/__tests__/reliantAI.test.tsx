import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Resilient smoke tests for ReliantAISection.
 *
 * The `useReliantAIQueries` hooks are fully mocked so the panel renders with
 * empty data (no network). The `reliantAIAvailable` capability flag is served
 * via a getter over a hoisted mutable so both the available and
 * "cloud not configured" branches can be exercised.
 *
 * Assertions target STABLE structure: the PageHeader h1 and the empty-state
 * headings — never marketing copy, which other agents are editing.
 */

const mocks = vi.hoisted(() => ({ available: true }))

vi.mock('@/services/controlPlane/reliantAI', () => ({
  get reliantAIAvailable() {
    return mocks.available
  },
  // Async wrappers — never invoked because the hooks are mocked, but present
  // so the import surface matches the real module.
  getReliantOverview: vi.fn(),
  getWalletOverview: vi.fn(),
  listLLMKeys: vi.fn(),
  listAvailableModels: vi.fn(),
  getLLMSpend: vi.fn(),
  createManagedLLMKey: vi.fn(),
  revokeLLMKey: vi.fn(),
  rotateLLMKey: vi.fn(),
}))

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
  variables: undefined,
})

vi.mock('@/hooks/useReliantAIQueries', () => ({
  useReliantOverview: () => query(undefined),
  useWalletOverview: () => query(undefined),
  useLLMKeys: () => query([]),
  useAvailableModels: () => query([]),
  useLLMSpend: () => query({ entries: [], totalSpend: 0 }),
  useCreateLLMKey: () => mutation(),
  useRevokeLLMKey: () => mutation(),
  useRotateLLMKey: () => mutation(),
}))

import { ReliantAISection } from '@/components/Settings/cloud/reliantAI'

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ReliantAISection />
    </QueryClientProvider>,
  )
}

describe('ReliantAISection', () => {
  beforeEach(() => {
    mocks.available = true
  })

  it('renders the header and the no-data empty state when overview/wallet are empty', () => {
    renderSection()

    expect(
      screen.getByRole('heading', { level: 1, name: /reliant ai/i }),
    ).toBeInTheDocument()

    // overview + wallet both undefined, not loading → "No access data".
    expect(
      screen.getByRole('heading', { level: 3, name: /no access data/i }),
    ).toBeInTheDocument()
  })

  it('renders the cloud-not-configured fallback when the surface is unavailable', () => {
    mocks.available = false
    renderSection()

    expect(
      screen.getByRole('heading', { level: 1, name: /reliant ai/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { level: 3, name: /cloud not configured/i }),
    ).toBeInTheDocument()
  })
})
