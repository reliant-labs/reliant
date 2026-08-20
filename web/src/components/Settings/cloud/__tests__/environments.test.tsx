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
 *
 * The section also renders the shared SelfHostedDaemonConnect panel (the same
 * download/setup instructions onboarding shows). That panel reaches for the
 * event bus and the daemon-registry hook, both stubbed below; the assertion
 * on it is deliberately structural (its heading), since the point of the
 * shared component is that this page never owns the copy.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  getComputeSubscription: vi.fn(async () => ({})),
  listDaemonTokens: vi.fn(async () => []),
}))

vi.mock('@/lib/event-context', () => ({
  useEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}))

// The self-hosted panel polls the daemon registry; an empty, settled list
// keeps it on its instructions branch without any network.
vi.mock('@/hooks/useDaemonStatus', () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  }),
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
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  // environments.tsx calls this at module scope to build its query-key table,
  // so it must exist on the mock or the module fails to evaluate.
  portAccessRulesQueryKey: (daemonId: string) => ['cp', 'ports', daemonId],
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

import { EnvironmentsSection, daemonDisplayName } from '@/components/Settings/cloud/environments'

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

  // The self-hosted setup instructions are the SHARED onboarding panel, and
  // they must appear in BOTH states: with a control plane (adding another
  // machine) and without one (self-hosting is the only path that still works).
  it('renders the shared self-hosted setup instructions', async () => {
    renderSection()

    expect(
      await screen.findByRole('heading', {
        name: /run reliant on your own machine/i,
      }),
    ).toBeInTheDocument()
    // Rendered by SelfHostedDaemonConnect, not by this page.
    expect(
      screen.getByRole('heading', { name: /install reliant daemon/i }),
    ).toBeInTheDocument()
  })

  it('still renders the self-hosted setup instructions without a control plane', async () => {
    mocks.caps.cloudDaemons = false
    renderSection()

    expect(
      await screen.findByRole('heading', {
        name: /run reliant on your own machine/i,
      }),
    ).toBeInTheDocument()
  })

  describe('managed vs self-hosted grouping', () => {
    const managedDaemon = {
      id: 'dd67e516-d02c-49d0-8210-8749022aba61',
      name: 'onboarding-daemon',
      daemonType: 1, // MANAGED
      status: 2, // ACTIVE
      resources: { cpuRequest: '2', cpuLimit: '2', memoryRequest: '4Gi', memoryLimit: '4Gi' },
      storageSize: '20Gi',
      hostname: '',
      platform: '',
      size: 2,
      idleTimeout: '30m',
    }
    const externalDaemon = {
      id: '2a76a273-f04d-4a8a-8391-864ad4e018f1',
      name: '2a76a273-f04d-4a8a-8391-864ad4e018f1', // UUID fallback name, as seen in prod
      daemonType: 2, // EXTERNAL
      status: 2, // ACTIVE
      resources: undefined,
      storageSize: '',
      hostname: '',
      platform: 'darwin',
      size: 2, // invented by the backend fallback; must not be shown
      idleTimeout: '',
    }

    it('renders managed and self-hosted machines in separate groups', async () => {
      mocks.listDaemons.mockResolvedValue({ daemons: [managedDaemon, externalDaemon] })
      renderSection()

      expect(await screen.findByText('Cloud machines')).toBeInTheDocument()
      expect(screen.getByText('Self-hosted machines')).toBeInTheDocument()

      // Managed row keeps its real name.
      expect(screen.getByText('onboarding-daemon')).toBeInTheDocument()
      // External row gets the readable fallback, not the raw UUID.
      expect(screen.queryByText('2a76a273-f04d-4a8a-8391-864ad4e018f1')).not.toBeInTheDocument()
      expect(screen.getByText(/self-hosted machine \(2a76a273\)/i)).toBeInTheDocument()
    })

    it('shows no size/resources column and no Suspend control for self-hosted machines', async () => {
      mocks.listDaemons.mockResolvedValue({ daemons: [managedDaemon, externalDaemon] })
      renderSection()

      await screen.findByText('Self-hosted machines')

      // Managed table still has a Resources column and a Suspend action.
      expect(screen.getByRole('columnheader', { name: /resources/i })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /suspend/i })).toBeInTheDocument()

      // Self-hosted table shows Platform instead, and only ONE Resources
      // column exists in the document (the managed table's) — the
      // self-hosted table does not add a second one.
      expect(screen.getByRole('columnheader', { name: /platform/i })).toBeInTheDocument()
      expect(screen.getAllByRole('columnheader', { name: /^resources$/i })).toHaveLength(1)
      expect(screen.getByText('darwin')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /remove/i })).toBeInTheDocument()
      // Suspend appears exactly once — on the managed row, not the self-hosted one.
      expect(screen.getAllByRole('button', { name: /suspend/i })).toHaveLength(1)
    })

    it('omits an empty group instead of rendering an empty table', async () => {
      mocks.listDaemons.mockResolvedValue({ daemons: [managedDaemon] })
      renderSection()

      expect(await screen.findByText('Cloud machines')).toBeInTheDocument()
      expect(screen.queryByText('Self-hosted machines')).not.toBeInTheDocument()
    })
  })

  describe('daemonDisplayName', () => {
    it('keeps a real, human-chosen name as-is', () => {
      expect(
        daemonDisplayName({ id: 'dd67e516-d02c-49d0-8210-8749022aba61', name: 'onboarding-daemon', hostname: '' }),
      ).toBe('onboarding-daemon')
    })

    it('falls back to the hostname when the name is a bare UUID', () => {
      expect(
        daemonDisplayName({
          id: '2a76a273-f04d-4a8a-8391-864ad4e018f1',
          name: '2a76a273-f04d-4a8a-8391-864ad4e018f1',
          hostname: "seans-macbook",
        }),
      ).toBe('seans-macbook')
    })

    it('falls back to a short id label when name and hostname are both unusable', () => {
      expect(
        daemonDisplayName({
          id: '2a76a273-f04d-4a8a-8391-864ad4e018f1',
          name: '2a76a273-f04d-4a8a-8391-864ad4e018f1',
          hostname: '',
        }),
      ).toBe('Self-hosted machine (2a76a273)')
    })

    it('falls back when name equals id but is not UUID-shaped', () => {
      expect(daemonDisplayName({ id: 'abc123', name: 'abc123', hostname: '' })).toBe(
        'Self-hosted machine (abc123)',
      )
    })

    it('falls back when the name is empty', () => {
      expect(daemonDisplayName({ id: 'abc123', name: '', hostname: 'my-laptop' })).toBe('my-laptop')
    })
  })
})