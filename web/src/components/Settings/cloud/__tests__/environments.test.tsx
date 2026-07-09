import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Resilient smoke tests for EnvironmentsSection.
 *
 * The section uses real @tanstack/react-query hooks over the
 * `@/services/controlPlane/environments` data layer, so that whole service
 * module is mocked (enums + async fns). Data functions resolve to empty
 * collections, driving the deterministic empty states. `useSearch` (router)
 * and the cloud-capability gate are stubbed too.
 *
 * Assertions target STABLE structure: the PageHeader h1, the two tab buttons,
 * the empty state, and the capability-gated fallback — never marketing copy.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  getComputeSubscription: vi.fn(async () => ({})),
  listDaemonTokens: vi.fn(async () => []),
}))

vi.mock('@/services/controlPlane/capabilities', () => ({
  capabilities: mocks.caps,
}))

vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({}),
}))

// The real module exports proto enums at module scope that environments.tsx
// dereferences while building lookup tables — they MUST exist on the mock or
// the module fails to evaluate. Values are arbitrary-but-distinct.
vi.mock('@/services/controlPlane/environments', () => ({
  DaemonStatus: {
    UNSPECIFIED: 0,
    PENDING: 1,
    ACTIVE: 2,
    SUSPENDED: 3,
    FAILED: 4,
    DISCONNECTED: 5,
  },
  DaemonSize: { UNSPECIFIED: 0, SMALL: 1, MEDIUM: 2, LARGE: 3, XL: 4 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  listDaemons: mocks.listDaemons,
  getComputeSubscription: mocks.getComputeSubscription,
  listDaemonTokens: mocks.listDaemonTokens,
  getDaemon: vi.fn(),
  createEnvironment: vi.fn(),
  deleteDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  resumeEnvironment: vi.fn(),
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
  createDaemonToken: vi.fn(),
  revokeDaemonToken: vi.fn(),
}))

import { EnvironmentsSection } from '@/components/Settings/cloud/environments'

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <EnvironmentsSection />
    </QueryClientProvider>,
  )
}

describe('EnvironmentsSection', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.listDaemons.mockResolvedValue({ daemons: [] })
    mocks.getComputeSubscription.mockResolvedValue({})
    mocks.listDaemonTokens.mockResolvedValue([])
  })

  it('renders the header, then the empty machines state', async () => {
    renderSection()

    expect(
      screen.getByRole('heading', { level: 1, name: /machines/i }),
    ).toBeInTheDocument()

    // The daemons query resolves to [] → the "No machines" empty state.
    expect(
      await screen.findByRole('heading', { level: 3, name: /no machines/i }),
    ).toBeInTheDocument()
  })

  it('renders the capability-gated fallback when cloud daemons are unavailable', () => {
    mocks.caps.cloudDaemons = false
    renderSection()

    expect(
      screen.getByRole('heading', { level: 1, name: /machines/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { level: 3, name: /machines unavailable/i }),
    ).toBeInTheDocument()
  })
})