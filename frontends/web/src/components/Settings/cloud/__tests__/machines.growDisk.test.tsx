import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
  grantedSmallDaemonSecondsRemaining: 0,
  planName: 'Compute XL',
  allowedDaemonSizes: ['m', 'l', 'xl', '2xl'],
  maxDaemonStorageGib,
})

// The Add machine sheet (billing checkout, catalog) is not under test here.
vi.mock('@/components/Machines/AddMachineSheet', () => ({ AddMachineSheet: () => null }))
vi.mock('@/components/Machines/PairMachinePanel', () => ({ PairMachinePanel: () => null }))
vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: mocks.getComputeEligibility,
}))

vi.mock('@/hooks/useCloudBillingQueries', () => ({
  useComputeUsage: () => ({ data: undefined }),
  usePlans: () => ({
    data: {
      plans: [],
      daemonPricing: { sizes: [{ size: '2xl', multiplier: 8n, hourlyPriceCents: '104', storageGib: 260n }], storageCentsPerGibMonth: '25.0000', placeholder: false },
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
    DAEMON_SIZE_M: 8,
    DAEMON_SIZE_L: 9,
    DAEMON_SIZE_XL: 10,
    DAEMON_SIZE_2XL: 11,
    DAEMON_SIZE_3XL: 12,
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

import { MachinesSection } from '@/components/Settings/cloud/machines'
import { DaemonLifecyclePhase, DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'
import { DaemonDiskState } from '@/gen/controlplane/controlplane/v1/shared_pb'
import { DiskReportSchema } from '@/gen/reliant/v1/tools_daemon_pb'
import { create } from '@bufbuild/protobuf'
import { setTransport } from '@/lib/connect'
import { daemonRegistryTransport } from '@/test/daemonRegistry'

// The daemon list is read through connectClient(DaemonRegistryService).
beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons: () => mocks.listDaemons() })))
afterEach(() => setTransport(null))

function machine(over: Record<string, unknown> = {}) {
  return {
    daemonId: 'd-1',
    hostname: 'dev-box',
    daemonType: 'managed',
    status: DaemonStatus.ACTIVE,
    lifecyclePhase: DaemonLifecyclePhase.READY,
    platform: '',
    size: '2xl',
    lastStatusMessage: '',
    ...over,
  }
}

const suspended = (over: Record<string, unknown> = {}) =>
  machine({ status: DaemonStatus.SUSPENDED, lifecyclePhase: DaemonLifecyclePhase.SUSPENDED, ...over })

/** An xl machine's 260 GB disk: its size's standard, one disk. */
function showMachine(row: Record<string, unknown>, disk: Record<string, unknown> = {}) {
  mocks.listDaemons.mockResolvedValue({ daemons: [row] })
  mocks.getDaemon.mockResolvedValue({
    daemon: {
      id: row.daemonId,
      storageSize: '260Gi',
      idleTimeout: '30m',
      disk: {
        requestedGib: 260,
        capacityGib: 260,
        usableGib: row.status === DaemonStatus.SUSPENDED ? 0 : 260,
        includedGib: 260,
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
    mocks.getComputeEligibility.mockResolvedValue(eligibility(420))
    mocks.suspendDaemon.mockResolvedValue(undefined)
    mocks.resumeEnvironment.mockResolvedValue(undefined)
    mocks.growDaemonDisk.mockResolvedValue(undefined)
  })

  it('is not offered when the plan does not grow disks', async () => {
    mocks.getComputeEligibility.mockResolvedValue(eligibility(0))
    showMachine(machine())
    renderDetail()

    expect(await screen.findByText('260 GB disk')).toBeInTheDocument()
    await waitFor(() => expect(mocks.getComputeEligibility).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: /^grow$/i })).not.toBeInTheDocument()
  })

  it('steps 10 GiB at a time up to the plan limit, with the price, and waits for the acknowledgement', async () => {
    const user = userEvent.setup()
    showMachine(suspended())
    renderDetail()
    const dialog = await openGrow(user)

    expect(within(dialog).getByTestId('grow-disk-target')).toHaveTextContent('270 GB')
    expect(within(dialog).getByRole('button', { name: /10 GB smaller/i })).toBeDisabled()
    expect(within(dialog).getByText(/up to 420 GB/i)).toBeInTheDocument()
    expect(within(dialog).getByTestId('grow-disk-price')).toHaveTextContent('Up to +$2.50/mo')

    await user.click(within(dialog).getByRole('button', { name: /10 GB larger/i }))
    expect(within(dialog).getByTestId('grow-disk-target')).toHaveTextContent('280 GB')
    expect(within(dialog).getByTestId('grow-disk-price')).toHaveTextContent('Up to +$5.00/mo')
    // Pooled storage: only what is beyond the plan's included storage bills.
    expect(within(dialog).getByTestId('grow-disk-price')).toHaveTextContent(/beyond your plan's included storage/)

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

    await waitFor(() => expect(mocks.growDaemonDisk).toHaveBeenCalledWith('d-1', 270))
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
    expect(mocks.growDaemonDisk).toHaveBeenCalledWith('d-1', 270)
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

    await waitFor(() => expect(mocks.growDaemonDisk).toHaveBeenCalledWith('d-1', 270))
    await waitFor(() => expect(screen.queryByTestId('grow-disk-dialog')).not.toBeInTheDocument())
    expect(mocks.suspendDaemon).not.toHaveBeenCalled()
  })

  it('never stops a running machine for a grow the plan refuses, and points at the plans', async () => {
    const user = userEvent.setup()
    mocks.growDaemonDisk.mockRejectedValueOnce(
      new ConnectError('520 GB is more than your plan allows', Code.PermissionDenied, {
        'x-forge-error-reason': 'daemon_storage_denied',
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
    showMachine(suspended(), { requestedGib: 420, capacityGib: 420 })
    renderDetail()
    const dialog = await openGrow(user)

    expect(within(dialog).getByTestId('grow-disk-at-limit')).toHaveTextContent(/already 420 GB/i)
    expect(within(dialog).getByRole('button', { name: /see plans/i })).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: /^grow disk$/i })).not.toBeInTheDocument()
  })

  it('shows a grow in progress', async () => {
    showMachine(machine(), { requestedGib: 300, state: DaemonDiskState.EXPANDING })
    renderDetail()
    expect(await screen.findByTestId('disk-grow-notice')).toHaveTextContent('Growing the disk to 300 GB…')
  })

  it('shows why a grow failed, and that nothing extra is billed', async () => {
    showMachine(machine(), { requestedGib: 300, state: DaemonDiskState.FAILED, message: 'The cloud disk could not be resized.' })
    renderDetail()
    const notice = await screen.findByTestId('disk-grow-notice')
    expect(notice).toHaveTextContent(/couldn't grow the disk to 300 GB/i)
    expect(notice).toHaveTextContent('The cloud disk could not be resized.')
    expect(notice).toHaveTextContent(/nothing extra is billed/i)
  })

  it('asks for a restart when the running machine has not picked up the space yet', async () => {
    const user = userEvent.setup()
    showMachine(machine(), { requestedGib: 300, capacityGib: 300, usableGib: 260 })
    renderDetail()
    const notice = await screen.findByTestId('disk-grow-notice')
    expect(notice).toHaveTextContent(/Restart .+ to use the extra 40 GB on its disk/)

    await user.click(within(notice).getByRole('button', { name: /^restart$/i }))
    expect(await screen.findByRole('heading', { name: /restart machine/i })).toBeInTheDocument()
  })

  it('offers Grow disk beside Free up space on a volume under pressure', async () => {
    // A real message, as the wire decodes one: every field the card reads is
    // set, to its default where the test does not care about it.
    const row = machine({
      disk: create(DiskReportSchema, {
        reportedAtMs: BigInt(Date.now()),
        volumes: [{ label: 'home', paths: ['/home/workspace'], totalBytes: 100n, freeBytes: 2n, pressure: true }],
      }),
    })
    showMachine(row)
    renderDetail()
    const card = await screen.findByTestId('machine-disk-card')
    const volume = within(card).getByTestId('machine-disk-volume')
    await waitFor(() => expect(within(volume).getByRole('button', { name: /grow disk/i })).toBeInTheDocument())
    expect(within(volume).getByRole('button', { name: /free up space/i })).toBeInTheDocument()
  })
})
