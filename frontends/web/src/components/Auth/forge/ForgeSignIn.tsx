// The sign-in screen with forge sign-in (AUTH_PROVIDER "forge"): what /auth
// renders instead of the Supabase AuthScreen. It talks to reliant-api's login
// broker through lib/auth/native-login.ts; the session is an HttpOnly cookie,
// and every identity the broker answers with goes through the store
// (authStore.signIn for a password, installForgeIdentity for the rest).
//
// Which methods it offers follows the broker (GET /auth/options): a password,
// passkeys, an emailed link, Google and GitHub, and sign-up only where the
// identity serves it. Nothing here offers a guest session: anonymous sign-in
// does not exist with forge sign-in (owner decision).
//
// Errors: a failed SIGN-IN reads the same whatever went wrong with the
// credentials, so the screen cannot tell anyone which addresses have
// accounts; a refused SIGN-UP is specific, because the person must read it to
// fix it (a password names the rules it breaks).
//
// In the desktop app the window does not sign in at all: the system browser
// does (window.electronAPI.forgeAuth.signIn), and the session is handed back.

import { useEffect, useState } from 'react'
import {
  AuthError as NativeAuthError,
  consumeMagicLink,
  isVerifySent,
  passkeySignIn,
  passkeysSupported,
  requestMagicLink,
  resendVerification,
  signUp,
  socialStart,
  unmetRules,
  type Identity,
  type PasswordRule,
} from '@/lib/auth/native-login'
import { useAuthStore } from '@/store/authStore'
import { trackEvent } from '@/lib/analytics'
import { AuthDivider, AuthError, AuthLayout, AuthLegalLinks } from '../../AuthLayout'
import { CheckSpamNote } from '../../CheckSpamNote'
import { OAuthButton } from '../../OAuthButton'
import { PasswordInput } from '../../PasswordInput'
import {
  ForgeAuthCard,
  ForgeAuthPanel,
  ForgeField,
  ForgeNotice,
  ForgeOutlineButton,
  ForgePasswordRequirements,
  ForgePrimaryButton,
  ForgeTextButton,
  forgeInputClass,
} from './ForgeAuthParts'
import { ForgeForgotPassword } from './ForgeForgotPassword'
import {
  installAndLand,
  queryParam,
  rememberReturnTo,
  safeReturnPath,
  useAuthOptions,
  usePasswordPolicy,
} from './forgeAuthLib'
import { landAt } from './forgeLanding'

const SIGN_IN_FAILED = 'That email and password didn’t match an account.'

// ── Desktop ──────────────────────────────────────────────────────────────

// TODO(R7 rebase): use lib/desktop-forge-sign-in.ts. The desktop bridge is
// typed loosely here until that helper lands: present on desktop builds that
// ship forge sign-in, absent everywhere else (an older desktop app falls
// back to signing in in the window).
type DesktopForgeAuth = { signIn: () => Promise<unknown> }

function desktopForgeAuth(): DesktopForgeAuth | null {
  if (typeof window === 'undefined') return null
  const api = (window as { electronAPI?: { forgeAuth?: Partial<DesktopForgeAuth> } }).electronAPI
  return typeof api?.forgeAuth?.signIn === 'function' ? (api.forgeAuth as DesktopForgeAuth) : null
}

/** True when the answer is an authenticated broker Identity. */
function isIdentity(value: unknown): value is Identity {
  return typeof value === 'object' && value !== null && (value as Identity).authenticated === true
}

function DesktopSignIn({ bridge, returnTo }: { bridge: DesktopForgeAuth; returnTo: string }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const start = async () => {
    setBusy(true)
    setError(null)
    try {
      const result = await bridge.signIn()
      if (isIdentity(result)) {
        if (await installAndLand(result, returnTo)) return
      } else {
        // The bridge set the session cookie itself: read it back.
        await useAuthStore.getState().refreshSession()
        if (useAuthStore.getState().identity) {
          landAt(returnTo)
          return
        }
      }
      setError('Sign-in didn’t finish. Please try again.')
    } catch {
      setError('Sign-in didn’t finish. Please try again.')
    }
    setBusy(false)
  }

  return (
    <ForgeAuthCard title="Sign in" description="Reliant opens your browser to sign you in, then brings you back here.">
      {error && <AuthError message={error} />}
      <ForgePrimaryButton type="button" busy={busy} onClick={() => void start()}>
        {busy ? 'Waiting for your browser…' : 'Sign in with your browser'}
      </ForgePrimaryButton>
      <AuthLegalLinks />
    </ForgeAuthCard>
  )
}

