import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'
// The same-origin guard, shared: only relative paths are honored, so a crafted
// `returnTo` cannot bounce the user to an attacker-controlled origin.
import { isSafeReturnTo } from '@/lib/returnTo'
import { describeAuthError } from '@/lib/authErrors'
import { LinkIdentityForm } from './LinkIdentityForm'
import { AuthLayout, AuthHeader, AuthError } from './AuthLayout'
import { BillingService } from '@/gen/controlplane/v1/public/billing_service_pb'
import { getControlPlaneClient } from '../services/controlPlane/client'
import { CheckSpamNote } from './CheckSpamNote'

/**
 * Account-upgrade flow. Entry point for an anonymous Supabase user who needs a
 * real identity *with an email* attached to their existing account (e.g. the
 * admin billing page blocks paid actions until a billing email exists).
 *
 * This is NOT a sign-in screen, and the distinction is the whole point. The
 * user already has an account with chats and workspaces in it; they are
 * attaching an identity to THAT account, not creating a second one:
 *   - GitHub / Google / Apple → supabase.auth.linkIdentity (round-trips through
 *     the provider and back to /auth/callback, which honors `returnTo`).
 *   - Email + password → supabase.auth.updateUser, which upgrades the
 *     anonymous user in place. With confirmations enabled the address stays
 *     pending until the user types the emailed code or clicks the emailed
 *     link; LinkIdentityForm owns that step inline.
 *
 * The email step is NOT delegated to the standalone `EmailVerification`
 * screen. That screen verifies with `type: 'signup'`, which reads GoTrue's
 * confirmation_token — but an anonymous user upgraded via updateUser has a
 * pending EMAIL CHANGE, whose token lives in email_change_token_new. Verifying
 * with the wrong type reports "Token has expired or is invalid" for a code
 * that is perfectly valid, so this route uses the form's own step, which
 * verifies with `type: 'email_change'`.
 *
 * A plain bounce to /auth does NOT work here: the anon user already has a
 * Supabase session, so /auth would just redirect them away — and signing in
 * there would strand the work sitting in the anonymous session.
 *
 * `?returnTo=<path>` is where we send the user once they have an email. It is
 * validated to a same-origin relative path before any redirect (same predicate
 * the OAuth callback uses) to avoid open-redirect abuse, then followed as a
 * client-side navigation.
 */

const goToReturnTo = (
  returnTo: string | undefined,
  navigate: ReturnType<typeof useNavigate>,
) => {
  if (isSafeReturnTo(returnTo)) {
    // Client-side navigation. Every returnTo we send here is a route in THIS
    // app (/settings/billing, or wherever the user was standing) — the old
    // window.location.assign tore down the SPA and paid a full cold boot,
    // which is what made returning from /upgrade look like a page refresh
    // that did nothing. `href` takes an already-built path, so a returnTo
    // carrying a query string round-trips unchanged.
    void navigate({ href: returnTo })
    return
  }
  void navigate({ to: '/', search: {} })
}

