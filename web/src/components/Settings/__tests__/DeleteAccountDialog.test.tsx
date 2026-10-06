/**
 * The account-deletion dialog's guardrails.
 *
 * These tests pin the two behaviours that make an irreversible action safe:
 * the confirm button stays disabled until the user has typed their own email,
 * and the dialog states what deletion does NOT cover. Both have been wrong in
 * shipped products, and neither is visible from the type signature.
 */
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  previewAccountDeletion: vi.fn(),
  deleteAccount: vi.fn(),
}))

vi.mock('@/api/grpc-client', () => ({
  createAccountClient: () => ({
    previewAccountDeletion: mocks.previewAccountDeletion,
    deleteAccount: mocks.deleteAccount,
  }),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { DeleteAccountDialog } from '../DeleteAccountDialog'

const previewWithEmail = {
  projectCount: 3n,
  chatCount: 47n,
  worktreeCount: 2n,
  messageCount: 1204n,
  hasProviderCredentials: true,
  confirmEmail: 'owner@example.com',
  retainedElsewhere: [
    'Your billing records, invoices and any wallet balance are kept by the Reliant control plane.',
  ],
}

function renderDialog(overrides: Partial<React.ComponentProps<typeof DeleteAccountDialog>> = {}) {
  const onClose = vi.fn()
  const onDeleted = vi.fn()
  render(
    <DeleteAccountDialog isOpen onClose={onClose} onDeleted={onDeleted} {...overrides} />
  )
  return { onClose, onDeleted }
}

describe('DeleteAccountDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.previewAccountDeletion.mockResolvedValue(previewWithEmail)
    mocks.deleteAccount.mockResolvedValue({ deletedRowCount: 1256n })
  })

  it('shows the real counts from the server, not a generic warning', async () => {
    renderDialog()
    expect(await screen.findByText('47')).toBeInTheDocument()
    expect(screen.getByText('1204')).toBeInTheDocument()
  })

  it('states what deletion does NOT cover', async () => {
    renderDialog()
    expect(await screen.findByText(/This will NOT be deleted/i)).toBeInTheDocument()
    expect(screen.getByText(/billing records/i)).toBeInTheDocument()
  })

  it('keeps the confirm button disabled until the email is typed exactly', async () => {
    const user = userEvent.setup()
    renderDialog()

    const confirmButton = await screen.findByRole('button', { name: /delete my account/i })
    expect(confirmButton).toBeDisabled()

    const input = screen.getByLabelText(/to confirm/i)
    await user.type(input, 'wrong@example.com')
    expect(confirmButton).toBeDisabled()

    await user.clear(input)
    await user.type(input, 'owner@example.com')
    await waitFor(() => expect(confirmButton).toBeEnabled())
  })

  it('does not call deleteAccount while the confirmation is wrong', async () => {
    const user = userEvent.setup()
    renderDialog()

    const input = await screen.findByLabelText(/to confirm/i)
    await user.type(input, 'wrong@example.com')
    await user.click(screen.getByRole('button', { name: /delete my account/i }))

    expect(mocks.deleteAccount).not.toHaveBeenCalled()
  })

  it('deletes and notifies the parent once the confirmation matches', async () => {
    const user = userEvent.setup()
    const { onDeleted } = renderDialog()

    const input = await screen.findByLabelText(/to confirm/i)
    await user.type(input, 'owner@example.com')
    await user.click(screen.getByRole('button', { name: /delete my account/i }))

    await waitFor(() => expect(mocks.deleteAccount).toHaveBeenCalledOnce())
    await waitFor(() => expect(onDeleted).toHaveBeenCalledOnce())
  })

  it('requires no typed confirmation for an anonymous session', async () => {
    // An anonymous user has no email claim, so there is nothing to type.
    // Requiring one would strand exactly the half-upgraded accounts this
    // feature exists to rescue.
    mocks.previewAccountDeletion.mockResolvedValue({
      ...previewWithEmail,
      confirmEmail: '',
    })
    renderDialog()

    const confirmButton = await screen.findByRole('button', { name: /delete my account/i })
    await waitFor(() => expect(confirmButton).toBeEnabled())
    expect(screen.queryByLabelText(/to confirm/i)).not.toBeInTheDocument()
  })

  it('states the refund and the forfeiture from the server quote', async () => {
    mocks.previewAccountDeletion.mockResolvedValue({
      ...previewWithEmail,
      wallet: {
        refundCents: 2500n,
        destinations: [{ cardBrand: 'visa', cardLast4: '1234', amountCents: 2500n }],
        unrefundableCents: 0n,
        forfeitedPromoCents: 700n,
      },
    })
    renderDialog()

    expect(
      await screen.findByText(
        "We'll refund $25.00 to your card ending ••••1234; $7.00 of promotional credit will be forfeited."
      )
    ).toBeInTheDocument()
  })

  it('splits a refund across cards and names paid credit support will refund', async () => {
    mocks.previewAccountDeletion.mockResolvedValue({
      ...previewWithEmail,
      wallet: {
        refundCents: 3000n,
        destinations: [
          { cardBrand: 'visa', cardLast4: '1234', amountCents: 2000n },
          { cardBrand: '', cardLast4: '', amountCents: 1000n },
        ],
        unrefundableCents: 500n,
        forfeitedPromoCents: 0n,
      },
    })
    renderDialog()

    expect(
      await screen.findByText(
        "We'll refund $30.00: $20.00 to your card ending ••••1234, $10.00 to your original payment method."
      )
    ).toBeInTheDocument()
    expect(screen.getByText(/\$5\.00 can't be refunded to your card automatically/)).toBeInTheDocument()
    expect(screen.queryByText(/forfeited/)).not.toBeInTheDocument()
  })

  it('shows no wallet section when there is nothing to settle', async () => {
    mocks.previewAccountDeletion.mockResolvedValue({
      ...previewWithEmail,
      wallet: { refundCents: 0n, destinations: [], unrefundableCents: 0n, forfeitedPromoCents: 0n },
    })
    renderDialog()
    await screen.findByText('47')
    expect(screen.queryByTestId('delete-account-wallet')).not.toBeInTheDocument()
  })

  it('tells the user when support still owes a refund, before signing out', async () => {
    const user = userEvent.setup()
    mocks.deleteAccount.mockResolvedValue({
      deletedRowCount: 10n,
      refundPending: true,
      refundOwedCents: 1500n,
    })
    const { onDeleted } = renderDialog()

    const input = await screen.findByLabelText(/to confirm/i)
    await user.type(input, 'owner@example.com')
    await user.click(screen.getByRole('button', { name: /delete my account/i }))

    expect(await screen.findByText(/couldn't refund \$15\.00 to your card automatically/)).toBeInTheDocument()
    expect(screen.getByText(/finalized once that's done/)).toBeInTheDocument()
    expect(onDeleted).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /done/i }))
    expect(onDeleted).toHaveBeenCalledOnce()
  })

  it('reports a failure without claiming anything was deleted', async () => {
    const user = userEvent.setup()
    mocks.deleteAccount.mockRejectedValue(new Error('account deletion failed; nothing was deleted'))
    const { onDeleted } = renderDialog()

    const input = await screen.findByLabelText(/to confirm/i)
    await user.type(input, 'owner@example.com')
    await user.click(screen.getByRole('button', { name: /delete my account/i }))

    expect(await screen.findByText(/nothing was deleted/i)).toBeInTheDocument()
    expect(onDeleted).not.toHaveBeenCalled()
  })
})