// ── The screen ───────────────────────────────────────────────────────────

type Mode = 'signIn' | 'signUp' | 'forgot'

export function ForgeSignIn() {
  // `redirect` is reliant's sign-in param (AuthGuard, lib/returnTo);
  // `returnTo` is forge's, which a forge-built link may carry.
  const [returnTo] = useState(() => safeReturnPath(queryParam('redirect') ?? queryParam('returnTo')))
  const bridge = desktopForgeAuth()
  if (bridge) return <DesktopSignIn bridge={bridge} returnTo={returnTo} />
  return <BrowserSignIn returnTo={returnTo} />
}

function BrowserSignIn({ returnTo }: { returnTo: string }) {
  const { options } = useAuthOptions()
  const policy = usePasswordPolicy()
  const [mode, setMode] = useState<Mode>('signIn')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [serverUnmet, setServerUnmet] = useState<PasswordRule[]>([])
  const [attempted, setAttempted] = useState(false)
  const [unverified, setUnverified] = useState(false)
  // "Check your email": an open sign-up mails a confirmation before any session.
  const [verifySent, setVerifySent] = useState<string | null>(null)
  // The emailed sign-in link's message, once one was requested.
  const [linkSent, setLinkSent] = useState<string | null>(null)
  // What is in flight: "submit", "passkey", "link", or a social provider type.
  const [busy, setBusy] = useState<string | null>(null)
  // The broker marks a failed provider round trip (devidp.SocialFailurePath).
  const [socialFailed] = useState(() => queryParam('social') === 'failed' || queryParam('error') === 'social')

  const signingUp = options.selfSignup && mode === 'signUp'
  const offersPassword = options.methods.includes('password')
  const offersPasskey = options.methods.includes('passkey') && passkeysSupported()
  const offersLink = options.methods.includes('magic_link') && options.mail

  useEffect(() => {
    trackEvent('auth_screen_viewed', { view: mode })
  }, [mode])

  const reset = () => {
    setError(null)
    setServerUnmet([])
    setAttempted(false)
    setUnverified(false)
  }

  const needEmail = (): boolean => {
    if (email.trim() !== '') return false
    setError('Enter your email address first.')
    return true
  }

  const run = async (what: string, action: () => Promise<void>) => {
    setBusy(what)
    reset()
    try {
      await action()
    } catch (err) {
      setError(err instanceof NativeAuthError ? err.message : 'That didn’t work. Please try again.')
      setUnverified(err instanceof NativeAuthError && err.code === 'email_unverified')
    }
    setBusy(null)
  }

  const handleSignIn = async () => {
    try {
      await useAuthStore.getState().signIn(email.trim(), password)
      landAt(returnTo)
    } catch (err) {
      const native = err instanceof NativeAuthError ? err : null
      // Unreachable, unavailable or rate-limited: say so, so a person does not
      // retype a correct password. Anything about the credentials is one sentence.
      const outage = native !== null && (native.status === 0 || native.status === 429 || native.status >= 500)
      setError(outage ? native.message : native ? SIGN_IN_FAILED : 'Sign-in failed. Please try again.')
      setUnverified(native?.code === 'email_unverified')
      setBusy(null)
    }
  }

  const handleSignUp = async () => {
    try {
      const created = await signUp({ email: email.trim(), ...(offersPassword ? { password } : {}) })
      if (isVerifySent(created)) {
        rememberReturnTo(returnTo)
        setVerifySent(created.message)
        setBusy(null)
        return
      }
      if (await installAndLand(created, returnTo)) return
      setError('Your account was created. Sign in to continue.')
      setMode('signIn')
    } catch (err) {
      if (err instanceof NativeAuthError) {
        setError(err.message)
        setServerUnmet(err.unmet)
        if (err.unmet.length > 0) setAttempted(true)
      } else {
        setError('We couldn’t create your account. Please try again.')
      }
    }
    setBusy(null)
  }

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (signingUp && offersPassword && policy !== null && unmetRules(policy, password).length > 0) {
      setAttempted(true)
      setError('Your password doesn’t meet every requirement yet.')
      return
    }
    setBusy('submit')
    reset()
    await (signingUp ? handleSignUp() : handleSignIn())
  }

  const requestLink = () => {
    if (needEmail()) return
    void run('link', async () => {
      rememberReturnTo(returnTo)
      setLinkSent(await requestMagicLink(email.trim()))
    })
  }

  if (mode === 'forgot') {
    return (
      <AuthLayout>
        <ForgeForgotPassword
          onBackToSignIn={() => {
            reset()
            setMode('signIn')
          }}
        />
      </AuthLayout>
    )
  }

  if (verifySent !== null) {
    return (
      <ForgeAuthCard title="Check your email">
        <ForgeNotice tone="success">
          <p>{verifySent}</p>
          <p className="text-muted-foreground">Open the link in the email to confirm your address, then sign in.</p>
        </ForgeNotice>
        <CheckSpamNote />
        <div className="text-center">
          <ForgeTextButton
            onClick={() => {
              setVerifySent(null)
              setMode('signIn')
            }}
          >
            Back to sign in
          </ForgeTextButton>
        </div>
      </ForgeAuthCard>
    )
  }

  if (linkSent !== null) {
    return <LinkSent email={email.trim()} message={linkSent} returnTo={returnTo} onBack={() => setLinkSent(null)} />
  }

  const anyBusy = busy !== null

  return (
    <AuthLayout>
      <ForgeAuthPanel
        title={signingUp ? 'Create your account' : 'Sign in'}
        description={signingUp ? 'Get started with Reliant.' : undefined}
      >
        <form className="space-y-5" onSubmit={onSubmit} aria-busy={anyBusy} id="auth-form" method="post" action="#">
          {socialFailed && !error && <AuthError message="That sign-in didn’t complete. Please try again." />}
          {error && <AuthError message={error} />}
          {unverified && <UnverifiedNotice email={email} />}

          <div className="space-y-4">
            <ForgeField id="email" label="Email address">
              <input
                id="email"
                name="email"
                type="email"
                autoComplete="username email"
                autoFocus
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                disabled={anyBusy}
                className={forgeInputClass}
                placeholder="you@example.com"
              />
            </ForgeField>

            {offersPassword && (
              <div>
                <PasswordInput
                  id="password"
                  name="password"
                  label="Password"
                  value={password}
                  onChange={(next) => {
                    setPassword(next)
                    // A changed password is a new one: the last verdict no longer applies.
                    if (signingUp && (serverUnmet.length > 0 || attempted)) {
                      setServerUnmet([])
                      setError(null)
                    }
                  }}
                  autoComplete={signingUp ? 'new-password' : 'current-password'}
                  required
                  disabled={anyBusy}
                  minLength={0}
                  showStrengthIndicator={false}
                  showRequirements={false}
                />
                {signingUp ? (
                  <ForgePasswordRequirements
                    id="password-requirements"
                    policy={policy}
                    password={password}
                    serverUnmet={serverUnmet}
                    attempted={attempted}
                  />
                ) : options.mail ? (
                  // Reset is a mailed link: offered only where the identity's
                  // mail reaches anyone (GET /auth/options `mail`).
                  <div className="flex justify-end mt-1">
                    <button
                      type="button"
                      onClick={() => {
                        reset()
                        setMode('forgot')
                      }}
                      className="text-sm text-primary-ink hover:underline"
                    >
                      Forgot password?
                    </button>
                  </div>
                ) : null}
              </div>
            )}
          </div>

          <div className="space-y-3 pt-1">
            {(offersPassword || signingUp) && (
              <ForgePrimaryButton busy={busy === 'submit'} disabled={anyBusy}>
                {busy === 'submit'
                  ? signingUp
                    ? 'Creating your account…'
                    : 'Signing in…'
                  : signingUp
                    ? 'Create account'
                    : 'Sign in'}
              </ForgePrimaryButton>
            )}

            {!signingUp && offersPasskey && (
              <ForgeOutlineButton
                busy={busy === 'passkey'}
                disabled={anyBusy}
                onClick={() => {
                  if (needEmail()) return
                  void run('passkey', async () => {
                    const identity = await passkeySignIn(email.trim())
                    if (!(await installAndLand(identity, returnTo))) throw new Error('not signed in')
                  })
                }}
              >
                Continue with a passkey
              </ForgeOutlineButton>
            )}

            {!signingUp && offersLink && (
              <ForgeOutlineButton busy={busy === 'link'} disabled={anyBusy} onClick={requestLink}>
                Email me a sign-in link
              </ForgeOutlineButton>
            )}

            {options.selfSignup && (
              <div className="text-center text-sm">
                <button
                  type="button"
                  data-testid="auth-mode-toggle"
                  onClick={() => {
                    reset()
                    setMode(signingUp ? 'signIn' : 'signUp')
                  }}
                  className="text-muted-foreground hover:text-foreground transition-colors"
                >
                  {signingUp ? 'Already have an account? Sign in' : 'New here? Create an account'}
                </button>
              </div>
            )}
          </div>

          {!signingUp && options.social.length > 0 && (
            <>
              <AuthDivider label="Or continue with" />
              <div className="space-y-3">
                {options.social.map((provider) =>
                  provider.type === 'github' || provider.type === 'google' ? (
                    <OAuthButton
                      key={provider.type}
                      provider={provider.type}
                      loading={busy === provider.type}
                      disabled={anyBusy}
                      onClick={() => void run(provider.type, () => socialStart(provider.type, returnTo))}
                    />
                  ) : (
                    <ForgeOutlineButton
                      key={provider.type}
                      busy={busy === provider.type}
                      disabled={anyBusy}
                      onClick={() => void run(provider.type, () => socialStart(provider.type, returnTo))}
                    >
                      Continue with {provider.label}
                    </ForgeOutlineButton>
                  ),
                )}
              </div>
            </>
          )}

          <AuthLegalLinks />
        </form>
      </ForgeAuthPanel>
    </AuthLayout>
  )
}

