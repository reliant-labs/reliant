import { useNavigate } from '@tanstack/react-router'
import { LogOut } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { AuthLayout, AuthHeader } from './AuthLayout'
import { EmailCodeForm } from './EmailCodeForm'

/**
 * The `/verify-email` stop: an account that has an address on file which was
 * never confirmed.
 *
 * Nothing in the app creates that state any more — sign-in mails a code and
 * the code IS the confirmation, so a user is confirmed the moment they are
 * signed in. What remains is accounts made before that change, plus anyone
 * AuthGuard bounces here for `!email_confirmed_at`, and they still need a way
 * through.
 *
 * `resendSignupVerification` (GoTrue `resend`, `type: 'signup'`) and
 * `verifyEmailOTP` (`type: 'signup'`) are the right pair for exactly this
 * population and no other: the account has a populated-but-unconfirmed
 * `users.email`, so the lookup finds it and the token lives in
 * `confirmation_token`. An anonymous user mid-upgrade has an EMPTY
 * `users.email` — their address sits in `users.email_change` — so this screen
 * would silently mail them nothing. That flow uses UpgradeAccount instead.
 *
 * No code is sent on mount. Landing here does not mean one is missing, and an
 * automatic send burned the rate limit for users who already had a valid code
 * sitting in their inbox.
 */
export function EmailVerification({ email }: { email?: string }) {
  const navigate = useNavigate()
  const { user, resendSignupVerification, verifyEmailOTP, signOut } = useAuthStore()

  const emailToVerify = email ?? user?.email

  const handleSignOut = async () => {
    try {
      await signOut()
    } finally {
      navigate({ to: '/auth', search: { redirect: undefined } })
    }
  }

  const signOutFooter = (
    <div className="text-center pt-4 border-t border-border">
      <button
        type="button"
        onClick={() => void handleSignOut()}
        className="flex items-center justify-center gap-2 mx-auto text-sm font-medium text-muted-foreground transition-colors hover:text-destructive"
      >
        <LogOut className="w-4 h-4" />
        Sign out and use a different email
      </button>
    </div>
  )

  if (!emailToVerify) {
    return (
      <AuthLayout>
        <div className="p-8 space-y-6">
          <AuthHeader
            title="Confirm your email"
            description="We don’t have an email address on this account. Sign in again to continue."
          />
          {signOutFooter}
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <div className="p-8 space-y-6">
        <AuthHeader title="Confirm your email" />

        <EmailCodeForm
          email={emailToVerify}
          idPrefix="verify-email"
          submitLabel="Confirm email"
          // Nothing was sent on mount, so the resend button is live
          // immediately — the user may well have arrived with no code at all.
          initialCooldown={0}
          description={
            <>
              Your account still needs <strong>{emailToVerify}</strong>{' '}
              confirmed. Enter the 6-digit code from the email we sent you, or
              send yourself a new one.
            </>
          }
          onVerify={async (code) => {
            await verifyEmailOTP(code, emailToVerify)
            navigate({ to: '/', search: {} })
          }}
          onResend={() => resendSignupVerification(emailToVerify)}
          footer={signOutFooter}
        />
      </div>
    </AuthLayout>
  )
}
