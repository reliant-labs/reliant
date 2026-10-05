import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/**
 * Machine → Access: the outside AI apps (ChatGPT, Claude, their mobile apps)
 * granted access to ONE machine. This replaced the standalone Settings →
 * Connectors section, so the behaviours pinned here are the ones that section
 * used to own, now scoped to the machine they act on:
 *
 *   - the detail view lists only THIS machine's grants;
 *   - creating a grant binds it to this machine, with no picker to get wrong;
 *   - revoking works;
 *   - the machine list says which machines have apps attached;
 *   - all of it still works without a control plane, where connectors can
 *     exist (the connector RPCs are reliant's, not control-plane's).
 */

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  search: {} as { daemon?: string },
  listDaemons: vi.fn(async () => ({ daemons: [] as unknown[] })),
  getDaemon: vi.fn(),
  getComputeEligibility: vi.fn(async () => ({
    eligible: true,
    reason: 0,
    hasActiveSubscription: true,
    grantedMinutesRemaining: 0,
    planName: 'Compute Small',
    allowedDaemonSizes: ['small'],
  })),
  listConnectors: vi.fn(async () => ({ connectors: [] as unknown[] })),
  listAvailableTools: vi.fn(async () => ({ tools: [] as unknown[] })),
  listConnectorActivity: vi.fn(async () => ({ activity: [] as unknown[] })),
  createConnector: vi.fn(),
  revokeConnector: vi.fn(async () => ({ revoked: true })),
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

vi.mock('@tanstack/react-router', () => ({
  useSearch: () => mocks.search,
  useNavigate: () => mocks.navigate,
  useParams: () => ({}),
}))

vi.mock('@/services/controlPlane/environments', () => ({
  DaemonSize: { UNSPECIFIED: 0, SMALL: 1, MEDIUM: 2, LARGE: 3, XL: 4 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = 'error') => fallback,
  portAccessRulesQueryKey: (daemonId: string) => ['cp', 'ports', daemonId],
  getComputeSubscription: vi.fn(async () => ({})),
  getDaemon: mocks.getDaemon,
  createEnvironment: vi.fn(),
  deleteDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  resumeEnvironment: vi.fn(),
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
}))

vi.mock('@/api/grpc-client', () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: async () => mocks.listDaemons(),
    }),
    connector: () => ({
      listConnectors: mocks.listConnectors,
      listAvailableTools: mocks.listAvailableTools,
      listConnectorActivity: mocks.listConnectorActivity,
      createConnector: mocks.createConnector,
      revokeConnector: mocks.revokeConnector,
    }),
  },
}))

import { MachinesSection } from '@/components/Settings/cloud/machines'
import { DaemonLifecyclePhase, DaemonStatus } from '@/gen/reliant/v1/daemon_registry_pb'
import { ConnectorExecMode } from '@/gen/reliant/v1/connector_pb'

function machine(over: Record<string, unknown> = {}) {
  return {
    daemonId: 'd-1',
    hostname: 'build-box',
    daemonType: 'managed',
    status: DaemonStatus.ACTIVE,
    lifecyclePhase: DaemonLifecyclePhase.READY,
    platform: 'linux',
    size: 'medium',
    lastStatusMessage: '',
    projects: [],
    ...over,
  }
}

function grant(over: Record<string, unknown> = {}) {
  return {
    id: 'g-1',
    daemonId: 'd-1',
    name: 'ChatGPT on my phone',
    tokenPrefix: 'rlc_abcd',
    allowedTools: ['read_file'],
    pathRoot: '/workspace',
    execMode: ConnectorExecMode.DENY,
    execAllowlist: [],
    createdAt: '2026-01-01T00:00:00Z',
    ...over,
  }
}

const TOOLS = [
  { name: 'read_file', description: 'Read a file', mutating: false, needsExec: false },
  { name: 'write_file', description: 'Write a file', mutating: true, needsExec: false },
  { name: 'run_command', description: 'Run a command', mutating: true, needsExec: true },
]

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MachinesSection />
    </QueryClientProvider>,
  )
}

async function accessRegion() {
  return screen.findByRole('region', { name: /^access$/i })
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.caps.cloudDaemons = true
  mocks.search = { daemon: 'd-1' }
  mocks.listDaemons.mockResolvedValue({
    daemons: [machine(), machine({ daemonId: 'd-2', hostname: 'other-box' })],
  })
  mocks.getDaemon.mockResolvedValue({
    daemon: { id: 'd-1', storageSize: '20Gi', idleTimeout: '30m' },
    workspaceBaseDomain: '',
  })
  mocks.listConnectors.mockResolvedValue({
    connectors: [
      grant(),
      grant({ id: 'g-2', daemonId: 'd-2', name: 'Claude on the other box' }),
    ],
  })
  mocks.listAvailableTools.mockResolvedValue({ tools: TOOLS })
  mocks.listConnectorActivity.mockResolvedValue({ activity: [] })
  mocks.createConnector.mockResolvedValue({
    credential: 'rlc_secret_value',
    mcpUrl: 'https://reliant.example/mcp',
    connector: grant({ id: 'g-new', name: 'Claude desktop' }),
  })
})

