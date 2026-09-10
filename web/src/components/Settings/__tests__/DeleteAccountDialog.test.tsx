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