/** The emailed sign-in link is on its way: open it, or type its code here. */
function LinkSent({
  email,
  message,
  returnTo,
  onBack,
}: {
  email: string
  message: string
  returnTo: string
  onBack: () => void
}) {
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<'code' | 'resend' | null>(null)
  const [current, setCurrent] = useState(message)

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy('code')
    setError(null)
    try {
      const identity = await consumeMagicLink(code.trim())
      if (await installAndLand(identity, returnTo)) return
      setError('That sign-in didn’t complete. Please try again.')
    } catch (err) {
      setError(err instanceof NativeAuthError ? err.message : 'That sign-in didn’t complete. Please try again.')
    }
    setBusy(null)
  }

  return (
    <ForgeAuthCard title="Check your email">
      <ForgeNotice tone="success">
        <p>{current}</p>
        <p className="text-muted-foreground">
          Open the link in this browser, or type the code from the email here. Never share it: Reliant will never ask
          you for it.
        </p>
      </ForgeNotice>
      <form className="space-y-5" onSubmit={onSubmit} aria-busy={busy !== null}>
        {error && <AuthError message={error} />}
        <ForgeField id="link-code" label="Code from the email">
          <input
            id="link-code"
            name="code"
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            required
            value={code}
            onChange={(e) => setCode(e.target.value)}
            disabled={busy !== null}
            className={forgeInputClass}
          />
        </ForgeField>
        <ForgePrimaryButton busy={busy === 'code'} disabled={busy !== null}>
          {busy === 'code' ? 'Signing in…' : 'Sign in'}
        </ForgePrimaryButton>
        <CheckSpamNote />
        <div className="flex justify-between">
          <ForgeTextButton onClick={onBack} disabled={busy !== null}>
            Use a different email
          </ForgeTextButton>
          <ForgeTextButton
            disabled={busy !== null}
            onClick={() => {
              setBusy('resend')
              setError(null)
              void requestMagicLink(email)
                .then(setCurrent)
                .catch((err: unknown) =>
                  setError(err instanceof NativeAuthError ? err.message : 'We couldn’t send a new link. Please try again.'),
                )
                .finally(() => setBusy(null))
            }}
          >
            Send a new link
          </ForgeTextButton>
        </div>
      </form>
    </ForgeAuthCard>
  )
}

/** The password was right but the address is unconfirmed: offer a new link. */
function UnverifiedNotice({ email }: { email: string }) {
  const [message, setMessage] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  return (
    <ForgeNotice tone="info">
      <p>This address isn’t confirmed yet. Use the link we emailed you, or ask for a new one.</p>
      {message ? (
        <p className="text-muted-foreground">{message}</p>
      ) : (
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            setBusy(true)
            void resendVerification(email.trim())
              .then(setMessage)
              .catch(() => setMessage('We couldn’t send a new link right now. Please try again.'))
              .finally(() => setBusy(false))
          }}
          className="text-sm text-primary-ink hover:underline disabled:opacity-50"
        >
          Send a new link
        </button>
      )}
    </ForgeNotice>
  )
}
