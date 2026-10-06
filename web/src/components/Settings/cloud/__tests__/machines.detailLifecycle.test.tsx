import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Machine DETAIL view: lifecycle actions and status presentation.
 *
 * Rendered via the `?daemon=<id>` deep link, which is the same entry point
 * the onboarding gate uses, so these tests exercise the real detail view
 * rather than a hand-mounted subcomponent.
 *
 * Three defects are pinned here, all visible in the owner's screenshot:
 *   1. No suspend / resume / restart on a cloud machine — only "Remove".
 *   2. TWO "Disconnected" badges in the header (the status badge and a
 *      separate connection badge both render it, in different colors).
 *   3. "Connected at" captioning a timestamp while the machine is
 *      disconnected, which reads as a current connection.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  getDaemon: vi.fn(),
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
  suspendDaemon: vi.fn(async () => {}),
  resumeEnvironment: vi.fn(async () => {}),
  navigate: vi.fn(),
}))

vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: mocks.getComputeEligibility,
}))

vi.mock('@/lib/event-context', () => ({
  useEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}))

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

// The deep link: `?daemon=<id>` opens the detail view directly.
vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({ daemon: 'd-1' }),
  useNavigate: () => mocks.navigate,
  useParams: () => ({}),
}))

vi.mock('@/services/controlPlane/environments', () => ({
  DaemonStatus: {
    UNSPECIFIED: 0,
    PENDING: 1,
    ACTIVE: 2,
    SUSPENDED: 3,
    FAILED: 5,
    DISCONNECTED: 4,
  },
  DaemonSize: { UNSPECIFIED: 0, SMALL: 1, MEDIUM: 2, LARGE: 3, XL: 4 },
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  portAccessRulesQueryKey: (daemonId: string) => ['cp', 'ports', daemonId],
  listDaemons: mocks.listDaemons,
  getComputeSubscription: mocks.getComputeSubscription,
  listDaemonTokens: mocks.listDaemonTokens,
  getDaemon: mocks.getDaemon,
  createEnvironment: vi.fn(),
  deleteDaemon: vi.fn(),
  suspendDaemon: mocks.suspendDaemon,
  resumeEnvironment: mocks.resumeEnvironment,
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
import { DaemonLifecyclePhase, DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'

const ACTIVE = DaemonStatus.ACTIVE
const SUSPENDED = DaemonStatus.SUSPENDED
const DISCONNECTED = DaemonStatus.DISCONNECTED
const FAILED = DaemonStatus.FAILED

const PHASE_READY = DaemonLifecyclePhase.READY
const PHASE_SUSPENDED = DaemonLifecyclePhase.SUSPENDED

// A registry row: identity, status and lifecycle. This is what the detail
// view's lifecycle buttons read (docs/design/one-daemon-list.md).
function cloudDaemon(over: Record<string, unknown> = {}) {
  return {
    daemonId: 'd-1',
    hostname: 'owner-machine',
    daemonType: 'managed',
    status: ACTIVE,
    lifecyclePhase: PHASE_READY,
    platform: '',
    size: 'medium',
    lastStatusMessage: '',
    ...over,
  }
}

function selfHostedDaemon(over: Record<string, unknown> = {}) {
  return {
    daemonId: 'd-1',
    hostname: 'seans-macbook',
    daemonType: 'self_hosted',
    status: ACTIVE,
    lifecyclePhase: DaemonLifecyclePhase.UNSPECIFIED,
    platform: 'darwin',
    size: '',
    lastStatusMessage: '',
    ...over,
  }
}

// The control-plane half: the provisioning spec the registry deliberately does
// not carry. Constant across these tests, which are about lifecycle gating.
const CP_SPEC = {
  resources: { cpuRequest: '2', memoryRequest: '4Gi' },
  storageSize: '20Gi',
  idleTimeout: '30m',
  updatedAt: undefined,
}

/**
 * Point BOTH halves at one machine.
 *
 * The detail view reads status and lifecycle from the registry list and the
 * spec from control-plane's GetDaemon, so a fixture that set only one would
 * leave the other empty and the test would fail for the wrong reason.
 */
function showMachine(row: Record<string, unknown>) {
  mocks.listDaemons.mockResolvedValue({ daemons: [row] })
  mocks.getDaemon.mockResolvedValue({
    daemon: { ...CP_SPEC, id: row.daemonId },
    workspaceBaseDomain: '',
  })
}

function renderDetail() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MachinesSection />
    </QueryClientProvider>,
  )
}

describe('machine detail — lifecycle actions', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.listDaemons.mockResolvedValue({ daemons: [] })
    mocks.suspendDaemon.mockResolvedValue(undefined)
    mocks.resumeEnvironment.mockResolvedValue(undefined)
  })

  it('offers Suspend and Restart on a running cloud machine', async () => {
    showMachine(cloudDaemon())
    renderDetail()

    expect(await screen.findByRole('button', { name: /^suspend$/i })).toBeEnabled()
    expect(screen.getByRole('button', { name: /^restart$/i })).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^resume$/i })).not.toBeInTheDocument()
  })

  it('offers Resume — and neither Suspend nor Restart — on a suspended cloud machine', async () => {
    showMachine(cloudDaemon({ status: SUSPENDED, lifecyclePhase: PHASE_SUSPENDED }))
    renderDetail()

    expect(await screen.findByRole('button', { name: /^resume$/i })).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^suspend$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^restart$/i })).not.toBeInTheDocument()
  })

  // A FAILED machine cannot be resumed (ResumeDaemon wants SUSPENDED), but it
  // CAN be suspended, which is the user's way out of a wedged start.
  it('offers Suspend on a FAILED machine, hides Resume, and explains the retry', async () => {
    showMachine(cloudDaemon({ status: FAILED, lifecyclePhase: 6 }))
    renderDetail()

    const suspend = await screen.findByRole('button', { name: /^suspend$/i })
    expect(suspend).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^resume$/i })).not.toBeInTheDocument()
    expect(screen.getByTestId('machine-recovery-hint')).toHaveTextContent(/suspend.*resume/i)
  })

  it('shows no lifecycle buttons for a self-hosted machine, and says why', async () => {
    showMachine(selfHostedDaemon())
    renderDetail()

    await screen.findByRole('heading', { name: /seans-macbook/i })
    expect(screen.queryByRole('button', { name: /^suspend$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^resume$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^restart$/i })).not.toBeInTheDocument()
    expect(screen.getByText(/runs on your own hardware/i)).toBeInTheDocument()
  })

  it('confirms first, then restarts by suspending and resuming in order', async () => {
    const user = userEvent.setup()
    // The machine starts RUNNING (the only state that offers Restart) and
    // reaches SUSPENDED once suspend lands — which is the transition
    // restartMachine polls for before it resumes. Both the first render and
    // that poll now read the registry list, so the sequence lives there.
    showMachine(cloudDaemon({ status: SUSPENDED, lifecyclePhase: PHASE_SUSPENDED }))
    mocks.listDaemons.mockResolvedValueOnce({ daemons: [cloudDaemon()] })
    renderDetail()

    await user.click(await screen.findByRole('button', { name: /^restart$/i }))

    // Confirmation names the consequence before anything happens.
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/disconnect/i)).toBeInTheDocument()
    expect(mocks.suspendDaemon).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /restart machine/i }))

    await waitFor(() => expect(mocks.resumeEnvironment).toHaveBeenCalledTimes(1))
    expect(mocks.suspendDaemon).toHaveBeenCalledTimes(1)
    expect(mocks.suspendDaemon).toHaveBeenCalledWith('d-1')
    expect(mocks.resumeEnvironment).toHaveBeenCalledWith('d-1')
    // Order: suspend's call settled before resume was invoked.
    expect(mocks.suspendDaemon.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.resumeEnvironment.mock.invocationCallOrder[0],
    )
  })
})

