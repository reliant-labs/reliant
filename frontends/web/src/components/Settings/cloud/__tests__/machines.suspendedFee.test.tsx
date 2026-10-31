import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * A SUSPENDED machine still holds its disk, and disk counts toward the org's
 * pooled storage (pricing v2) — so each suspended row shows its disk size
 * beside Delete, the only control that frees it. There is no separate
 * "suspended fee": storage costs the same running or suspended, and the
 * storage total lives on the billing page.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] as unknown[] })),
  getComputeSubscription: vi.fn(async () => ({})),
  getDaemon: vi.fn(async () => ({ daemon: { disk: { capacityGib: 200, requestedGib: 240, includedGib: 80 } } })),
  getComputeEligibility: vi.fn(async () => ({
    eligible: true,
    reason: 0,
    hasActiveSubscription: true,
    grantedSmallDaemonSecondsRemaining: 0,
    planName: 'Compute Small',
    allowedDaemonSizes: ['m'],
  })),
  navigate: vi.fn(),
}))

// The machines gate reads compute eligibility from the server rather than
// deriving it from the subscription, so this module must be mocked for the
// section to render at all.
// The server's per-daemon price list (ListPlans daemon_pricing).
// The Add machine sheet (billing checkout, catalog) is not under test here.
vi.mock('@/components/Machines/AddMachineSheet', () => ({ AddMachineSheet: () => null }))
vi.mock('@/components/Machines/PairMachinePanel', () => ({ PairMachinePanel: () => null }))
vi.mock('@/hooks/useCloudBillingQueries', () => ({
  useComputeUsage: () => ({ data: undefined }),
  usePlans: () => ({
    data: {
      plans: [],
      daemonPricing: {
        sizes: [
          { size: 'm', multiplier: 1n, hourlyPriceCents: '13.0000', storageGib: 25n },
          { size: 'xl', multiplier: 4n, hourlyPriceCents: '52.0000', storageGib: 180n },
        ],
        storageCentsPerGibMonth: '25.0000',
        placeholder: true,
      },
    },
    isLoading: false,
  }),
}))

vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: mocks.getComputeEligibility,
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

// useNavigate backs useGoToBilling, which the un-funded prompt routes through.
vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({}),
  useNavigate: () => mocks.navigate,
  useParams: () => ({}),
}))

// The real module exports proto enums at module scope that machines.tsx
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
  DaemonSize: {
    DAEMON_SIZE_UNSPECIFIED: 0,
    DAEMON_SIZE_M: 8,
    DAEMON_SIZE_L: 9,
    DAEMON_SIZE_XL: 10,
    DAEMON_SIZE_2XL: 11,
  },
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  // machines.tsx calls this at module scope to build its query-key table,
  // so it must exist on the mock or the module fails to evaluate.
  portAccessRulesQueryKey: (daemonId: string) => ['cp', 'ports', daemonId],
  listDaemons: mocks.listDaemons,
  getComputeSubscription: mocks.getComputeSubscription,
  getDaemon: mocks.getDaemon,
  createEnvironment: vi.fn(),
  deleteDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  resumeEnvironment: vi.fn(),
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
}))

import { MachinesSection } from '@/components/Settings/cloud/machines'
import { DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'
import { setTransport } from '@/lib/connect'
import { daemonRegistryTransport } from '@/test/daemonRegistry'

// The daemon list is read through connectClient(DaemonRegistryService).
beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons: () => mocks.listDaemons() })))
afterEach(() => setTransport(null))

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MachinesSection />
    </QueryClientProvider>,
  )
}


// A registry row. `size` is the lifecycle mirror's tier NAME, which is also
// what the fee lookup keys on (SIZE_TIERS → the server's per-size disk).
const managed = (name: string, status: number, size: string) => ({
  daemonId: `00000000-0000-4000-8000-${name.length.toString().padStart(12, '0')}${status}`.slice(0, 36),
  name,
  hostname: `ws-ws-${name}`,
  daemonType: 'managed',
  status,
  platform: '',
  size,
})

describe('MachinesSection — suspended machine disk', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.getComputeSubscription.mockResolvedValue({})
    mocks.getComputeEligibility.mockResolvedValue({
      eligible: true,
      reason: 0,
      hasActiveSubscription: true,
      grantedSmallDaemonSecondsRemaining: 0,
      planName: 'Compute Large',
      allowedDaemonSizes: ['m', 'xl'],
    })
  })

  it('shows the disk beside Delete for a suspended machine, and not for a running one', async () => {
    mocks.listDaemons.mockResolvedValue({
      daemons: [
        managed('parked-large', DaemonStatus.SUSPENDED, 'xl'),
        managed('busy-small', DaemonStatus.ACTIVE, 'm'),
      ],
    })
    renderSection()

    await screen.findByText('parked-large')
    const fees = screen.getAllByTestId('machine-suspended-fee')
    // Only the suspended one; its one provisioned disk, no per-machine fee.
    expect(fees).toHaveLength(1)
    expect(await screen.findByText(/200 GB disk/)).toBeInTheDocument()
    expect(fees[0]).not.toHaveTextContent(/while suspended/)
    const row = screen.getByText('parked-large').closest('tr')!
    expect(within(row).getByTestId('machine-suspended-fee')).toBeInTheDocument()
  })
})
