import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const mocks = vi.hoisted(() => ({
  listDaemons: vi.fn(async () => ({ daemons: [] as unknown[] })),
}))

vi.mock('@/hooks/useCloudBillingQueries', () => ({
  usePlans: () => ({ data: { plans: [] }, isLoading: false }),
}))
vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: vi.fn(async () => ({
    eligible: true, reason: 0, hasActiveSubscription: true,
    grantedMinutesRemaining: 0, planName: 'Compute Small', allowedDaemonSizes: ['small'],
  })),
}))
vi.mock('@/lib/event-context', () => ({
  useEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}))
vi.mock('@/hooks/useDaemonStatus', () => ({
  useDaemonStatus: () => ({ daemons: [], activeDaemon: undefined, loading: false, refresh: vi.fn() }),
}))
vi.mock('@/services/controlPlane/capabilities', () => ({
  capabilities: { cloudDaemons: true },
}))
vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({}),
  useParams: () => ({}),
  useNavigate: () => vi.fn(),
}))
vi.mock('@/services/controlPlane/environments', () => ({
  DaemonSize: { DAEMON_SIZE_UNSPECIFIED: 0, DAEMON_SIZE_SMALL: 1, DAEMON_SIZE_MEDIUM: 2, DAEMON_SIZE_LARGE: 3, DAEMON_SIZE_XL: 4 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  portAccessRulesQueryKey: (id: string) => ['cp', 'ports', id],
  getComputeSubscription: vi.fn(async () => ({})),
  listDaemonTokens: vi.fn(async () => []),
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
vi.mock('@/api/grpc-client', () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons: async () => mocks.listDaemons() }) },
}))

import {
  MachinesSection,
  canRemoveDaemon,
  CONNECTED_MACHINE_REMOVE_REASON,
} from '@/components/Settings/cloud/machines'
import { DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'

const row = (name: string, daemonType: string, status: number) => ({
  daemonId: `id-${name}`,
  hostname: name,
  daemonType,
  status,
  platform: 'darwin',
  size: 'small',
})

describe('canRemoveDaemon', () => {
  it('blocks only connected self-hosted/external machines', () => {
    expect(canRemoveDaemon({ daemonType: 'self_hosted', status: DaemonStatus.ACTIVE })).toEqual({
      allowed: false,
      reason: CONNECTED_MACHINE_REMOVE_REASON,
    })
    expect(canRemoveDaemon({ daemonType: 'external', status: DaemonStatus.ACTIVE }).allowed).toBe(false)
    expect(canRemoveDaemon({ daemonType: 'self_hosted', status: DaemonStatus.DISCONNECTED }).allowed).toBe(true)
    expect(canRemoveDaemon({ daemonType: 'managed', status: DaemonStatus.ACTIVE }).allowed).toBe(true)
  })
})

describe('MachinesSection — remove guard', () => {
  beforeEach(() => vi.clearAllMocks())

  it('disables Remove for a connected local machine, enables it when disconnected, leaves cloud Delete alone', async () => {
    mocks.listDaemons.mockResolvedValue({
      daemons: [
        row('live-laptop', 'self_hosted', DaemonStatus.ACTIVE),
        row('old-laptop', 'self_hosted', DaemonStatus.DISCONNECTED),
        row('cloud-box', 'managed', DaemonStatus.ACTIVE),
      ],
    })
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <MachinesSection />
      </QueryClientProvider>,
    )

    await screen.findByText('live-laptop')
    const remove = (name: string) =>
      within(screen.getByText(name).closest('tr')!).getAllByRole('button').at(-1)!
    expect(remove('live-laptop')).toBeDisabled()
    expect(remove('old-laptop')).toBeEnabled()
    expect(remove('cloud-box')).toBeEnabled()
  })
})
