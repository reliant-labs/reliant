/**
 * The daemon detail screen, rendered for real against a stubbed data layer.
 *
 * `daemonPresentation.test.ts` pins the gating table; this pins that the
 * screen actually consults it — the failure mode being a Resume button
 * rendered unconditionally, which no unit test of the table would catch.
 */

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DaemonStatus, DaemonSize } from '@/gen/controlplane/v1/public/shared_pb'
import type { Daemon } from '@/services/controlPlane/daemon'

const resumeMutate = vi.fn()
let daemons: Daemon[] = []

vi.mock('@/hooks/useOnboardingQueries', () => ({
  useDaemonList: () => ({ data: daemons, isLoading: false }),
  useResumeDaemon: () => ({ mutate: resumeMutate, isPending: false }),
}))

// The screen only needs Link/useParams; the real router would want a whole
// route tree, which `mobileDaemonRoutes.test.tsx` already covers.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useParams: () => ({ daemonId: 'd1' }),
}))

const { MobileDaemonScreen } = await import('../MobileDaemonScreen')

function daemon(overrides: Partial<Daemon> = {}): Daemon {
  return {
    id: 'd1',
    name: 'work-box',
    status: DaemonStatus.ACTIVE,
    size: DaemonSize.MEDIUM,
    gitRepo: 'reliant-labs/reliant',
    gitBranch: 'main',
    hostname: '',
    platform: '',
    idleTimeout: '30m',
    lastStatusMessage: '',
    ...overrides,
  } as Daemon
}

beforeEach(() => {
  resumeMutate.mockClear()
})

describe('MobileDaemonScreen', () => {
  it('shows the machine name, status and size', () => {
    daemons = [daemon()]
    render(<MobileDaemonScreen />)
    expect(screen.getByText('work-box')).toBeInTheDocument()
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(screen.getByText('Medium')).toBeInTheDocument()
  })

  it('does NOT offer Resume for a running daemon', () => {
    daemons = [daemon({ status: DaemonStatus.ACTIVE })]
    render(<MobileDaemonScreen />)
    expect(screen.queryByRole('button', { name: /resume/i })).not.toBeInTheDocument()
  })

  it('offers Resume for a suspended daemon and calls the shared mutation', async () => {
    daemons = [daemon({ status: DaemonStatus.SUSPENDED })]
    render(<MobileDaemonScreen />)

    const button = screen.getByRole('button', { name: /resume/i })
    await userEvent.click(button)
    expect(resumeMutate).toHaveBeenCalledWith('d1')
  })

  it('offers Resume for a disconnected daemon', () => {
    daemons = [daemon({ status: DaemonStatus.DISCONNECTED })]
    render(<MobileDaemonScreen />)
    expect(screen.getByRole('button', { name: /resume/i })).toBeInTheDocument()
  })

  it('never renders suspend or delete — those are daemonManage', () => {
    daemons = [daemon({ status: DaemonStatus.ACTIVE })]
    render(<MobileDaemonScreen />)
    expect(screen.queryByRole('button', { name: /suspend/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete/i })).not.toBeInTheDocument()
  })

  it('surfaces the gateway status message when the daemon is unhealthy', () => {
    daemons = [
      daemon({
        status: DaemonStatus.DISCONNECTED,
        lastStatusMessage: 'dial tcp: i/o timeout',
      }),
    ]
    render(<MobileDaemonScreen />)
    expect(screen.getByText('dial tcp: i/o timeout')).toBeInTheDocument()
  })

  it('reports a missing machine instead of rendering an empty shell', () => {
    // Deep link to a deleted machine, or one owned by another account.
    daemons = []
    render(<MobileDaemonScreen />)
    expect(screen.getByText('Machine not found')).toBeInTheDocument()
  })
})
