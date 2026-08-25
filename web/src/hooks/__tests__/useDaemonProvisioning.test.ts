import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useDaemonProvisioning } from '../useDaemonProvisioning'

/**
 * These pin the disambiguation the onboarding compute step depends on.
 *
 * An EMPTY ListDaemons result is ambiguous on desktop: it means either "this
 * user has no daemon" or "this user's bundled daemon has not been credentialed
 * yet". Reading the second as the first is what put a fresh sign-in on the
 * compute-choice step for ~15 seconds while its own daemon was registering.
 */

const getBackendStatus = vi.fn()

const asDesktop = () => {
  ;(window as Window & { electronAPI?: unknown }).electronAPI = { getBackendStatus }
}

beforeEach(() => {
  vi.clearAllMocks()
  delete (window as Window & { electronAPI?: unknown }).electronAPI
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useDaemonProvisioning', () => {
  it('is false in a browser, where there is no bundled daemon', async () => {
    const { result } = renderHook(() => useDaemonProvisioning())

    // Web callers must see today's behaviour unchanged: an empty daemon list
    // there genuinely means "no daemon".
    await waitFor(() => expect(result.current).toBe(false))
    expect(getBackendStatus).not.toHaveBeenCalled()
  })

  it('reports provisioning while the daemon is awaiting credentials', async () => {
    asDesktop()
    getBackendStatus.mockResolvedValue({ awaitingCredentials: true })

    const { result } = renderHook(() => useDaemonProvisioning())

    await waitFor(() => expect(result.current).toBe(true))
  })

  it('clears once the daemon is credentialed', async () => {
    asDesktop()
    getBackendStatus.mockResolvedValue({ awaitingCredentials: true })

    const { result } = renderHook(() => useDaemonProvisioning())
    await waitFor(() => expect(result.current).toBe(true))

    // The daemon finishes registering — the poll must notice and stop, or the
    // step would sit on a spinner after the answer arrived.
    getBackendStatus.mockResolvedValue({ awaitingCredentials: false })
    await waitFor(() => expect(result.current).toBe(false), { timeout: 3000 })
  })

  it('is false when the daemon already has credentials', async () => {
    asDesktop()
    getBackendStatus.mockResolvedValue({ awaitingCredentials: false })

    const { result } = renderHook(() => useDaemonProvisioning())

    await waitFor(() => expect(result.current).toBe(false))
  })

  it('does not strand onboarding when the status read fails', async () => {
    asDesktop()
    getBackendStatus.mockRejectedValue(new Error('IPC gone'))

    const { result } = renderHook(() => useDaemonProvisioning())

    // A failed read must degrade to "not provisioning" so the user still gets
    // a usable step rather than an indefinite spinner.
    await waitFor(() => expect(result.current).toBe(false))
  })

  it('stops polling after unmount', async () => {
    asDesktop()
    getBackendStatus.mockResolvedValue({ awaitingCredentials: true })

    const { result, unmount } = renderHook(() => useDaemonProvisioning())
    await waitFor(() => expect(result.current).toBe(true))

    unmount()
    const callsAtUnmount = getBackendStatus.mock.calls.length
    await new Promise((resolve) => setTimeout(resolve, 1500))

    // A hook that kept polling after unmount would leak a timer per visit to
    // the step.
    expect(getBackendStatus.mock.calls.length).toBe(callsAtUnmount)
  })
})
