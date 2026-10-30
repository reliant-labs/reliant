import { render, screen, fireEvent } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import {
  ComputeOverageControl,
  choiceFromState,
  suggestedLimitCents,
} from '@/components/Settings/cloud/ComputeOverageControl'

/**
 * The overage spend cap. The server has enforced `budget_cents` since the
 * column landed, but nothing could set it — these cover the control that now
 * can, and the copy that has to tell the truth about what the cap does.
 */

const defaults = {
  enabled: false,
  pricing: { pricingVersion: 'test-version', sizes: [{ size: 'small', hourlyPriceCents: '13', exactHourlyPriceCents: '13/1', costWeight: '1/1', storageGib: 120 }], storageCentsPerGibMonth: '25', placeholder: false },
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

describe('suggestedLimitCents', () => {
  it('suggests half the plan price so the field is not blank homework', () => {
    expect(suggestedLimitCents(4000)).toBe(2000)
  })

  it('falls back to a concrete suggestion when the plan price is unknown', () => {
    expect(suggestedLimitCents(null)).toBeGreaterThan(0)
  })
})

describe('ComputeOverageControl', () => {
  it('says the limit pauses running machines, without promising a hard ceiling', () => {
    // THE point of this control's copy, and it had to change when the cap
    // became real. It previously asserted the opposite — "already running
    // keeps running until it goes idle" — because the billing sweep decided
    // on the free-tier global spend cap, which could never fire, so the limit
    // gated CreateDaemon/ResumeDaemon and nothing else.
    //
    // The sweep now enforces this cap directly (svcdaemon ComputeFundingChecker)
    // and suspends a running machine within about a minute of it being met. So
    // the copy must say the limit pauses running machines, AND must still not
    // promise an instantaneous ceiling: a user who reads it as absolute and is
    // then billed past it is the failure this sentence exists to prevent.
    renderControl({ enabled: true, budgetCents: 2000n })

    expect(
      screen.getByText(/pauses running ones shortly after the limit is reached/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/final bill can pass the limit slightly/i),
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

  it('shows per-size server hourly rates, never a flat minute estimate', () => {
    renderControl({ enabled: true, budgetCents: 2000n })
    expect(screen.getAllByText(/small: \$0.13\/hr/)).toHaveLength(2)
    expect(screen.queryByText(/extra minutes/)).not.toBeInTheDocument()
    expect(screen.getByText(/disk beyond it is billed running or suspended/)).toBeInTheDocument()
  })

  it('shows storage as its own always-on line, outside the cap', () => {
    renderControl({ enabled: true, budgetCents: 2000n, storage: { includedGib: 60, extraGib: 20 } })
    expect(screen.getByTestId('overage-storage-line')).toHaveTextContent(
      'Storage: $5.00/mo for 20 GB beyond your included 60 GB — billed separately, not limited by this cap.',
    )
  })

  it('says what storage is included when nothing is extra', () => {
    renderControl({ enabled: false, storage: { includedGib: 80, extraGib: 0 } })
    expect(screen.getByTestId('overage-storage-line')).toHaveTextContent(
      'Storage: 80 GB included, then $0.25/GB-month — billed separately, not limited by this cap.',
    )
  })

  it('shows overage spend against the cap so the ceiling and the distance to it are one glance', () => {
    renderControl({
      enabled: true,
      budgetCents: 2000n,
      overageSpentCents: 1500,
    })

    expect(screen.getByText('$15.00 of $20.00')).toBeInTheDocument()
  })

  it('says prices are unavailable rather than inventing a rate', () => {
    renderControl({ pricing: undefined })
    expect(screen.getAllByText(/prices unavailable/)).toHaveLength(2)
  })
})
