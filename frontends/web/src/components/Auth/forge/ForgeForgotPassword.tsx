// "Forgot password" with forge sign-in: the broker mails a link to
// /auth/reset?userID=…&code=… (POST /auth/forgot). The answer is the same
// sentence whether or not the address has an account, and so is this screen.
//
// Renders the inside of a card, like the Supabase ForgotPassword it stands in
// for: the caller draws the AuthLayout.

import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { AuthError as NativeAuthError, forgotPassword } from '@/lib/auth/native-login'
import { authPath } from '@/lib/paths'
import { AuthError, AuthLayout } from '../../AuthLayout'
import { CheckSpamNote } from '../../CheckSpamNote'
import { ForgeAuthPanel, ForgeField, ForgeNotice, ForgePrimaryButton, ForgeTextButton, forgeInputClass } from './ForgeAuthParts'

export function ForgeForgotPassword({ onBackToSignIn }: { onBackToSignIn: () => void }) {
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      setSent(await forgotPassword(email.trim()))
    } catch (err) {
      setError(err instanceof NativeAuthError ? err.message : 'We couldn’t send the request. Please try again.')
    } finally {
      setBusy(false)
    }
  }

  const back = (
    <div className="text-center">
      <ForgeTextButton onClick={onBackToSignIn}>Back to sign in</ForgeTextButton>
    </div>
  )

  if (sent !== null) {
    return (
      <ForgeAuthPanel title="Check your email">
        <ForgeNotice tone="success">
          <p>{sent}</p>
          <p className="text-muted-foreground">Open the link in the email to choose a new password.</p>
        </ForgeNotice>
        <CheckSpamNote />
        {back}
      </ForgeAuthPanel>
    )
  }

  return (
    <ForgeAuthPanel
      title="Reset your password"
      description="Enter the email address of your account and we’ll send you a link to choose a new password."
    >
      <form className="space-y-5" onSubmit={onSubmit} aria-busy={busy}>
        {error && <AuthError message={error} />}
        <ForgeField id="forgot-email" label="Email address">
          <input
            id="forgot-email"
            name="email"
            type="email"
            autoComplete="username email"
            autoFocus
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={busy}
            className={forgeInputClass}
            placeholder="you@example.com"
          />
        </ForgeField>
        <ForgePrimaryButton busy={busy}>{busy ? 'Sending…' : 'Send reset link'}</ForgePrimaryButton>
        {back}
      </form>
    </ForgeAuthPanel>
  )
}

/** The /auth/forgot page (linked from the verify and reset pages). */
export function ForgeForgotPasswordPage() {
  const navigate = useNavigate()
  return (
    <AuthLayout>
      <ForgeForgotPassword onBackToSignIn={() => navigate({ to: authPath(), search: { redirect: undefined } })} />
    </AuthLayout>
  )
}
