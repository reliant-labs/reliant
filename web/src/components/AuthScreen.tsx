import { useState, useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { OAuthButton } from './OAuthButton'
import type { SocialProvider } from './icons/SocialProviderIcon'
import { ForgotPassword } from './ForgotPassword'
import { EmailCodeForm } from './EmailCodeForm'
import { PasswordInput } from './PasswordInput'
import { isSafeReturnTo } from '@/lib/returnTo'
import { describeAuthError } from '@/lib/authErrors'
import { AuthLayout, AuthHeader, AuthError, AuthDivider, AuthLegalLinks } from './AuthLayout'

/**
 * Sign in with an emailed code. One path for everyone, new or returning.
 *
 * ── Why a code and not a password ────────────────────────────────────────
 *
 * The screen this replaces tried to be one form that both signed you in and
 * signed you up. That is the right shape — it provably leaks nothing about
 * which addresses are registered — but it cannot coexist with Chrome's
 * password generation. Chrome decides whether to offer a generated password
 * when the form is RENDERED, from `autocomplete="new-password"`, and a merged
 * form does not know at first paint whether the user is new. Every fix for
 * that reintroduced the sign-up toggle the merge had just removed.
 *
 * A code dissolves the conflict rather than trading against it. There is no
 * password field on first paint, so there is no autocomplete token to get
 * wrong, no generation question, no sign-in/sign-up fork, and no forgotten
 * password to recover.
 *
 * ── Why it cannot leak whether an account exists ──────────────────────────
 *
 * `signInWithOtp({ shouldCreateUser: true })` answers a registered address and
 * an unregistered one with the same empty `200`. One receives a magic-link
 * token, the other a signup token, and nothing in the response distinguishes
 * them — so this component renders the identical code screen for both because
 * it has nothing else it COULD render. The old flow had to buy that property
 * with a carefully-worded ambiguous prompt; here it is a consequence of the
 * API, not of the copy.
 *
 * ── Why password sign-in survives as a secondary screen ───────────────────
 *
 * Codes depend on mail arriving. Our sending domain authenticates correctly
 * but has almost no reputation yet, so mail can land in spam; the password
 * screen is the way through when it does. It is reached deliberately, and on
 * it the field is unambiguously an EXISTING password — so `current-password`
 * is simply correct there and the dilemma that produced this rewrite does not
 * arise. Account creation lives entirely on the code path, which is why no
 * screen here declares `new-password` at all.
 */
type Phase =
  // Email only. One button: Continue. This is where everyone starts.
  | 'email'
  // The 6-digit code we just mailed. Same screen for new and returning users.
  | 'code'
  // Secondary, opt-in: sign in with an existing password.
  | 'password'
  | 'forgot-password'

export function AuthScreen() {
  const [phase, setPhase] = useState<Phase>('email')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  // The provider whose sign-in is currently starting, or null. On success the
  // page navigates or redirects away, so this is only cleared on failure.
  const [pendingProvider, setPendingProvider] = useState<SocialProvider | null>(null)

  const {
    signIn,
    sendEmailSignInCode,
    verifyEmailSignInCode,
    signInAnonymously,
    signInWithGithub,
    signInWithGoogle,
    signInWithApple,
  } = useAuthStore()
  const navigate = useNavigate()
  const { redirect: redirectParam } = useSearch({ from: '/auth' })

  // Track auth screen view for pre-auth funnel
  useEffect(() => {
    if (!window.electronAPI?.analyticsTrack) return

    void window.electronAPI.analyticsTrack({
      eventName: 'auth_screen_viewed',
      metadata: {
        view: phase,
      },
    })
  }, [phase])

  const goToApp = () => {
    if (redirectParam) {
      window.location.href = redirectParam
      return
    }
    navigate({ to: '/', search: {} })
  }

  // Where the magic link in the SAME email should come back to. The link
  // returns through /auth/callback, which honors `returnTo` — so a user who
  // was sent to /auth mid-flow lands back in that flow whether they typed the
  // code or clicked the link. Only same-origin relative paths survive
  // isSafeReturnTo; an absolute `redirect` is handled by goToApp instead.
  const linkState = isSafeReturnTo(redirectParam) ? { returnTo: redirectParam } : undefined

  /**
   * Mail a code. Identical request for every address, so the screen that
   * follows is identical too.
   */
  const handleSendCode = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setLoading(true)

    try {
      await sendEmailSignInCode(email, linkState)
      setPhase('code')
    } catch (err) {
      setError(describeAuthError(err, 'We couldn’t send a code to that address.'))
    } finally {
      setLoading(false)
    }
  }

  const handlePasswordSignIn = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setLoading(true)

    try {
      await signIn(email, password)
      goToApp()
    } catch (err) {
      // No branching on invalid_credentials here. On the code path an unknown
      // address is not an error at all, so this screen has no reason to guess
      // what a failure means — it reports it and offers the code instead.
      setError(describeAuthError(err, 'That email and password didn’t match an account.'))
    } finally {
      setLoading(false)
    }
  }

  const handleOAuthSignIn = async (provider: 'google' | 'github' | 'apple') => {
    // Track WHICH provider is starting, not just that something is. The button
    // for the clicked provider shows the pending state; the others are merely
    // disabled, so the UI never claims to be connecting to a provider the user
    // did not choose.
    setPendingProvider(provider)
    setLoading(true)
    setError(null)

    try {
      if (provider === 'google') {
        await signInWithGoogle()
        // For web, OAuth will redirect. For Electron, window will open.
        // Don't navigate here - let the OAuth callback handle it
      } else if (provider === 'github') {
        await signInWithGithub()
      } else if (provider === 'apple') {
        await signInWithApple()
      }
    } catch (err: unknown) {
      let errorMessage = `Failed to sign in with ${provider}`

      if (err instanceof Error) {
        errorMessage = err.message
      } else if (typeof err === 'object' && err !== null) {
        if ('error_description' in err && typeof err.error_description === 'string') {
          errorMessage = err.error_description
        } else if ('message' in err && typeof err.message === 'string') {
          errorMessage = err.message
        }
      }

      if (errorMessage.includes('Multiple accounts') || errorMessage.includes('same email')) {
        errorMessage = `An account with this email already exists. Please sign in with your existing method first, then link ${provider} in settings.`
      }

      setError(errorMessage)
      setLoading(false)
      setPendingProvider(null)
    }
  }

  const handleSkip = async () => {
    setLoading(true)
    setError(null)
    try {
      await signInAnonymously()
      navigate({ to: '/', search: {} })
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to continue as guest')
      setLoading(false)
    }
  }

  if (phase === 'forgot-password') {
    return (
      <AuthLayout>
        <ForgotPassword onBackToSignIn={() => setPhase('password')} />
      </AuthLayout>
    )
  }

  // The code screen is the shared EmailCodeForm, the same component the
  // password-reset, anonymous-upgrade and billing-email flows use. Only the
  // two calls differ, because GoTrue resolves each flow's token from a
  // different column.
  if (phase === 'code') {
    return (
      <AuthLayout>
        <div className="p-8 space-y-6">
          <AuthHeader title="Check your email" />

          <EmailCodeForm
            email={email}
            idPrefix="auth-signin"
            submitLabel="Sign in"
            onVerify={async (code) => {
              await verifyEmailSignInCode(email, code)
              goToApp()
            }}
            onResend={() => sendEmailSignInCode(email, linkState)}
            footer={
              <div className="text-center">
                <button
                  type="button"
                  onClick={() => setPhase('email')}
                  className="text-sm text-muted-foreground hover:text-foreground transition-colors"
                >
                  Use a different email
                </button>
              </div>
            }
          />

          <AuthLegalLinks />
        </div>
      </AuthLayout>
    )
  }

  const usingPassword = phase === 'password'

  return (
    <AuthLayout>
      <div className="p-8 space-y-6">
        <AuthHeader
          title="Sign in"
          description={
            usingPassword
              ? 'Enter the password for your account.'
              : 'Enter your email and we’ll send you a 6-digit code.'
          }
        />

        <form
          className="space-y-5"
          onSubmit={usingPassword ? handlePasswordSignIn : handleSendCode}
          autoComplete="on"
          id="auth-form"
          method="post"
          action="#"
        >
          {error && <AuthError message={error} />}

          <div className="space-y-4">
            <div>
              <label htmlFor="email" className="block text-sm font-medium mb-1.5">
                Email address
              </label>
              <input
                id="email"
                name="email"
                type="email"
                autoComplete="username email"
                autoFocus
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                disabled={loading}
                className="block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
                placeholder="you@example.com"
              />
            </div>

            {usingPassword && (
              <div>
                {/*
                  Unambiguously an EXISTING password: account creation happens
                  on the code path, so this field is never the "choose a new
                  one" case and `current-password` is simply correct. That is
                  the whole reason this screen is separate.
                */}
                <PasswordInput
                  id="password"
                  name="password"
                  label="Password"
                  value={password}
                  onChange={setPassword}
                  autoComplete="current-password"
                  required
                  disabled={loading}
                  showStrengthIndicator={false}
                  showRequirements={false}
                />

                <div className="flex justify-end mt-1">
                  <button
                    type="button"
                    onClick={() => setPhase('forgot-password')}
                    className="text-sm text-primary hover:underline"
                  >
                    Forgot password?
                  </button>
                </div>
              </div>
            )}
          </div>

          <div className="space-y-3 pt-1">
            <button
              type="submit"
              disabled={loading}
              className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {loading ? 'Processing...' : usingPassword ? 'Sign in' : 'Continue'}
            </button>

            {/*
              A user-initiated toggle between two local phases. No request is
              made either way, so which one a visitor is looking at says
              nothing about whether their address is registered.
            */}
            <div className="text-center text-sm">
              <button
                type="button"
                data-testid="auth-method-toggle"
                onClick={() => {
                  setPhase(usingPassword ? 'email' : 'password')
                  setPassword('')
                  setError(null)
                }}
                className="text-muted-foreground hover:text-foreground transition-colors"
              >
                {usingPassword ? 'Email me a code instead' : 'Use a password instead'}
              </button>
            </div>
          </div>

          <AuthDivider label="Or continue with" />

          <div className="space-y-3">
            <OAuthButton
              provider="github"
              onClick={() => handleOAuthSignIn('github')}
              loading={pendingProvider === 'github'}
              disabled={loading}
            />
            <OAuthButton
              provider="google"
              onClick={() => handleOAuthSignIn('google')}
              loading={pendingProvider === 'google'}
              disabled={loading}
            />
          </div>

          {/* Skip for now — the anonymous path onboarding starts from. */}
          <div className="text-center">
            <button
              type="button"
              disabled={loading}
              onClick={handleSkip}
              className="text-sm text-muted-foreground hover:text-foreground transition-colors"
            >
              Skip for now
            </button>
          </div>

          <AuthLegalLinks />
        </form>
      </div>
    </AuthLayout>
  )
}
