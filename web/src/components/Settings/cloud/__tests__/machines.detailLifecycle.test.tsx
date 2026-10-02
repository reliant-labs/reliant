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

import { MachinesSection } from '@/components/Settings/cloud/machines'

const ACTIVE = 2
const SUSPENDED = 3
const DISCONNECTED = 4
const FAILED = 5

const PHASE_READY = 3
const PHASE_SUSPENDED = 5

function cloudDaemon(over: Record<string, unknown> = {}) {
  return {
    id: 'd-1',
    name: 'owner-machine',
    daemonType: 1, // MANAGED
    status: ACTIVE,
    lifecyclePhase: PHASE_READY,
    resources: { cpuRequest: '2', memoryRequest: '4Gi' },
    storageSize: '20Gi',
    hostname: '',
    platform: '',
    size: 2,
    idleTimeout: '30m',
    lastStatusMessage: '',
    ...over,
  }
}

function selfHostedDaemon(over: Record<string, unknown> = {}) {
  return {
    id: 'd-1',
    name: 'seans-macbook',
    daemonType: 2, // EXTERNAL
    status: ACTIVE,
    lifecyclePhase: 0,
    resources: undefined,
    storageSize: '',
    hostname: 'seans-macbook',
    platform: 'darwin',
    size: 0,
    idleTimeout: '',
    lastStatusMessage: '',
    ...over,
  }
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
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon(),
      workspaceBaseDomain: '',
    })
    renderDetail()

    expect(await screen.findByRole('button', { name: /^suspend$/i })).toBeEnabled()
    expect(screen.getByRole('button', { name: /^restart$/i })).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^resume$/i })).not.toBeInTheDocument()
  })

  it('offers Resume — and neither Suspend nor Restart — on a suspended cloud machine', async () => {
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon({ status: SUSPENDED, lifecyclePhase: PHASE_SUSPENDED }),
      workspaceBaseDomain: '',
    })
    renderDetail()

    expect(await screen.findByRole('button', { name: /^resume$/i })).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^suspend$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^restart$/i })).not.toBeInTheDocument()
  })

  // ResumeDaemon refuses anything that is not SUSPENDED, so this button can
  // only ever produce an error. It is disabled and says why instead.
  it('disables Resume on a FAILED machine and explains why', async () => {
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon({ status: FAILED, lifecyclePhase: 6 }),
      workspaceBaseDomain: '',
    })
    renderDetail()

    const resume = await screen.findByRole('button', { name: /^resume$/i })
    expect(resume).toBeDisabled()
    expect(resume).toHaveAttribute('title', expect.stringMatching(/failed/i))
  })

  it('shows no lifecycle buttons for a self-hosted machine, and says why', async () => {
    mocks.getDaemon.mockResolvedValue({
      daemon: selfHostedDaemon(),
      workspaceBaseDomain: '',
    })
    renderDetail()

    await screen.findByRole('heading', { name: /seans-macbook/i })
    expect(screen.queryByRole('button', { name: /^suspend$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^resume$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^restart$/i })).not.toBeInTheDocument()
    expect(screen.getByText(/runs on your own hardware/i)).toBeInTheDocument()
  })

  it('confirms first, then restarts by suspending and resuming in order', async () => {
    const user = userEvent.setup()
    // Poll: the pod is gone on the first read after suspend.
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon({ status: SUSPENDED, lifecyclePhase: PHASE_SUSPENDED }),
      workspaceBaseDomain: '',
    })
    // First render must be the RUNNING machine so Restart is offered.
    mocks.getDaemon.mockResolvedValueOnce({
      daemon: cloudDaemon(),
      workspaceBaseDomain: '',
    })
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
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon({ status: DISCONNECTED, lifecyclePhase: PHASE_READY }),
      workspaceBaseDomain: '',
    })
    renderDetail()

    const header = await screen.findByTestId('machine-detail-header')
    expect(
      within(header).getAllByText(/disconnected/i),
    ).toHaveLength(1)
  })

  it('renders one badge for a running machine too', async () => {
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon(),
      workspaceBaseDomain: '',
    })
    renderDetail()

    const header = await screen.findByTestId('machine-detail-header')
    expect(within(header).getAllByText(/^active$/i)).toHaveLength(1)
    // "Connected" was a second badge saying the same thing as "Active".
    expect(within(header).queryByText(/^connected$/i)).not.toBeInTheDocument()
  })

  // "Connected at: <timestamp>" beside a disconnected machine reads as a
  // live connection. The timestamp is the LAST one.
  it('labels the connection timestamp "Last connected" while disconnected', async () => {
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon({
        status: DISCONNECTED,
        connectedAt: { seconds: 1750000000n, nanos: 0 },
      }),
      workspaceBaseDomain: '',
    })
    renderDetail()

    expect(await screen.findByText(/last connected/i)).toBeInTheDocument()
    expect(screen.queryByText(/^connected at$/i)).not.toBeInTheDocument()
  })

  it('still says "Connected at" while the machine is actually connected', async () => {
    mocks.getDaemon.mockResolvedValue({
      daemon: cloudDaemon({ connectedAt: { seconds: 1750000000n, nanos: 0 } }),
      workspaceBaseDomain: '',
    })
    renderDetail()

    expect(await screen.findByText(/^connected at$/i)).toBeInTheDocument()
    expect(screen.queryByText(/last connected/i)).not.toBeInTheDocument()
  })
})
