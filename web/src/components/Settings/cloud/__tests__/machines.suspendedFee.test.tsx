import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Per-daemon billing task J (design §6.2, "Machines list"): a SUSPENDED
 * machine still holds its disk, and the disk is billed — so each one shows
 * "$X/mo while suspended" beside Delete, the only control that stops it. The
 * fee is the size's disk (the server's one storage number) × the server's
 * per-GiB-month price; the client restates neither.
 *
 * FAILS ON main: the machines list showed no suspended fee at all.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  getComputeSubscription: vi.fn(async () => ({})),
  getComputeEligibility: vi.fn(async () => ({
    eligible: true,
    reason: 0,
    hasActiveSubscription: true,
    grantedMinutesRemaining: 0,
    planName: 'Compute Small',
    allowedDaemonSizes: ['small'],
  })),
  listDaemonTokens: vi.fn(async () => []),
  navigate: vi.fn(),
}))

// The machines gate reads compute eligibility from the server rather than
// deriving it from the subscription, so this module must be mocked for the
// section to render at all.
// The server's per-daemon price list (ListPlans daemon_pricing).
vi.mock('@/hooks/useCloudBillingQueries', () => ({
  usePlans: () => ({
    data: {
      plans: [],
      daemonPricing: {
        sizes: [
          { size: 'small', multiplier: 1n, hourlyPriceCents: '13.0000', storageGib: 25n },
          { size: 'large', multiplier: 4n, hourlyPriceCents: '52.0000', storageGib: 100n },
        ],
        suspendedDiskCentsPerGibMonth: '25.0000',
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
    DAEMON_SIZE_SMALL: 1,
    DAEMON_SIZE_MEDIUM: 2,
    DAEMON_SIZE_LARGE: 3,
    DAEMON_SIZE_XL: 4,
  },
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  // machines.tsx calls this at module scope to build its query-key table,
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

// The machine LIST comes from reliant's daemon registry
// (docs/design/one-daemon-list.md). It is fed from the same mocks.listDaemons
// the tests already drive, so their setup calls keep working unchanged.
vi.mock('@/api/grpc-client', () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: async () => mocks.listDaemons(),
    }),
  },
}))

import { MachinesSection } from '@/components/Settings/cloud/machines'
import { DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'

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
  hostname: name,
  daemonType: 'managed',
  status,
  platform: '',
  size,
})

describe('MachinesSection — suspended disk fee (J)', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.getComputeSubscription.mockResolvedValue({})
    mocks.getComputeEligibility.mockResolvedValue({
      eligible: true,
      reason: 0,
      hasActiveSubscription: true,
      grantedMinutesRemaining: 0,
      planName: 'Compute Large',
      allowedDaemonSizes: ['small', 'large'],
    })
    mocks.listDaemonTokens.mockResolvedValue([])
  })

  it('shows the monthly disk fee beside Delete for a suspended machine, and not for a running one', async () => {
    mocks.listDaemons.mockResolvedValue({
      daemons: [
        managed('parked-large', DaemonStatus.SUSPENDED, 'large'),
        managed('busy-small', DaemonStatus.ACTIVE, 'small'),
      ],
    })
    renderSection()

    await screen.findByText('parked-large')
    const fees = screen.getAllByTestId('machine-suspended-fee')
    // 100 GiB × $0.25/GiB-month = $25.00/mo — only the suspended one.
    expect(fees).toHaveLength(1)
    expect(fees[0]).toHaveTextContent('$25.00/mo while suspended')
    const row = screen.getByText('parked-large').closest('tr')!
    expect(within(row).getByTestId('machine-suspended-fee')).toBeInTheDocument()
  })
})
