import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Machine detail: Resize.
 *
 * The control plane resizes only a SUSPENDED machine and applies the size at
 * the next start (control-plane docs/design/daemon-resize.md). So the same
 * button does two different things, and these tests pin both:
 *   - suspended → one ResizeDaemon call, nothing started;
 *   - running   → stop, wait for the pod to go, resize, start — and a refused
 *                 resize still starts the machine again.
 * Plus the choices the modal offers: the plan decides which sizes are
 * clickable, and a downsize says the disk does not shrink.
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] as unknown[] })),
  getDaemon: vi.fn(),
  getComputeEligibility: vi.fn(async () => ({
    eligible: true,
    reason: 0,
    hasActiveSubscription: true,
    grantedSmallDaemonSecondsRemaining: 0,
    planName: 'Compute Large',
    allowedDaemonSizes: ['small', 'medium', 'large'],
  })),
  suspendDaemon: vi.fn(async () => {}),
  resumeEnvironment: vi.fn(async () => {}),
  resizeDaemon: vi.fn(async () => {}),
  navigate: vi.fn(),
}))

// The Add machine sheet (billing checkout, catalog) is not under test here.
vi.mock('@/components/Machines/AddMachineSheet', () => ({ AddMachineSheet: () => null }))
vi.mock('@/components/Machines/PairMachinePanel', () => ({ PairMachinePanel: () => null }))
vi.mock('@/hooks/useCloudBillingQueries', () => ({ useComputeUsage: () => ({ data: { allowanceWeightedSeconds: '576000/1', remainingWeightedSeconds: '3600/1' } }), usePlans: () => ({ data: { daemonPricing: { pricingVersion: 'test-version', sizes: [{ size: 'small', costWeight: '1/1', exactHourlyPriceCents: '13/1', hourlyPriceCents: '13', storageGib: 120 }, { size: 'large', costWeight: '4/1', exactHourlyPriceCents: '52/1', hourlyPriceCents: '52', storageGib: 180 }], storageCentsPerGibMonth: '25', placeholder: false } } }) }));

vi.mock('@/services/controlPlane/billing', () => ({
  getComputeEligibility: mocks.getComputeEligibility,
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

// The real enum shape: SIZE_TIERS keys on DaemonSize.DAEMON_SIZE_*, and the
// resize call is asserted with those numbers.
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
  resizeDaemon: mocks.resizeDaemon,
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
}))

import { MachinesSection } from '@/components/Settings/cloud/machines'
import { DaemonLifecyclePhase, DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'
import { setTransport } from '@/lib/connect'
import { daemonRegistryTransport } from '@/test/daemonRegistry'

// The daemon list is read through connectClient(DaemonRegistryService).
beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons: () => mocks.listDaemons() })))
afterEach(() => setTransport(null))

const LARGE = 3
const XL = 4

function machine(over: Record<string, unknown> = {}) {
  return {
    daemonId: 'd-1',
    hostname: 'dev-box',
    daemonType: 'managed',
    status: DaemonStatus.ACTIVE,
    lifecyclePhase: DaemonLifecyclePhase.READY,
    platform: '',
    size: 'medium',
    lastStatusMessage: '',
    ...over,
  }
}

const suspended = (over: Record<string, unknown> = {}) =>
  machine({ status: DaemonStatus.SUSPENDED, lifecyclePhase: DaemonLifecyclePhase.SUSPENDED, ...over })

function showMachine(row: Record<string, unknown>) {
  mocks.listDaemons.mockResolvedValue({ daemons: [row] })
  mocks.getDaemon.mockResolvedValue({
    daemon: { id: row.daemonId, storageSize: '40Gi', idleTimeout: '30m', disk: { capacityGib: 200, includedGib: 80, requestedGib: 200 } },
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

async function openResize(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: /^resize$/i }))
  return screen.findByRole('dialog')
}

