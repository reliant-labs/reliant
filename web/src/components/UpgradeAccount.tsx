import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'
import { validatePassword } from '../utils/passwordValidation'
import { OAuthButton } from './OAuthButton'
import { PasswordInput, ConfirmPasswordInput } from './PasswordInput'
import { EmailVerification } from './EmailVerification'
import { GradientBackground } from './GradientBackground'
import { BrandMark } from './icons/BrandMark'
import { BillingService } from '@/gen/controlplane/v1/public/billing_service_pb'
import { getControlPlaneClient } from '../services/controlPlane/client'

/**
 * Account-upgrade flow. Entry point for an anonymous Supabase user who needs a
 * real identity *with an email* attached to their existing account (e.g. the
 * admin billing page blocks paid actions until a billing email exists).
 *
 * A plain bounce to /auth does NOT work here: the anon user already has a
 * Supabase session, so /auth would just redirect them away. Instead this screen
 * LINKS a real identity onto the current anonymous account:
 *   - Google / Apple → supabase.auth.linkIdentity (round-trips through the
 *     provider and back to /auth/callback, which honors `returnTo`).
 *   - Email + password → supabase.auth.signUp, which upgrades the anonymous
 *     user in place. With email confirmation enabled this returns no session,
 *     so we drop into the existing EmailVerification OTP screen; once verified
 *     the account carries a confirmed email.
 *
 * `?returnTo=<path>` is where we send the user once they have an email. It is
 * validated to a same-origin relative path before any redirect (same predicate
 * the OAuth callback uses) to avoid open-redirect abuse.
 */

// Mirror OAuthCallback's same-origin guard: only honor relative paths so a
// crafted `returnTo` can't bounce the user to an attacker-controlled origin.
const isSafeReturnTo = (returnTo: string | undefined): returnTo is string =>
  !!returnTo && returnTo.startsWith('/') && !returnTo.startsWith('//')

const goToReturnTo = (returnTo: string | undefined, fallback: () => void) => {
  if (isSafeReturnTo(returnTo)) {
    // Full navigation (not the router) because returnTo is frequently a
    // different app under the same origin — e.g. /admin/billing.
    window.location.assign(returnTo)
    return
  }
  fallback()
}