describe('machine detail — status presentation', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.listDaemons.mockResolvedValue({ daemons: [] })
  })

  // The screenshot bug: a disconnected machine rendered "Disconnected"
  // twice in the header — once as the status badge (red) and once as a
  // separate connection badge (grey). One state, one badge.
  it('renders exactly one status badge in the header when disconnected', async () => {
    showMachine(cloudDaemon({ status: DISCONNECTED, lifecyclePhase: PHASE_READY }))
    renderDetail()

    const header = await screen.findByTestId('machine-detail-header')
    expect(
      within(header).getAllByText(/disconnected/i),
    ).toHaveLength(1)
  })

  it('renders one badge for a running machine too', async () => {
    showMachine(cloudDaemon())
    renderDetail()

    const header = await screen.findByTestId('machine-detail-header')
    expect(within(header).getAllByText(/^active$/i)).toHaveLength(1)
    // "Connected" was a second badge saying the same thing as "Active".
    expect(within(header).queryByText(/^connected$/i)).not.toBeInTheDocument()
  })

  // "Connected at: <timestamp>" beside a disconnected machine reads as a
  // live connection. The timestamp is the LAST one.
  it('labels the connection timestamp "Last connected" while disconnected', async () => {
    showMachine(
      cloudDaemon({
        status: DISCONNECTED,
        connectedAt: { seconds: 1750000000n, nanos: 0 },
      }),
    )
    renderDetail()

    expect(await screen.findByText(/last connected/i)).toBeInTheDocument()
    expect(screen.queryByText(/^connected at$/i)).not.toBeInTheDocument()
  })

  it('still says "Connected at" while the machine is actually connected', async () => {
    showMachine(cloudDaemon({ connectedAt: { seconds: 1750000000n, nanos: 0 } }))
    renderDetail()

    expect(await screen.findByText(/^connected at$/i)).toBeInTheDocument()
    expect(screen.queryByText(/last connected/i)).not.toBeInTheDocument()
  })
})
