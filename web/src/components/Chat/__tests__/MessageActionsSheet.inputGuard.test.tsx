/**
 * Regression test for a sheet that ran actions the user never chose.
 *
 * The sheet opens on long-press, while the finger is still down and directly
 * underneath it. Releasing dispatched pointerup/click onto whichever action
 * button happened to be at that coordinate — a real browser audit observed
 * "Copy message" running itself, and "Branch in place" creating three actual
 * chats in the database.
 *
 * Guarding in `useLongPress` could not work: once the portal renders over the
 * message, the pressed element stops receiving the gesture and no `touchend`
 * arrives there, so its `preventDefault()` never ran. The sheet itself has to
 * refuse input until the opening touch has lifted.
 */

import { describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { MessageActionsSheet } from '../MessageActionsSheet'

function renderSheet(onSelect = vi.fn()) {
  const actions = [
    { key: 'copy', label: 'Copy message', onSelect },
    { key: 'branch', label: 'Branch in place', onSelect },
  ]
  render(
    <MessageActionsSheet
      isOpen
      onClose={vi.fn()}
      actions={actions}
      timestampLabel="12:34"
    />,
  )
  return onSelect
}

/** The portal root the sheet renders into. */
function sheetRoot(): HTMLElement {
  const dialog = screen.getByRole('dialog')
  // The pointer-events guard lives on the fixed overlay wrapping the panel.
  const root = dialog.parentElement
  if (!root) throw new Error('sheet root not found')
  return root
}

describe('MessageActionsSheet input guard', () => {
  it('ignores pointer events until the opening touch lifts', () => {
    renderSheet()
    // While the finger that opened the sheet is still down, nothing in the
    // sheet — including the backdrop — may react.
    expect(sheetRoot().className).toContain('pointer-events-none')
  })

  it('accepts input once the release is observed', () => {
    renderSheet()
    act(() => {
      document.dispatchEvent(new Event('pointerup'))
    })
    expect(sheetRoot().className).not.toContain('pointer-events-none')
  })

  it('arms on a fallback timer when no release is delivered', () => {
    // A finger dragged off-screen never delivers pointerup. Without the
    // fallback the sheet would stay permanently inert.
    vi.useFakeTimers()
    try {
      renderSheet()
      expect(sheetRoot().className).toContain('pointer-events-none')
      act(() => {
        vi.advanceTimersByTime(400)
      })
      expect(sheetRoot().className).not.toContain('pointer-events-none')
    } finally {
      vi.useRealTimers()
    }
  })

  it('still renders its actions while guarded', () => {
    // The guard suppresses input, not rendering — the user must be able to
    // read the options before their finger comes up.
    renderSheet()
    expect(screen.getByText('Copy message')).toBeInTheDocument()
    expect(screen.getByText('Branch in place')).toBeInTheDocument()
  })
})