export function UpgradeAccount() {
  const navigate = useNavigate()
  const { returnTo } = useSearch({ from: '/upgrade' })
  const {
    user,
    initialized,
    loading: authLoading,
    initialize,
    linkGoogleAccount,
    linkAppleAccount,
    signUp,
    sendEmailVerificationOTP,
    verifyEmailOTP,
  } = useAuthStore()

  const [showEmailForm, setShowEmailForm] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [verificationEmail, setVerificationEmail] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

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
      goToReturnTo(returnTo, () => navigate({ to: '/', search: {} }))
    }
  }, [initialized, authLoading, alreadyUpgraded, returnTo, navigate])

  const handleLink = async (provider: 'google' | 'apple') => {
    setError(null)
    setSubmitting(true)
    try {
      // Thread returnTo through the OAuth state so /auth/callback lands the
      // user back at the originating surface (e.g. /admin/billing) after the
      // provider round-trip. linkGoogleAccount/linkAppleAccount perform the
      // redirect themselves — control does not return here on web.
      const state = isSafeReturnTo(returnTo)
        ? { source: 'link' as const, returnTo }
        : { source: 'link' as const }
      if (provider === 'google') {
        await linkGoogleAccount(state)
      } else {
        await linkAppleAccount(state)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to link ${provider}`)
      setSubmitting(false)
    }
  }

  const handleEmailSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)

    const passwordValidation = validatePassword(password)
    if (!passwordValidation.valid) {
      setError(
        `Password must meet all requirements: ${passwordValidation.unmetRequirements
          .map((r) => r.label.toLowerCase())
          .join(', ')}`,
      )
      return
    }
    if (password !== confirmPassword) {
      setError('Passwords do not match')
      return
    }

    setSubmitting(true)
    try {
      // signUp on an anonymous user upgrades it in place (adds the email/password
      // identity). With email confirmation enabled Supabase returns no session
      // until the OTP is verified — hand off to EmailVerification in that case.
      const { session } = await signUp(email, password)
      if (!session) {
        setVerificationEmail(email)
        return
      }
      // Rare: confirmation disabled → email is live immediately. The
      // alreadyUpgraded effect will pick up the new user and honor returnTo,
      // but redirect eagerly here too so there's no perceptible gap.
      goToReturnTo(returnTo, () => navigate({ to: '/', search: {} }))
    } catch (err) {
      let message = 'Failed to upgrade account'
      if (err instanceof Error) message = err.message
      if (message.includes('already registered') || message.includes('already in use')) {
        message =
          'That email is already attached to another account. Sign in with it instead, or use a different email.'
      }
      setError(message)
      setSubmitting(false)
    }
  }

  // Step 1 of the billing-email fallback: ship an OTP to the address the user
  // typed. We pass it as an override so the OTP primitives don't key off the
  // (absent) account email.
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
      await sendEmailVerificationOTP(trimmed)
      setBillingEmail(trimmed)
      setBillingCodeSent(true)
    } catch (err) {
      setBillingError(
        err instanceof Error ? err.message : 'Failed to send verification code.',
      )
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
      await verifyEmailOTP(billingCode, billingEmail)
    } catch (err) {
      let message = 'Failed to verify code. Please try again.'
      if (err instanceof Error) {
        if (err.message.includes('expired')) {
          message = 'Verification code has expired. Please request a new one.'
        } else if (err.message.includes('invalid')) {
          message = 'Invalid verification code. Please check and try again.'
        } else {
          message = err.message || message
        }
      }
      setBillingError(message)
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

    goToReturnTo(returnTo, () => navigate({ to: '/', search: {} }))
  }

  // ---- Render states -------------------------------------------------------

  // Email confirmation pending: reuse the existing OTP screen verbatim. Once it
  // verifies, the user carries a confirmed email and the alreadyUpgraded effect
  // (or EmailVerification's own navigation) takes over.
  if (verificationEmail) {
    return <EmailVerification autoSend email={verificationEmail} />
  }

  // While auth is still resolving, or we're about to redirect an
  // already-upgraded user, render nothing rather than flash the upgrade form.
  if (!initialized || authLoading || alreadyUpgraded) {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
        </div>
      </div>
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
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <BrandMark className="h-8 w-8" />
              <div className="text-center space-y-1">
                <h2 className="text-xl font-semibold">Add a billing email</h2>
                <p className="text-sm text-muted-foreground">
                  {billingCodeSent
                    ? `Enter the 6-digit code we sent to ${billingEmail}.`
                    : "We couldn't get an email from that provider. Add one for billing — we'll verify it with a code."}
                </p>
              </div>
            </div>

            {billingError && (
              <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
                <p className="text-sm text-red-800 dark:text-red-200">{billingError}</p>
              </div>
            )}

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
        </div>
      </div>
    )
  }

  // Default: anonymous user → present the upgrade options that yield an email.
  return (
    <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
      <GradientBackground />
      <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl">
          <div className="p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <div className="flex items-center gap-3">
                <BrandMark className="h-8 w-8" />
                <h1 className="text-3xl font-bold">Reliant</h1>
              </div>
              <div className="text-center space-y-1">
                <h2 className="text-xl font-semibold">Upgrade your account</h2>
                <p className="text-sm text-muted-foreground">
                  Add an email to your account to continue. Link a provider or
                  set an email and password — we&apos;ll bring you right back.
                </p>
              </div>
            </div>

            {error && (
              <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
                <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
              </div>
            )}

            {!showEmailForm ? (
              <div className="space-y-3">
                <OAuthButton
                  provider="google"
                  onClick={() => handleLink('google')}
                  loading={submitting}
                />

                <div className="relative">
                  <div className="absolute inset-0 flex items-center">
                    <div className="w-full border-t border-border" />
                  </div>
                  <div className="relative flex justify-center text-sm">
                    <span className="px-2 bg-background text-muted-foreground">
                      Or
                    </span>
                  </div>
                </div>

                <button
                  type="button"
                  disabled={submitting}
                  onClick={() => {
                    setError(null)
                    setShowEmailForm(true)
                  }}
                  className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  Continue with email
                </button>
              </div>
            ) : (
              <form className="space-y-5" onSubmit={handleEmailSubmit} autoComplete="on">
                <div className="space-y-4">
                  <div>
                    <label htmlFor="upgrade-email" className="block text-sm font-medium mb-1.5">
                      Email address
                    </label>
                    <input
                      id="upgrade-email"
                      name="email"
                      type="email"
                      autoComplete="username email"
                      autoFocus
                      required
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                      className="block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
                      placeholder="you@example.com"
                    />
                  </div>

                  <PasswordInput
                    id="upgrade-password"
                    name="password"
                    label="Password"
                    value={password}
                    onChange={setPassword}
                    autoComplete="new-password"
                    required
                    disabled={submitting}
                    showStrengthIndicator
                    showRequirements
                  />

                  <ConfirmPasswordInput
                    id="upgrade-confirm-password"
                    name="confirmPassword"
                    label="Confirm Password"
                    value={confirmPassword}
                    password={password}
                    onChange={setConfirmPassword}
                    autoComplete="new-password"
                    required
                    disabled={submitting}
                  />
                </div>

                <div className="space-y-3 pt-1">
                  <button
                    type="submit"
                    disabled={submitting}
                    className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                  >
                    {submitting ? 'Processing...' : 'Upgrade account'}
                  </button>
                  <div className="text-center">
                    <button
                      type="button"
                      disabled={submitting}
                      onClick={() => {
                        setError(null)
                        setShowEmailForm(false)
                      }}
                      className="text-sm text-muted-foreground hover:text-foreground transition-colors"
                    >
                      Back to provider options
                    </button>
                  </div>
                </div>
              </form>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
