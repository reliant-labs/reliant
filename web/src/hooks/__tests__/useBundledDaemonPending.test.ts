import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'

// The session anchor inside the hook is module-scoped ON PURPOSE — it has to
// survive the renderer remount that follows the post-sign-in daemon restart,
// which is what broke the previous implementation. That also means it
// survives between tests, so each test imports a fresh module instance.
const loadHook = async () => {
  vi.resetModules()
  return (await import('../useBundledDaemonPending')).useBundledDaemonPending
}

/**
 * Pins the disambiguation onboarding's compute step depends on: an empty
 * ListDaemons result means "no daemon" in a browser, but on desktop it may
 * mean "the bundled daemon has not registered yet".
 *
 * The regression these guard against is specific and was shipped twice: an
 * earlier version keyed on a wall-clock budget, which (a) restarted when the
 * renderer remounted after the post-sign-in daemon restart, and (b) could only
 * bound the wait, never end it early — so the user sat through the whole
 * timeout and then landed on the daemon page anyway.
 */

beforeEach(() => {
  vi.clearAllMocks()
  delete (window as Window & { electronAPI?: unknown }).electronAPI
})

afterEach(() => {
  vi.useRealTimers()
})

const asDesktop = () => {
  ;(window as Window & { electronAPI?: unknown }).electronAPI = {}
}

describe('useBundledDaemonPending', () => {
  it('is false in a browser, where an empty list really means no daemon', async () => {
    const useBundledDaemonPending = await loadHook()
    const { result } = renderHook(() => useBundledDaemonPending(false))
    expect(result.current).toBe(false)
  })

  it('waits on desktop while no daemon has appeared', async () => {
    asDesktop()
    const useBundledDaemonPending = await loadHook()
    const { result } = renderHook(() => useBundledDaemonPending(false))
    expect(result.current).toBe(true)
  })

  it('resolves as soon as a daemon appears', async () => {
    asDesktop()
    const useBundledDaemonPending = await loadHook()
    const { result, rerender } = renderHook(
      ({ found }) => useBundledDaemonPending(found),
      { initialProps: { found: false } },
    )
    expect(result.current).toBe(true)

    // What the daemon-connected IPC event ultimately produces: a refetch that
    // puts a daemon in the list. This is the mechanism that ends the wait —
    // not the timeout.
    rerender({ found: true })
    await waitFor(() => expect(result.current).toBe(false))
  })

  it('does not wait when a daemon is already registered', async () => {
    asDesktop()
    const useBundledDaemonPending = await loadHook()
    const { result } = renderHook(() => useBundledDaemonPending(true))
    expect(result.current).toBe(false)
  })

  it('does not restart the budget when the renderer remounts', async () => {
    // THE REGRESSION THAT SHIPPED TWICE. The renderer reloads after the
    // post-sign-in daemon restart, unmounting and remounting this hook. A
    // per-component anchor (useRef(Date.now())) restarts the wait there, so
    // the user serves the budget twice. The anchor must be session-scoped.
    asDesktop()
    vi.useFakeTimers()
    const useBundledDaemonPending = await loadHook()

    const first = renderHook(() => useBundledDaemonPending(false))
    expect(first.result.current).toBe(true)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(29_000)
    })
    first.unmount()

    // Remount, as the renderer reload does. Only ~1s of budget should remain.
    const second = renderHook(() => useBundledDaemonPending(false))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000)
    })

    expect(second.result.current).toBe(false)
  })

  it('does not start out already expired in a long-lived renderer', async () => {
    // THE FLASH. The anchor is module-scoped and was never reset, so once a
    // renderer had been alive longer than the budget, every subsequent wait
    // began pre-expired: the hook returned false on its FIRST render,
    // ComputeStep skipped "Connecting your daemon…", and the user saw the
    // compute CHOICE flash up before the daemon registered and the auto-skip
    // advanced past it — the question-that-answers-itself this hook exists to
    // suppress.
    //
    // This is the normal case, not an edge one: every dev session with HMR
    // keeps one renderer for many minutes, and a packaged app left open before
    // sign-in does the same.
    asDesktop()
    vi.useFakeTimers()
    const useBundledDaemonPending = await loadHook()

    // A renderer that has been up well past the budget with nothing waiting.
    const idle = renderHook(() => useBundledDaemonPending(true))
    expect(idle.result.current).toBe(false)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(120_000)
    })
    idle.unmount()

    // Now sign-in begins and a real wait starts. It must actually wait.
    const { result } = renderHook(() => useBundledDaemonPending(false))
    expect(result.current).toBe(true)
  })

  it('gives up after the budget so a dead daemon cannot pin onboarding', async () => {
    asDesktop()
    vi.useFakeTimers()
    const useBundledDaemonPending = await loadHook()
    const { result } = renderHook(() => useBundledDaemonPending(false))
    expect(result.current).toBe(true)

    // Drive the backstop timer rather than waiting 30s of wall clock, then
    // let React flush the resulting state update.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000)
    })
    expect(result.current).toBe(false)
  })
})
