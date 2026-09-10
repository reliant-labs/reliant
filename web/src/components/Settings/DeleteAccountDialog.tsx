import { useEffect, useState } from 'react'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { Modal } from '../ui/Modal'
import { Button } from '../ui/Button'
import { Input } from '../ui/Input'
import { createAccountClient } from '../../api/grpc-client'
import type { PreviewAccountDeletionResponse } from '../../gen/reliant/v1/account_pb'
import { logger } from '../../lib/logger'

interface DeleteAccountDialogProps {
  isOpen: boolean
  onClose: () => void
  /** Called after the account has been deleted, to sign the user out. */
  onDeleted: () => void
}

/**
 * Confirmation dialog for account deletion.
 *
 * Two deliberate design choices, both about making an irreversible action
 * feel irreversible:
 *
 * 1. It shows REAL counts fetched from the server, not a generic warning. "3
 *    projects, 47 chats, 1,204 messages" is information a user can weigh;
 *    "this cannot be undone" is not.
 * 2. It states plainly what survives deletion — chiefly billing records, kept
 *    as financial documents with the personal details scrubbed. The server
 *    sends that list rather than this component owning the copy, which is why
 *    the scope has widened twice (control-plane account, then the sign-in
 *    identity) without this file changing.
 *
 * The typed-email confirmation is enforced by the server too; the field here
 * is the speed bump, not the security boundary.
 */
export function DeleteAccountDialog({ isOpen, onClose, onDeleted }: DeleteAccountDialogProps) {
  const [preview, setPreview] = useState<PreviewAccountDeletionResponse | null>(null)
  const [isLoadingPreview, setIsLoadingPreview] = useState(false)
  const [typedConfirmation, setTypedConfirmation] = useState('')
  const [isDeleting, setIsDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!isOpen) {
      // Reset on close so reopening never shows a stale count or a
      // half-typed confirmation from a previous attempt.
      setPreview(null)
      setTypedConfirmation('')
      setError(null)
      return
    }

    let cancelled = false
    setIsLoadingPreview(true)
    createAccountClient()
      .previewAccountDeletion({})
      .then((resp) => {
        if (!cancelled) setPreview(resp)
      })
      .catch((err) => {
        logger.error('[DeleteAccount] preview failed', err)
        if (!cancelled) setError('Could not load your account details. Please try again.')
      })
      .finally(() => {
        if (!cancelled) setIsLoadingPreview(false)
      })

    return () => {
      cancelled = true
    }
  }, [isOpen])

  // An anonymous session has no email to type, so there is nothing to
  // confirm — the dialog itself is the confirmation.
  const requiredEmail = preview?.confirmEmail ?? ''
  const needsTypedConfirmation = requiredEmail !== ''
  const confirmationMatches =
    !needsTypedConfirmation ||
    typedConfirmation.trim().toLowerCase() === requiredEmail.trim().toLowerCase()

  const handleDelete = async () => {
    setIsDeleting(true)
    setError(null)
    try {
      await createAccountClient().deleteAccount({ confirmEmail: typedConfirmation })
      onDeleted()
    } catch (err) {
      logger.error('[DeleteAccount] delete failed', err)
      setError(
        err instanceof Error
          ? err.message
          : 'Account deletion failed. Nothing was deleted — please try again.'
      )
      setIsDeleting(false)
    }
  }

  const counts: Array<{ label: string; value: bigint | undefined }> = [
    { label: 'Projects', value: preview?.projectCount },
    { label: 'Chats', value: preview?.chatCount },
    { label: 'Worktrees', value: preview?.worktreeCount },
    { label: 'Messages', value: preview?.messageCount },
  ]

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Delete account" size="md">
      <div className="space-y-5 p-1">
        <div className="flex items-start gap-3">
          <div className="p-2 bg-destructive/10 rounded-lg shrink-0">
            <AlertTriangle className="w-5 h-5 text-destructive" />
          </div>
          <p className="text-sm text-muted-foreground">
            This permanently deletes everything Reliant stores for your account.
            It cannot be undone.
          </p>
        </div>

        {isLoadingPreview && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            Loading your account details...
          </div>
        )}

        {preview && (
          <>
            <div>
              <h4 className="text-sm font-medium mb-2">This will be deleted</h4>
              <ul className="space-y-1">
                {counts.map(({ label, value }) => (
                  <li key={label} className="flex justify-between text-sm">
                    <span className="text-muted-foreground">{label}</span>
                    <span className="font-medium tabular-nums">
                      {(value ?? 0n).toString()}
                    </span>
                  </li>
                ))}
              </ul>
              {preview.hasProviderCredentials && (
                <p className="text-xs text-muted-foreground mt-2">
                  Your connected provider accounts (Claude, Codex, Copilot) and
                  API keys will be disconnected. You can re-link them on a new
                  account.
                </p>
              )}
            </div>

            {preview.retainedElsewhere.length > 0 && (
              <div className="rounded-lg border border-border/40 bg-muted/30 p-3">
                <h4 className="text-sm font-medium mb-2">This will NOT be deleted</h4>
                <ul className="space-y-1.5">
                  {preview.retainedElsewhere.map((line) => (
                    <li key={line} className="text-xs text-muted-foreground">
                      {line}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {needsTypedConfirmation && (
              <div>
                <label
                  htmlFor="delete-account-confirm"
                  className="block text-sm font-medium mb-1.5"
                >
                  Type <span className="font-mono">{requiredEmail}</span> to confirm
                </label>
                <Input
                  id="delete-account-confirm"
                  value={typedConfirmation}
                  onChange={(e) => setTypedConfirmation(e.target.value)}
                  placeholder={requiredEmail}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={isDeleting}
                />
              </div>
            )}
          </>
        )}

        {error && (
          <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
            <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
          </div>
        )}

        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" onClick={onClose} disabled={isDeleting}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={handleDelete}
            disabled={!preview || !confirmationMatches || isDeleting}
            leftIcon={isDeleting ? <Loader2 className="w-3 h-3 animate-spin" /> : undefined}
          >
            {isDeleting ? 'Deleting...' : 'Delete my account'}
          </Button>
        </div>
      </div>
    </Modal>
  )
}
