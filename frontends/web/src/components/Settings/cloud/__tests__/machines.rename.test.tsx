import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ConnectError, Code } from '@connectrpc/connect'

const mocks = vi.hoisted(() => ({
  listDaemons: vi.fn(async () => ({ daemons: [] as unknown[] })),
  renameMachine: vi.fn(async (_req: { daemonId: string; name: string }) => ({})),
}))

vi.mock('@/components/Machines/AddMachineSheet', () => ({ AddMachineSheet: () => null }))
vi.mock('@/components/Machines/PairMachinePanel', () => ({ PairMachinePanel: () => null }))
vi.mock('@/hooks/useCloudBillingQueries', () => ({
  useComputeUsage: () => ({ data: undefined }),
  usePlans: () => ({ data: { plans: [] }, isLoading: false }),
}))
vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: vi.fn(async () => ({
    eligible: true, reason: 0, hasActiveSubscription: true,
    grantedSmallDaemonSecondsRemaining: 0, planName: 'Compute Small', allowedDaemonSizes: ['small'],
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
  describeError: (e: unknown, fallback = 'error') => (e instanceof Error && e.message ? e.message : fallback),
  portAccessRulesQueryKey: (id: string) => ['cp', 'ports', id],
  getComputeSubscription: vi.fn(async () => ({})),
  getDaemon: vi.fn(),
  createEnvironment: vi.fn(),
  deleteDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  resumeEnvironment: vi.fn(),
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
}))
import { MachinesSection } from '@/components/Settings/cloud/machines'
import { machineNameProblem } from '@/components/Settings/cloud/RenameMachine'
import { DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'
import { setTransport } from '@/lib/connect'
import { daemonRegistryTransport } from '@/test/daemonRegistry'

beforeEach(() =>
  setTransport(
    daemonRegistryTransport({
      listDaemons: () => mocks.listDaemons(),
      renameMachine: (req: { daemonId: string; name: string }) => mocks.renameMachine(req),
    }),
  ),
)
afterEach(() => setTransport(null))

const machine = (name: string, daemonType = 'managed') => ({
  daemonId: `id-${name}`,
  name,
  hostname: `ws-ws-${name}`,
  daemonType,
  status: DaemonStatus.ACTIVE,
  size: 'small',
})

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <MachinesSection />
    </QueryClientProvider>,
  )
}

describe('machineNameProblem', () => {
  it('accepts the control plane rule and nothing else', () => {
    expect(machineNameProblem('big-box')).toBeNull()
    expect(machineNameProblem(' cloud-2xl ')).toBeNull()
    for (const bad of ['', 'Big-Box', 'big box', '-box', 'box-', 'a'.repeat(64), 'box_1']) {
      expect(machineNameProblem(bad), bad).not.toBeNull()
    }
  })
})

describe('MachinesSection — rename', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renames a machine inline from its row, by id, and re-reads the list', async () => {
    mocks.listDaemons.mockResolvedValue({ daemons: [machine('cloud-2xl'), machine('cicd')] })
    renderSection()

    fireEvent.click(await screen.findByRole('button', { name: 'Rename cloud-2xl' }))
    const input = screen.getByRole('textbox', { name: 'Machine name' })
    expect(input).toHaveValue('cloud-2xl')

    const reads = mocks.listDaemons.mock.calls.length
    mocks.listDaemons.mockResolvedValue({ daemons: [machine('big-box'), machine('cicd')] })
    fireEvent.change(input, { target: { value: 'big-box' } })
    fireEvent.submit(input.closest('form')!)

    await waitFor(() => expect(mocks.renameMachine).toHaveBeenCalledTimes(1))
    expect(mocks.renameMachine.mock.calls[0][0]).toMatchObject({ daemonId: 'id-cloud-2xl', name: 'big-box' })
    expect(await screen.findByText('big-box')).toBeInTheDocument()
    expect(mocks.listDaemons.mock.calls.length).toBeGreaterThan(reads)
    expect(screen.queryByRole('textbox', { name: 'Machine name' })).not.toBeInTheDocument()
  })

  it('refuses a malformed name without a request, and shows a server refusal in place', async () => {
    mocks.listDaemons.mockResolvedValue({ daemons: [machine('cloud-2xl'), machine('cicd')] })
    renderSection()

    fireEvent.click(await screen.findByRole('button', { name: 'Rename cloud-2xl' }))
    const input = screen.getByRole('textbox', { name: 'Machine name' })
    fireEvent.change(input, { target: { value: 'Big Box' } })
    fireEvent.submit(input.closest('form')!)
    expect(await screen.findByRole('alert')).toHaveTextContent('lowercase')
    expect(mocks.renameMachine).not.toHaveBeenCalled()

    mocks.renameMachine.mockRejectedValueOnce(
      new ConnectError('a machine named "cicd" already exists; choose another name', Code.AlreadyExists),
    )
    fireEvent.change(input, { target: { value: 'cicd' } })
    fireEvent.submit(input.closest('form')!)
    expect(await screen.findByRole('alert')).toHaveTextContent('already exists')
    // Still editing: the user picks another name or cancels.
    fireEvent.keyDown(input, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Machine name' })).not.toBeInTheDocument())
    const row = screen.getByText('cloud-2xl').closest('tr')!
    expect(within(row).getByRole('button', { name: 'Rename cloud-2xl' })).toBeInTheDocument()
  })
})