describe('machine detail — Access', () => {
  it("lists only this machine's grants", async () => {
    renderSection()
    const region = await accessRegion()

    expect(await within(region).findByText('ChatGPT on my phone')).toBeInTheDocument()
    expect(within(region).queryByText('Claude on the other box')).not.toBeInTheDocument()
  })

  it('reads activity per grant on this machine, not the whole account', async () => {
    renderSection()
    const region = await accessRegion()
    await within(region).findByText('ChatGPT on my phone')

    await waitFor(() => expect(mocks.listConnectorActivity).toHaveBeenCalled())
    const grantIds = mocks.listConnectorActivity.mock.calls.map(
      (call) => (call as unknown as [{ grantId?: string }])[0].grantId,
    )
    expect(grantIds).toEqual(['g-1'])
  })

  it("creates a grant bound to this machine's daemonId, with no machine picker", async () => {
    const user = userEvent.setup()
    renderSection()
    const region = await accessRegion()

    await user.click(await within(region).findByRole('button', { name: /give an app access/i }))

    // No way to point the grant at a different machine.
    expect(within(region).queryByRole('combobox', { name: /workspace|machine/i })).not.toBeInTheDocument()

    await user.type(within(region).getByLabelText(/^name$/i), 'Claude desktop')
    await user.click(within(region).getByRole('button', { name: /grant access/i }))

    await waitFor(() => expect(mocks.createConnector).toHaveBeenCalledTimes(1))
    const req = (mocks.createConnector.mock.calls[0] as unknown as [Record<string, unknown>])[0]
    expect(req).toMatchObject({
      daemonId: 'd-1',
      name: 'Claude desktop',
      // A managed pod's projects are cloned under this root (the workspace
      // operator sets HOME and DAEMON_WORKING_DIR to /home/workspace). The old
      // standalone form defaulted to "/workspace", which does not exist there.
      pathRoot: '/home/workspace/projects',
      // Least privilege by default: read-only tools, no shell.
      allowedTools: ['read_file'],
      execMode: ConnectorExecMode.DENY,
    })

    // The credential is revealed once.
    expect(await within(region).findByDisplayValue('rlc_secret_value')).toBeInTheDocument()
  })

  it('makes the user choose a directory on a personal machine, and warns', async () => {
    const user = userEvent.setup()
    mocks.listDaemons.mockResolvedValue({
      daemons: [
        machine({
          daemonType: 'self_hosted',
          hostname: 'my-laptop',
          projects: [{ path: '/Users/me/code/reliant' }],
        }),
      ],
    })
    renderSection()
    const region = await accessRegion()

    await user.click(await within(region).findByRole('button', { name: /give an app access/i }))
    expect(within(region).getByText(/your own computer, not a disposable sandbox/i)).toBeInTheDocument()
    await user.type(within(region).getByLabelText(/^name$/i), 'Claude')

    // No default root: granting must wait until one is chosen.
    expect(within(region).getByLabelText(/allowed directory/i)).toHaveValue('')
    expect(within(region).getByRole('button', { name: /grant access/i })).toBeDisabled()

    // A project the machine reports is offered as a one-click root.
    await user.click(within(region).getByRole('button', { name: '/Users/me/code/reliant' }))
    await user.click(within(region).getByRole('button', { name: /grant access/i }))

    await waitFor(() => expect(mocks.createConnector).toHaveBeenCalledTimes(1))
    expect(mocks.createConnector.mock.calls[0]).toEqual([
      expect.objectContaining({ daemonId: 'd-1', pathRoot: '/Users/me/code/reliant' }),
    ])
  })

  it('revokes a grant after confirmation', async () => {
    const user = userEvent.setup()
    renderSection()
    const region = await accessRegion()
    await within(region).findByText('ChatGPT on my phone')

    await user.click(within(region).getByRole('button', { name: /^revoke$/i }))
    expect(mocks.revokeConnector).not.toHaveBeenCalled()
    await user.click(within(region).getByRole('button', { name: /confirm/i }))

    await waitFor(() => expect(mocks.revokeConnector).toHaveBeenCalledTimes(1))
    expect(mocks.revokeConnector.mock.calls[0]).toEqual([expect.objectContaining({ id: 'g-1' })])
    // The list is re-read so the revoked state shows.
    await waitFor(() => expect(mocks.listConnectors.mock.calls.length).toBeGreaterThan(1))
  })

  it('shows Access without a control plane, and does not ask control-plane for the machine', async () => {
    mocks.caps.cloudDaemons = false
    mocks.listDaemons.mockResolvedValue({
      daemons: [machine({ daemonType: 'self_hosted', size: '', lifecyclePhase: 0 })],
    })
    renderSection()

    const region = await accessRegion()
    expect(await within(region).findByText('ChatGPT on my phone')).toBeInTheDocument()
    expect(mocks.getDaemon).not.toHaveBeenCalled()
  })
})

describe('machines list — app indicator', () => {
  it('marks a machine that has apps attached', async () => {
    mocks.search = {}
    mocks.listConnectors.mockResolvedValue({
      connectors: [
        grant(),
        grant({ id: 'g-3', name: 'Claude' }),
        // Revoked grants no longer reach the machine, so they are not counted.
        grant({ id: 'g-4', name: 'Old', revokedAt: '2026-01-02T00:00:00Z' }),
      ],
    })
    renderSection()

    expect(await screen.findByText('2 apps')).toBeInTheDocument()
    // d-2 has none, so exactly one indicator renders.
    expect(screen.queryAllByText(/\d+ apps?$/)).toHaveLength(1)
  })

  it('lists registered machines without a control plane', async () => {
    mocks.caps.cloudDaemons = false
    mocks.search = {}
    mocks.listDaemons.mockResolvedValue({
      daemons: [machine({ daemonType: 'self_hosted', hostname: 'my-laptop' })],
    })
    renderSection()

    expect(await screen.findByRole('button', { name: 'my-laptop' })).toBeInTheDocument()
  })
})
