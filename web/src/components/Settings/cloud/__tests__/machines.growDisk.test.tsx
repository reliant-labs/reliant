import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Code, ConnectError } from '@connectrpc/connect'

/**
 * Machine detail: Grow disk (control-plane docs/design/workspace-disk-resize.md §5).
 *
 * A grow is one-way and billed, so these tests pin what the user is shown
 * before it and what each choice does:
 *   - it is offered only where the plan grows disks at all;
 *   - the stepper runs in 10 GiB steps from the next step to the plan limit,
 *     with the price, and nothing happens until the one-way warning is ticked;
 *   - suspended → one GrowDaemonDisk call, nothing started;
 *   - running → grow FIRST, then the Restart sequence, so a refused grow never
 *     stops the machine; or grow now and use the space after a later restart;
 *   - the page then says the disk is growing, failed, or needs a restart.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] as unknown[] })),
  getDaemon: vi.fn(),
  getComputeEligibility: vi.fn(),
  suspendDaemon: vi.fn(async () => {}),
  resumeEnvironment: vi.fn(async () => {}),
  growDaemonDisk: vi.fn(async () => undefined),
  navigate: vi.fn(),
}))

const eligibility = (maxDaemonStorageGib: number) => ({
  eligible: true,
  reason: 0,
  hasActiveSubscription: true,
  grantedMinutesRemaining: 0,
  planName: 'Compute XL',
  allowedDaemonSizes: ['small', 'medium', 'large', 'xl'],
  maxDaemonStorageGib,
})

vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: mocks.getComputeEligibility,
}))

vi.mock('@/hooks/useCloudBillingQueries', () => ({
  useComputeUsage: () => ({ data: undefined }),
  usePlans: () => ({
    data: {
      plans: [],
      daemonPricing: { sizes: [{ size: 'xl', multiplier: 8n, hourlyPriceCents: '104', storageGib: 260n }], suspendedDiskCentsPerGibMonth: '25.0000', placeholder: false },
    },
  }),
}))

vi.mock('@/lib/event-context', () => ({
  useEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}))

vi.mock('@/hooks/useDaemonStatus', () => ({
  useDaemonStatus: () => ({ daemons: [], activeDaemon: undefined, loading: false, refresh: vi.fn() }),
}))

vi.mock('@/services/controlPlane/capabilities', () => ({
  capabilities: mocks.caps,
}))

vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({ daemon: 'd-1' }),
  useNavigate: () => mocks.navigate,
  useParams: () => ({}),
}))

vi.mock('@/services/controlPlane/environments', () => ({
  DaemonStatus: { UNSPECIFIED: 0, PENDING: 1, ACTIVE: 2, SUSPENDED: 3, FAILED: 5, DISCONNECTED: 4 },
  DaemonSize: {
    DAEMON_SIZE_UNSPECIFIED: 0,
    DAEMON_SIZE_SMALL: 1,
    DAEMON_SIZE_MEDIUM: 2,
    DAEMON_SIZE_LARGE: 3,
    DAEMON_SIZE_XL: 4,
    DAEMON_SIZE_2XL: 5,
  },
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (e: unknown, fallback = 'error') => (e instanceof Error ? e.message : fallback),
  portAccessRulesQueryKey: (daemonId: string) => ['cp', 'ports', daemonId],
  getComputeSubscription: vi.fn(async () => ({})),
  getDaemon: mocks.getDaemon,
  deleteDaemon: vi.fn(),
  suspendDaemon: mocks.suspendDaemon,
  resumeEnvironment: mocks.resumeEnvironment,
  resizeDaemon: vi.fn(),
  growDaemonDisk: mocks.growDaemonDisk,
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
}))

vi.mock('@/api/grpc-client', () => ({
  grpcClient: {
    daemonRegistry: () => ({ listDaemons: async () => mocks.listDaemons() }),
  },
}))

import { MachinesSection } from '@/components/Settings/cloud/machines'
import { DaemonLifecyclePhase, DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'
import { DaemonDiskState } from '@/gen/controlplane/controlplane/v1/shared_pb'

function machine(over: Record<string, unknown> = {}) {
  return {
    daemonId: 'd-1',
    hostname: 'dev-box',
    daemonType: 'managed',
    status: DaemonStatus.ACTIVE,
    lifecyclePhase: DaemonLifecyclePhase.READY,
    platform: '',
    size: 'xl',
    lastStatusMessage: '',
    ...over,
  }
}

const suspended = (over: Record<string, unknown> = {}) =>
  machine({ status: DaemonStatus.SUSPENDED, lifecyclePhase: DaemonLifecyclePhase.SUSPENDED, ...over })

/** An xl machine's 160 GiB disk, all of it included in the hourly price. */
function showMachine(row: Record<string, unknown>, disk: Record<string, unknown> = {}) {
  mocks.listDaemons.mockResolvedValue({ daemons: [row] })
  mocks.getDaemon.mockResolvedValue({
    daemon: {
      id: row.daemonId,
      storageSize: '160Gi',
      idleTimeout: '30m',
      disk: {
        requestedGib: 160,
        capacityGib: 160,
        usableGib: row.status === DaemonStatus.SUSPENDED ? 0 : 160,
        includedGib: 160,
        state: DaemonDiskState.STABLE,
        message: '',
        ...disk,
      },
    },
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

async function openGrow(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: /^grow$/i }))
  return screen.findByTestId('grow-disk-dialog')
}

