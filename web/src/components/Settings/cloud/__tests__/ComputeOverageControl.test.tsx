import { render, screen, fireEvent } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import {
  ComputeOverageControl,
  choiceFromState,
  limitAsMinutes,
  suggestedLimitCents,
} from '@/components/Settings/cloud/ComputeOverageControl'

/**
 * The overage spend cap. The server has enforced `budget_cents` since the
 * column landed, but nothing could set it — these cover the control that now
 * can, and the copy that has to tell the truth about what the cap does.
 */

const defaults = {
  enabled: false,
  overageCentsPerMinute: 40, // $0.40/min
  overageSpentCents: 0,
  monthlyPriceCents: 4000, // $40/mo
  onSave: vi.fn(),
}

function renderControl(props: Partial<Parameters<typeof ComputeOverageControl>[0]> = {}) {
  const onSave = vi.fn()
  render(<ComputeOverageControl {...defaults} onSave={onSave} {...props} />)
  return { onSave }
}

describe('choiceFromState', () => {
  it('treats a stored cap of 0 as UNCAPPED, not as a zero ceiling', () => {
    // The server gates on `budget_cents > 0`, so 0 is uncapped there. A UI that
    // showed "capped at $0" would claim a limit the backend does not enforce.
    expect(choiceFromState(true, 0n)).toBe('uncapped')
  })

  it('treats an absent cap as uncapped', () => {
    expect(choiceFromState(true, undefined)).toBe('uncapped')
  })

  it('reads a real cap as capped', () => {
    expect(choiceFromState(true, 2000n)).toBe('capped')
  })

  it('reads overage-off as off regardless of any stored cap', () => {
    expect(choiceFromState(false, 2000n)).toBe('off')
  })
})

describe('limitAsMinutes', () => {
  it('converts a dollar limit to extra minutes at the plan rate', () => {
    // $20 at $0.40/min is 50 minutes. Users reason in machine time and are
    // billed in dollars; the control shows both.
    expect(limitAsMinutes(2000, 40)).toBe(50)
  })

  it('returns null when the plan has no overage rate, rather than dividing by zero', () => {
    expect(limitAsMinutes(2000, 0)).toBeNull()
  })
})

describe('suggestedLimitCents', () => {
  it('suggests half the plan price so the field is not blank homework', () => {
    expect(suggestedLimitCents(4000)).toBe(2000)
  })

  it('falls back to a concrete suggestion when the plan price is unknown', () => {
    expect(suggestedLimitCents(null)).toBeGreaterThan(0)
  })
})

describe('ComputeOverageControl', () => {
  it('states plainly that the limit does not stop a running machine', () => {
    // THE point of this control's copy. The cap gates CreateDaemon and
    // ResumeDaemon and nothing else, so a user who reads it as a hard ceiling
    // and leaves a machine up over a weekend is billed for the weekend.
    renderControl({ enabled: true, budgetCents: 2000n })

    expect(
      screen.getByText(/already running keeps running until it goes idle/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/final bill can pass the limit/i),
    ).toBeInTheDocument()
  })

  it('does not save on mount, or on any change, without an explicit click', () => {
    // This authorizes spend. Nothing here may fire from an effect.
    const { onSave } = renderControl({ enabled: true, budgetCents: 2000n })

    fireEvent.click(screen.getByRole('radio', { name: /no limit/i }))
    fireEvent.change(screen.getByLabelText('Limit'), { target: { value: '35' } })

    expect(onSave).not.toHaveBeenCalled()
  })

  it('sends the cap in cents when a limit is saved', () => {
    const { onSave } = renderControl({ enabled: false })

    fireEvent.click(screen.getByRole('radio', { name: /up to a monthly limit/i }))
    fireEvent.change(screen.getByLabelText('Limit'), { target: { value: '20' } })
    fireEvent.click(screen.getByRole('button', { name: /save extra-time/i }))

    expect(onSave).toHaveBeenCalledWith({ enabled: true, budgetCents: 2000n })
  })

  it('sends no cap for the explicit no-limit choice, which is what uncapped means', () => {
    const { onSave } = renderControl({ enabled: true, budgetCents: 2000n })

    fireEvent.click(screen.getByRole('radio', { name: /no limit/i }))
    fireEvent.click(screen.getByRole('button', { name: /save extra-time/i }))

    expect(onSave).toHaveBeenCalledWith({ enabled: true })
  })

  it('turns overage off without carrying a stale cap along', () => {
    const { onSave } = renderControl({ enabled: true, budgetCents: 2000n })

    fireEvent.click(screen.getByRole('radio', { name: /stop at my included hours/i }))
    fireEvent.click(screen.getByRole('button', { name: /save extra-time/i }))

    expect(onSave).toHaveBeenCalledWith({ enabled: false })
  })

  it('refuses to submit a capped choice of $0, which the server would read as uncapped', () => {
    const { onSave } = renderControl({ enabled: false })

    fireEvent.click(screen.getByRole('radio', { name: /up to a monthly limit/i }))
    fireEvent.change(screen.getByLabelText('Limit'), { target: { value: '0' } })

    expect(screen.getByRole('button', { name: /save extra-time/i })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: /save extra-time/i }))
    expect(onSave).not.toHaveBeenCalled()
  })

  it('shows the dollar limit as extra minutes at the plan rate', () => {
    renderControl({ enabled: true, budgetCents: 2000n })

    expect(screen.getByText(/≈ 50 extra minutes/i)).toBeInTheDocument()
  })

  it('shows overage spend against the cap so the ceiling and the distance to it are one glance', () => {
    renderControl({
      enabled: true,
      budgetCents: 2000n,
      overageSpentCents: 1500,
    })

    expect(screen.getByText('$15.00 of $20.00')).toBeInTheDocument()
  })

  it('offers no control at all on a plan with no overage rate', () => {
    renderControl({ overageCentsPerMinute: 0 })

    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.getByText(/doesn't offer extra time/i)).toBeInTheDocument()
  })
})