export function UpgradeAccount() {
  const navigate = useNavigate()
  const { returnTo } = useSearch({ from: '/upgrade' })
  const {
    user,
    initialized,
    loading: authLoading,
    initialize,
    sendEmailIdentityVerification,
    verifyEmailIdentityOTP,
  } = useAuthStore()

  // The link form owns its own email/password/verification/provider state.
  // What stays here is this route's one extra job: the billing-email fallback
  // for a provider that yielded no usable address.
  // No-email-after-upgrade fallback (e.g. GitHub with a private email): the
  // user enters a billing email, verifies it via OTP, and only then do we
  // persist it through the control-plane UpdateBillingEmail RPC.
  const [billingEmail, setBillingEmail] = useState('')
  const [billingCodeSent, setBillingCodeSent] = useState(false)
  const [billingCode, setBillingCode] = useState('')
  const [billingSubmitting, setBillingSubmitting] = useState(false)
  const [billingError, setBillingError] = useState<string | null>(null)

  useEffect(() => {
    if (!initialized) initialize()
  }, [initialized, initialize])

  // A user counts as "already upgraded" once they are non-anonymous AND carry a
  // usable email. The anonymous flag flips the moment an identity links, but the
  // email only lands after the provider/OTP round-trip completes.
  const alreadyUpgraded = useMemo(
    () => !!user && !user.is_anonymous && !!user.email,
    [user],
  )

  // Honor returnTo as soon as the account is fully upgraded (covers both the
  // "arrived already upgraded" case and the post-link callback hop landing
  // here). Wait for auth init so we don't redirect on a stale null user.
  useEffect(() => {
    if (!initialized || authLoading) return
    if (alreadyUpgraded) {
      goToReturnTo(returnTo, navigate)
    }
  }, [initialized, authLoading, alreadyUpgraded, returnTo, navigate])

  // Step 1 of the billing-email fallback: ship an OTP to the address the user
  // typed. We pass it explicitly because the account email is absent — that
  // absence is the whole reason this branch renders.
  //
  // This goes through the EMAIL-CHANGE primitives, not the signup ones. The
  // user here is non-anonymous but has no `users.email` (GitHub with a private
  // address), so attaching one is an email change: GoTrue writes the pending
  // address to `users.email_change` and the token to `email_change_token_new`.
  // `resend`/`verifyOtp` with `type: 'signup'` look up `users.email` and
  // `confirmation_token` respectively, so both missed — the send returned a
  // bare 200 having mailed nothing, and a correct code reported "Token has
  // expired or is invalid".
  const handleBillingSendCode = async (e: React.FormEvent) => {
    e.preventDefault()
    setBillingError(null)

    const trimmed = billingEmail.trim()
    if (!trimmed) {
      setBillingError('Please enter an email address.')
      return
    }

    setBillingSubmitting(true)
    try {
      await sendEmailIdentityVerification(trimmed)
      setBillingEmail(trimmed)
      setBillingCodeSent(true)
    } catch (err) {
      setBillingError(describeAuthError(err, 'Failed to send verification code.'))
    } finally {
      setBillingSubmitting(false)
    }
  }

  // Step 2: verify the OTP, and ONLY on success persist the address as the
  // billing email. A billing email must be trustworthy, so verification gates
  // the RPC. After both succeed, honor returnTo exactly like the happy path.
  const handleBillingVerify = async (e: React.FormEvent) => {
    e.preventDefault()
    setBillingError(null)

    if (billingCode.length !== 6) {
      setBillingError('Verification code must be 6 digits.')
      return
    }

    setBillingSubmitting(true)
    try {
      await verifyEmailIdentityOTP(billingCode, billingEmail)
    } catch (err) {
      setBillingError(describeAuthError(err, 'Failed to verify code. Please try again.'))
      setBillingSubmitting(false)
      return
    }

    // OTP verified → persist as the billing email. The backend validates the
    // format and rejects unusable addresses (e.g. github noreply), so surface
    // any rejection here rather than continuing.
    try {
      await getControlPlaneClient(BillingService).updateBillingEmail({
        email: billingEmail,
      })
    } catch (err) {
      setBillingError(
        err instanceof Error ? err.message : 'Failed to save billing email.',
      )
      setBillingSubmitting(false)
      return
    }

    goToReturnTo(returnTo, navigate)
  }

  // ---- Render states -------------------------------------------------------

  // While auth is still resolving, or we're about to redirect an
  // already-upgraded user, render nothing rather than flash the upgrade form.
  if (!initialized || authLoading || alreadyUpgraded) {
    return (
      <AuthLayout>
        <div className="p-8 flex justify-center">
          <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
        </div>
      </AuthLayout>
    )
  }

  // Edge case: the user is non-anonymous (upgraded) but STILL has no usable
  // email — e.g. they linked GitHub, whose Supabase provider may not expose a
  // verified email (control-plane's validateBillingEmail also rejects
  // *.users.noreply.github.com). Let them set a billing email directly: enter
  // an address, verify it via OTP, then persist it through the control-plane
  // UpdateBillingEmail RPC. Verification gates the RPC so the billing email is
  // trustworthy. On success we honor returnTo just like the happy path.
  if (user && !user.is_anonymous && !user.email) {
    return (
      <AuthLayout>
        <div className="p-8 space-y-6">
          <AuthHeader
            title="Add a billing email"
            description={
              billingCodeSent
                ? `Enter the 6-digit code we sent to ${billingEmail}.`
                : "We couldn't get an email from that provider. Add one for billing — we'll verify it with a code."
            }
          />

          {billingError && <AuthError message={billingError} />}

          {billingCodeSent && <CheckSpamNote />}

          {!billingCodeSent ? (
              <form className="space-y-5" onSubmit={handleBillingSendCode} autoComplete="on">
                <div>
                  <label htmlFor="billing-email" className="block text-sm font-medium mb-1.5">
                    Email address
                  </label>
                  <input
                    id="billing-email"
                    name="email"
                    type="email"
                    autoComplete="email"
                    autoFocus
                    required
                    value={billingEmail}
                    onChange={(e) => setBillingEmail(e.target.value)}
                    disabled={billingSubmitting}
                    className="block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
                    placeholder="you@example.com"
                  />
                </div>
                <button
                  type="submit"
                  disabled={billingSubmitting || !billingEmail.trim()}
                  className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  {billingSubmitting ? 'Sending...' : 'Send code'}
                </button>
              </form>
            ) : (
              <form className="space-y-5" onSubmit={handleBillingVerify}>
                <div>
                  <label htmlFor="billing-code" className="block text-sm font-medium mb-1.5">
                    Verification code
                  </label>
                  <input
                    id="billing-code"
                    name="code"
                    type="text"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    autoFocus
                    value={billingCode}
                    onChange={(e) => {
                      setBillingCode(e.target.value.replace(/\D/g, '').slice(0, 6))
                      if (billingError) setBillingError(null)
                    }}
                    disabled={billingSubmitting}
                    maxLength={6}
                    placeholder="000000"
                    className="block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent text-center text-2xl tracking-widest focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
                  />
                </div>
                <button
                  type="submit"
                  disabled={billingSubmitting || billingCode.length !== 6}
                  className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  {billingSubmitting ? 'Verifying...' : 'Verify and continue'}
                </button>
                <div className="text-center">
                  <button
                    type="button"
                    disabled={billingSubmitting}
                    onClick={() => {
                      setBillingError(null)
                      setBillingCode('')
                      setBillingCodeSent(false)
                    }}
                    className="text-sm text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50"
                  >
                    Use a different email
                  </button>
                </div>
              </form>
          )}
        </div>
      </AuthLayout>
    )
  }

  // Default: anonymous user → present every way to attach an identity that
  // yields an email. Providers and the email form are shown together (same
  // shape as the sign-in screen) rather than behind a toggle: there is no
  // "Skip for now" escape here, so hiding half the options only adds a click.
  //
  // The form itself is `LinkIdentityForm`, shared with the in-checkout modal so
  // the two surfaces cannot drift into offering different providers — or, worse,
  // into one of them growing a guest escape hatch.
  return (
    <AuthLayout>
      <div className="p-8 space-y-6">
        <AuthHeader
          title="Save your account"
          description="You're working in a temporary account. Attach an email or a provider to keep it — your chats and workspaces stay exactly as they are, and you'll be able to sign back in from anywhere."
        />

        <LinkIdentityForm
          returnTo={returnTo}
          onLinked={() => goToReturnTo(returnTo, navigate)}
        />

      </div>
    </AuthLayout>
  )
}