describe('machine detail — grow disk', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.getComputeEligibility.mockResolvedValue(eligibility(320))
    mocks.suspendDaemon.mockResolvedValue(undefined)
    mocks.resumeEnvironment.mockResolvedValue(undefined)
    mocks.growDaemonDisk.mockResolvedValue(undefined)
  })

  it('is not offered when the plan does not grow disks', async () => {
    mocks.getComputeEligibility.mockResolvedValue(eligibility(0))
    showMachine(machine())
    renderDetail()

    expect(await screen.findByText('260 GiB disk')).toBeInTheDocument()
    await waitFor(() => expect(mocks.getComputeEligibility).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: /^grow$/i })).not.toBeInTheDocument()
  })

  it('steps 10 GiB at a time up to the plan limit, with the price, and waits for the acknowledgement', async () => {
    const user = userEvent.setup()
    showMachine(suspended())
    renderDetail()
    const dialog = await openGrow(user)

    expect(within(dialog).getByTestId('grow-disk-target')).toHaveTextContent('270 GiB')
    expect(within(dialog).getByRole('button', { name: /10 GiB smaller/i })).toBeDisabled()
    expect(within(dialog).getByText(/up to 420 GiB/i)).toBeInTheDocument()
    expect(within(dialog).getByTestId('grow-disk-price')).toHaveTextContent('Adds +$2.50/mo')

    await user.click(within(dialog).getByRole('button', { name: /10 GiB larger/i }))
    expect(within(dialog).getByTestId('grow-disk-target')).toHaveTextContent('280 GiB')
    expect(within(dialog).getByTestId('grow-disk-price')).toHaveTextContent('Adds +$5.00/mo')
    // Stopped, the whole disk bills: 180 GiB × 25¢.
    expect(within(dialog).getByTestId('grow-disk-price')).toHaveTextContent('$70.00/mo for the whole 280 GiB disk until deleted')

    const confirm = within(dialog).getByRole('button', { name: /^grow disk$/i })
    expect(confirm).toBeDisabled()
    expect(within(dialog).getByTestId('grow-disk-warning')).toHaveTextContent(/can only grow/i)
    await user.click(within(dialog).getByRole('checkbox', { name: /can't be made smaller/i }))
    expect(confirm).toBeEnabled()
  })

  it('grows a suspended machine with one call and starts nothing', async () => {
    const user = userEvent.setup()
    showMachine(suspended())
    renderDetail()
    const dialog = await openGrow(user)

    expect(within(dialog).getByText(/uses the space when it next starts/i)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('checkbox', { name: /can't be made smaller/i }))
    await user.click(within(dialog).getByRole('button', { name: /^grow disk$/i }))

    await waitFor(() => expect(mocks.growDaemonDisk).toHaveBeenCalledWith('d-1', 170))
    expect(mocks.suspendDaemon).not.toHaveBeenCalled()
    expect(mocks.resumeEnvironment).not.toHaveBeenCalled()
    await waitFor(() => expect(screen.queryByTestId('grow-disk-dialog')).not.toBeInTheDocument())
  })

  it('grows a running machine first, then restarts it, by default', async () => {
    const user = userEvent.setup()
    // Running on first render; SUSPENDED once the stop has landed, which is
    // what the restart sequence polls for before it starts the machine again.
    showMachine(suspended())
    mocks.listDaemons.mockResolvedValueOnce({ daemons: [machine()] })
    renderDetail()
    const dialog = await openGrow(user)

    expect(within(dialog).getByRole('radio', { name: /grow and restart now/i })).toBeChecked()
    expect(within(dialog).getByText(/disconnect/i)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('checkbox', { name: /can't be made smaller/i }))
    await user.click(within(dialog).getByRole('button', { name: /^grow and restart$/i }))

    await waitFor(() => expect(mocks.resumeEnvironment).toHaveBeenCalledTimes(1))
    expect(mocks.growDaemonDisk).toHaveBeenCalledWith('d-1', 170)
    const order = [
      mocks.growDaemonDisk.mock.invocationCallOrder[0],
      mocks.suspendDaemon.mock.invocationCallOrder[0],
      mocks.resumeEnvironment.mock.invocationCallOrder[0],
    ]
    expect(order).toEqual([...order].sort((a, b) => a - b))
  })

  it('can grow a running machine without restarting it', async () => {
    const user = userEvent.setup()
    showMachine(machine())
    renderDetail()
    const dialog = await openGrow(user)

    await user.click(within(dialog).getByRole('radio', { name: /use the space after the next restart/i }))
    await user.click(within(dialog).getByRole('checkbox', { name: /can't be made smaller/i }))
    await user.click(within(dialog).getByRole('button', { name: /^grow disk$/i }))

    await waitFor(() => expect(mocks.growDaemonDisk).toHaveBeenCalledWith('d-1', 170))
    await waitFor(() => expect(screen.queryByTestId('grow-disk-dialog')).not.toBeInTheDocument())
    expect(mocks.suspendDaemon).not.toHaveBeenCalled()
  })

  it('never stops a running machine for a grow the plan refuses, and points at the plans', async () => {
    const user = userEvent.setup()
    mocks.growDaemonDisk.mockRejectedValueOnce(
      new ConnectError('320 GiB is more than your plan allows', Code.PermissionDenied, {
        'x-reliant-reason': 'daemon_storage_denied',
      }),
    )
    showMachine(machine())
    renderDetail()
    const dialog = await openGrow(user)

    await user.click(within(dialog).getByRole('checkbox', { name: /can't be made smaller/i }))
    await user.click(within(dialog).getByRole('button', { name: /^grow and restart$/i }))

    expect(await within(dialog).findByText(/more than your plan allows/i)).toBeInTheDocument()
    expect(mocks.suspendDaemon).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: /see plans/i }))
    expect(mocks.navigate).toHaveBeenCalled()
  })

  it('says a disk at the plan limit cannot grow, and offers no confirm', async () => {
    const user = userEvent.setup()
    showMachine(suspended(), { requestedGib: 320, capacityGib: 320 })
    renderDetail()
    const dialog = await openGrow(user)

    expect(within(dialog).getByTestId('grow-disk-at-limit')).toHaveTextContent(/already 420 GiB/i)
    expect(within(dialog).getByRole('button', { name: /see plans/i })).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: /^grow disk$/i })).not.toBeInTheDocument()
  })

  it('shows a grow in progress', async () => {
    showMachine(machine(), { requestedGib: 200, state: DaemonDiskState.EXPANDING })
    renderDetail()
    expect(await screen.findByTestId('disk-grow-notice')).toHaveTextContent('Growing the disk to 300 GiB…')
  })

  it('shows why a grow failed, and that nothing extra is billed', async () => {
    showMachine(machine(), { requestedGib: 200, state: DaemonDiskState.FAILED, message: 'The cloud disk could not be resized.' })
    renderDetail()
    const notice = await screen.findByTestId('disk-grow-notice')
    expect(notice).toHaveTextContent(/couldn't grow the disk to 300 GiB/i)
    expect(notice).toHaveTextContent('The cloud disk could not be resized.')
    expect(notice).toHaveTextContent(/nothing extra is billed/i)
  })

  it('asks for a restart when the running machine has not picked up the space yet', async () => {
    const user = userEvent.setup()
    showMachine(machine(), { requestedGib: 200, capacityGib: 200, usableGib: 160 })
    renderDetail()
    const notice = await screen.findByTestId('disk-grow-notice')
    expect(notice).toHaveTextContent(/Restart .+ to use the extra 40 GiB on its disk/)

    await user.click(within(notice).getByRole('button', { name: /^restart$/i }))
    expect(await screen.findByRole('heading', { name: /restart machine/i })).toBeInTheDocument()
  })

  it('offers Grow disk beside Free up space on a volume under pressure', async () => {
    const row = machine({
      disk: {
        reportedAtMs: BigInt(Date.now()),
        volumes: [
          { label: 'home', paths: ['/home/workspace'], totalBytes: 100n, freeBytes: 2n, pressure: true, consumers: [] },
        ],
      },
    })
    showMachine(row)
    renderDetail()
    const card = await screen.findByTestId('machine-disk-card')
    const volume = within(card).getByTestId('machine-disk-volume')
    await waitFor(() => expect(within(volume).getByRole('button', { name: /grow disk/i })).toBeInTheDocument())
    expect(within(volume).getByRole('button', { name: /free up space/i })).toBeInTheDocument()
  })
})