describe('machine detail — resize', () => {
  beforeEach(() => {
    mocks.caps.cloudDaemons = true
    vi.clearAllMocks()
    mocks.suspendDaemon.mockResolvedValue(undefined)
    mocks.resumeEnvironment.mockResolvedValue(undefined)
    mocks.resizeDaemon.mockResolvedValue(undefined)
  })

  it('offers Resize on a managed machine but not on a self-hosted one', async () => {
    showMachine(machine())
    const { unmount } = renderDetail()
    expect(await screen.findByRole('button', { name: /^resize$/i })).toBeEnabled()
    unmount()

    showMachine(machine({ daemonType: 'self_hosted', hostname: 'laptop' }))
    renderDetail()
    await screen.findByRole('heading', { name: /laptop/i })
    expect(screen.queryByRole('button', { name: /^resize$/i })).not.toBeInTheDocument()
  })

  it('marks the current size and disables sizes the plan does not include', async () => {
    const user = userEvent.setup()
    showMachine(suspended())
    renderDetail()
    const dialog = await openResize(user)

    await waitFor(() => expect(within(dialog).getByTestId('resize-size-large')).toBeEnabled())
    expect(within(dialog).getByTestId('resize-size-medium')).toBeDisabled()
    expect(within(dialog).getByTestId('resize-size-medium')).toHaveTextContent(/current/i)
    expect(within(dialog).queryByTestId('resize-size-xl')).not.toBeInTheDocument()
    expect(within(dialog).getByTestId('resize-size-large')).not.toHaveTextContent(/\$|hrs|GiB|suspended|included/i)
    // Plan includes 160 small hours; largest allowed size here is large (x4).
    expect(within(dialog).getByTestId('resize-pricing-note')).toHaveTextContent(
      'Includes 40.0 hours a month. Extra hours and storage are billed separately.',
    )
    expect(dialog).not.toHaveTextContent(/×|uses included hours|burn/i)
    // Nothing is chosen until the user chooses.
    expect(within(dialog).getByRole('button', { name: /^resize$/i })).toBeDisabled()
  })

  it('resizes a suspended machine with one call and starts nothing', async () => {
    const user = userEvent.setup()
    showMachine(suspended())
    renderDetail()
    const dialog = await openResize(user)

    expect(within(dialog).getByTestId('resize-effect')).toHaveTextContent(/next time it starts/i)
    await waitFor(() => expect(within(dialog).getByTestId('resize-size-large')).toBeEnabled())
    await user.click(within(dialog).getByTestId('resize-size-large'))
    await user.click(within(dialog).getByRole('button', { name: /^resize$/i }))

    await waitFor(() => expect(mocks.resizeDaemon).toHaveBeenCalledWith('d-1', LARGE))
    expect(mocks.suspendDaemon).not.toHaveBeenCalled()
    expect(mocks.resumeEnvironment).not.toHaveBeenCalled()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('resizes a running machine by stopping it, resizing, then starting it', async () => {
    const user = userEvent.setup()
    // Running on first render; SUSPENDED once the stop has landed, which is
    // what the sequence polls for before it resizes.
    showMachine(suspended())
    mocks.listDaemons.mockResolvedValueOnce({ daemons: [machine()] })
    renderDetail()
    const dialog = await openResize(user)

    expect(within(dialog).getByTestId('resize-effect')).toHaveTextContent(/disconnect/i)
    await waitFor(() => expect(within(dialog).getByTestId('resize-size-large')).toBeEnabled())
    await user.click(within(dialog).getByTestId('resize-size-large'))
    await user.click(within(dialog).getByRole('button', { name: /resize and restart/i }))

    await waitFor(() => expect(mocks.resumeEnvironment).toHaveBeenCalledTimes(1))
    expect(mocks.resizeDaemon).toHaveBeenCalledWith('d-1', LARGE)
    const order = [
      mocks.suspendDaemon.mock.invocationCallOrder[0],
      mocks.resizeDaemon.mock.invocationCallOrder[0],
      mocks.resumeEnvironment.mock.invocationCallOrder[0],
    ]
    expect(order).toEqual([...order].sort((a, b) => a - b))
  })

  it('starts a running machine again when its resize is refused, and shows why', async () => {
    const user = userEvent.setup()
    mocks.getComputeEligibility.mockResolvedValueOnce({
      eligible: true,
      reason: 0,
      hasActiveSubscription: true,
      grantedSmallDaemonSecondsRemaining: 0,
      planName: 'Compute XL',
      allowedDaemonSizes: ['small', 'medium', 'large', 'xl'],
    })
    mocks.resizeDaemon.mockRejectedValueOnce(new Error('your plan does not include daemon size xl'))
    showMachine(suspended())
    mocks.listDaemons.mockResolvedValueOnce({ daemons: [machine()] })
    renderDetail()
    const dialog = await openResize(user)

    await waitFor(() => expect(within(dialog).getByTestId('resize-size-xl')).toBeEnabled())
    await user.click(within(dialog).getByTestId('resize-size-xl'))
    await user.click(within(dialog).getByRole('button', { name: /resize and restart/i }))

    expect(await within(dialog).findByText(/does not include daemon size xl/i)).toBeInTheDocument()
    expect(mocks.resizeDaemon).toHaveBeenCalledWith('d-1', XL)
    expect(mocks.resumeEnvironment).toHaveBeenCalledTimes(1)
  })

  it('says the disk does not shrink when the new size is smaller', async () => {
    const user = userEvent.setup()
    showMachine(suspended({ size: 'large' }))
    renderDetail()
    const dialog = await openResize(user)

    await waitFor(() => expect(within(dialog).getByTestId('resize-size-small')).toBeEnabled())
    expect(within(dialog).queryByTestId('resize-disk-note')).not.toBeInTheDocument()
    await user.click(within(dialog).getByTestId('resize-size-small'))
    expect(within(dialog).getByTestId('resize-disk-note')).toHaveTextContent(/above the new size's standard is billed while running/i)
    const small = within(dialog).getByTestId('resize-size-small');
    expect(small).not.toHaveTextContent(/\$|hrs|GiB|suspended/i);
  })
})
