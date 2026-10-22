/**
 * The poll gate. A backgrounded phone that keeps a 5s daemon poll running
 * burns battery and control-plane quota for a screen nobody is looking at, so
 * "hidden ⇒ no interval" is a behavioral requirement, not a nicety.
 *
 * `false` specifically (not 0, not undefined) is what TanStack Query reads as
 * "stop polling"; 0 and undefined both fall back to its default behavior, so
 * the exact value is asserted.
 */

import { describe, expect, it, afterEach, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { useVisibilityPolling } from '../useVisibilityPolling'

function setVisibility(state: DocumentVisibilityState) {
  // jsdom's visibilityState is a getter with no setter; override the property
  // and fire the event the real browser would.
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => state,
  })
  act(() => {
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

function Probe() {
  const interval = useVisibilityPolling(5000)
  return <div data-testid="interval">{String(interval)}</div>
}

afterEach(() => {
  setVisibility('visible')
  vi.restoreAllMocks()
})

describe('useVisibilityPolling', () => {
  it('polls at the requested interval while the tab is visible', () => {
    render(<Probe />)
    expect(screen.getByTestId('interval')).toHaveTextContent('5000')
  })

  it('stops polling when the tab is hidden', () => {
    render(<Probe />)
    setVisibility('hidden')
    expect(screen.getByTestId('interval')).toHaveTextContent('false')
  })

  it('resumes polling when the tab comes back', () => {
    render(<Probe />)
    setVisibility('hidden')
    expect(screen.getByTestId('interval')).toHaveTextContent('false')
    setVisibility('visible')
    expect(screen.getByTestId('interval')).toHaveTextContent('5000')
  })

  it('starts stopped when mounted into an already-hidden tab', () => {
    // A restored background tab fires no visibilitychange, so the mount-time
    // re-read is the only thing that catches this.
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => 'hidden',
    })
    render(<Probe />)
    expect(screen.getByTestId('interval')).toHaveTextContent('false')
  })

  it('removes its listener on unmount', () => {
    const remove = vi.spyOn(document, 'removeEventListener')
    const { unmount } = render(<Probe />)
    unmount()
    expect(remove).toHaveBeenCalledWith('visibilitychange', expect.any(Function))
  })
})
