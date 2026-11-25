// /auth/reset?userID=…&code=…[&orgID=…] with forge sign-in: the page the
// broker's reset email opens (internal/app/login_broker.go handleForgot).
// POST /auth/reset sets the password and, in the common case, signs the
// person in with it; the identity goes through the store like any sign-in.
//
// Errors here are SPECIFIC, unlike sign-in's: the person has to read a
// refusal to fix it, and a refused password names the rules it breaks.

import { useState } from 'react'
import { Check, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { AuthError as NativeAuthError, resetPassword, unmetRules, type PasswordRule } from '@/lib/auth/native-login'
import { authPath, forgeForgotPasswordPath } from '@/lib/paths'
import { AuthError, AuthLayout } from '../../AuthLayout'
import { PasswordInput } from '../../PasswordInput'
import { ForgeAuthPanel, ForgeLink, ForgePasswordRequirements, ForgePrimaryButton } from './ForgeAuthParts'
import { installAndLand, queryParam, rememberedReturnTo, usePasswordPolicy } from './forgeAuthLib'
import { landAt } from './forgeLanding'

export interface ForgeResetPasswordFormProps {
  userID: string | null
  code: string | null
  /** Shown above the form: why a password is being chosen (the verify page uses it). */
  intro?: React.ReactNode
}

/** The reset form, as the inside of a card. */
export function ForgeResetPasswordForm({ userID, code, intro }: ForgeResetPasswordFormProps) {
  const policy = usePasswordPolicy()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [serverUnmet, setServerUnmet] = useState<PasswordRule[]>([])
  const [attempted, setAttempted] = useState(false)
  const [busy, setBusy] = useState(false)

  if (!userID || !code) {
    return (
      <ForgeAuthPanel
        title="This reset link is incomplete"
        description="Open the link exactly as it was sent to you, or request a new one."
      >
        <div className="flex justify-between">
          <ForgeLink href={forgeForgotPasswordPath()}>Request a new link</ForgeLink>
          <ForgeLink href={authPath()}>Back to sign in</ForgeLink>
        </div>
      </ForgeAuthPanel>
    )
  }

  const mismatch = confirm.length > 0 && confirm !== password
  const matches = confirm.length > 0 && confirm === password
  const passwordUnmet = policy !== null && unmetRules(policy, password).length > 0

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (passwordUnmet) {
      setAttempted(true)
      setError('Your new password doesn’t meet every requirement yet.')
      return
    }
    if (password !== confirm) {
      setError('The two passwords don’t match.')
      return
    }
    setBusy(true)
    setError(null)
    setServerUnmet([])
    try {
      const identity = await resetPassword({ userID, code, password })
      // Set but not signed in (the follow-up sign-in failed): sign in as usual.
      if (!(await installAndLand(identity, rememberedReturnTo(true)))) landAt(authPath())
    } catch (err) {
      if (err instanceof NativeAuthError) {
        setError(err.message)
        setServerUnmet(err.unmet)
        if (err.unmet.length > 0) setAttempted(true)
      } else {
        setError('We couldn’t reset your password. Please try again.')
      }
      setBusy(false)
    }
  }

  return (
    <ForgeAuthPanel title="Choose a new password">
      {intro}
      <form className="space-y-5" onSubmit={onSubmit} aria-busy={busy}>
        {error && <AuthError message={error} />}
        <div>
          <PasswordInput
            id="new-password"
            name="password"
            label="New password"
            value={password}
            onChange={(next) => {
              setPassword(next)
              // A changed password is a new one: the last verdict no longer applies.
              if (serverUnmet.length > 0 || attempted) {
                setServerUnmet([])
                setError(null)
              }
            }}
            autoComplete="new-password"
            required
            disabled={busy}
            minLength={policy?.min_length ?? 0}
            showStrengthIndicator={false}
            showRequirements={false}
          />
          <ForgePasswordRequirements
            id="new-password-requirements"
            policy={policy}
            password={password}
            serverUnmet={serverUnmet}
            attempted={attempted}
          />
        </div>
        <div>
          <PasswordInput
            id="confirm-password"
            name="confirm"
            label="Confirm new password"
            value={confirm}
            onChange={setConfirm}
            autoComplete="new-password"
            required
            disabled={busy}
            minLength={0}
            showStrengthIndicator={false}
            showRequirements={false}
          />
          <p
            className={cn(
              'mt-2 flex min-h-5 items-center gap-1.5 text-xs',
              mismatch ? 'text-destructive-ink' : 'text-success-ink',
            )}
          >
            {mismatch ? (
              <>
                <X aria-hidden="true" className="h-3 w-3 flex-shrink-0" />
                The two passwords don’t match.
              </>
            ) : matches ? (
              <>
                <Check aria-hidden="true" className="h-3 w-3 flex-shrink-0" />
                The passwords match.
              </>
            ) : null}
          </p>
        </div>
        <ForgePrimaryButton busy={busy}>{busy ? 'Setting password…' : 'Set password'}</ForgePrimaryButton>
        <div className="text-center">
          <ForgeLink href={authPath()}>Back to sign in</ForgeLink>
        </div>
      </form>
    </ForgeAuthPanel>
  )
}

/** The /auth/reset page: the link's userID and code from the query string. */
export function ForgeResetPasswordPage() {
  return (
    <AuthLayout>
      <ForgeResetPasswordForm userID={queryParam('userID')} code={queryParam('code')} />
    </AuthLayout>
  )
}
