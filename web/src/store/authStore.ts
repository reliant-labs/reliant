import { create } from 'zustand'
import type { User, Session, AuthChangeEvent } from '@supabase/supabase-js'
import { supabase, setAuthStorageUnreadableHandler } from '@/lib/supabase'
import { logger } from '@/lib/logger'
import { getAppURL } from '@/lib/constants'
import { setSentryUser } from '@/lib/sentry'
import { queryClient } from '@/lib/query-client'
import {
  startOAuthSignIn,
  oauthCallbackTransport,
  type OAuthProvider,
} from '@/lib/oauth-signin'

const isElectron = !!window.electronAPI

/**
 * Where the identity provider should send the user back to.
 *
 * This must be the PUBLIC app URL, never the renderer's own origin. In the
 * packaged desktop app the renderer is served from a local address, so
 * `window.location.origin` is an ephemeral loopback port — sending that to a
 * provider produced the `http://127.0.0.1:<port>/auth/callback` redirects users
 * hit in the field. `getAppURL()` resolves the hosted origin and only falls
 * through to the document origin when that origin is genuinely public.
 *
 * Electron completes the round trip by opening the provider in the system
 * browser (see linkOAuthIdentity), which lands on the hosted callback page.
 */
const getOAuthRedirectUrl = async (): Promise<string> => {
  return `${getAppURL()}/auth/callback`
}

// Append OAuth round-trip state to the redirect URL as query params. Replaces
// the previous side-channel localStorage flags (Phase 3 refactor).
// `source` tags the trigger for analytics (signin vs link). `returnTo`
// preserves the originating URL so the callback can land the user back where
// they came from (e.g. mid-onboarding step + plan).
type OAuthRedirectState = {
  source?: 'signin' | 'link'
  returnTo?: string
}

// Providers that can be attached to an existing account via linkIdentity.
export type LinkableProvider = 'google' | 'github' | 'apple'

const providerLabels: Record<LinkableProvider, string> = {
  google: 'Google',
  github: 'GitHub',
  apple: 'Apple',
}

const withOAuthState = (baseUrl: string, state: OAuthRedirectState): string => {
  try {
    const url = new URL(baseUrl)
    if (state.source) url.searchParams.set('source', state.source)
    if (state.returnTo) url.searchParams.set('returnTo', state.returnTo)
    return url.toString()
  } catch {
    // Fallback for unparseable URLs (shouldn't happen for redirect URLs).
    return baseUrl
  }
}

const normalizeFailureReason = (error: unknown): string => {
  if (typeof error === 'object' && error !== null) {
    if ('code' in error && typeof error.code === 'string' && error.code.length > 0) {
      return error.code
    }
    if ('message' in error && typeof error.message === 'string' && error.message.length > 0) {
      return error.message.toLowerCase().replace(/\s+/g, '_').slice(0, 120)
    }
  }

  if (typeof error === 'string' && error.length > 0) {
    return error.toLowerCase().replace(/\s+/g, '_').slice(0, 120)
  }

  return 'unknown_error'
}

const trackAuthFunnelEvent = async (eventName: string, metadata: Record<string, unknown> = {}): Promise<void> => {
  if (!isElectron || !window.electronAPI?.analyticsTrack) {
    return
  }

  try {
    await window.electronAPI.analyticsTrack({
      eventName,
      metadata,
    })
  } catch (error) {
    logger.debug('[AuthStore] analyticsTrack failed', { eventName, error })
  }
}


// OAuth functionality temporarily removed for API key focus

interface AuthState {
  user: User | null
  session: Session | null
  loading: boolean
  initialized: boolean
  authError: string | null // For OAuth callback errors

  setUser: (user: User | null) => void
  setSession: (session: Session | null) => void
  setLoading: (loading: boolean) => void
  clearAuthError: () => void

  signIn: (email: string, password: string) => Promise<void>
  linkEmailIdentity: (
    email: string,
    password: string,
    state?: OAuthRedirectState,
  ) => Promise<{ user: User | null; verificationRequired: boolean }>
  signInWithGoogle: () => Promise<void>
  signInWithGithub: (state?: OAuthRedirectState) => Promise<void>
  signInWithApple: () => Promise<void>
  linkOAuthIdentity: (provider: LinkableProvider, state?: OAuthRedirectState) => Promise<void>
  linkGoogleAccount: (state?: OAuthRedirectState) => Promise<void>
  linkAppleAccount: (state?: OAuthRedirectState) => Promise<void>
  linkGithubAccount: (state?: OAuthRedirectState) => Promise<void>
  unlinkIdentity: (identityId: string) => Promise<void>
  sendPasswordResetOTP: (email: string) => Promise<void>
  verifyPasswordResetOTP: (email: string, code: string) => Promise<void>
  updatePassword: (newPassword: string) => Promise<void>
  sendEmailSignInCode: (email: string, state?: OAuthRedirectState) => Promise<void>
  verifyEmailSignInCode: (email: string, code: string) => Promise<void>
  resendSignupVerification: (email: string) => Promise<void>
  verifyEmailOTP: (code: string, emailOverride?: string) => Promise<void>
  verifyEmailIdentityOTP: (code: string, email: string) => Promise<void>
  sendEmailIdentityVerification: (
    email: string,
    state?: OAuthRedirectState,
  ) => Promise<void>
  signInAnonymously: () => Promise<void>
  signOut: () => Promise<void>
  setApiKeySession: (apiKey: string) => void

  initialize: () => Promise<void>
  refreshSession: () => Promise<void>
}

/**
 * Start provider sign-in, with the analytics funnel and error handling that
 * every provider shares.
 *
 * The flow itself lives in lib/oauth-signin.ts so the browser and the desktop
 * app run the same code; this wrapper exists only so the three store actions
 * do not each repeat the same funnel events and try/catch. Sign-in COMPLETES
 * at /auth/callback (OAuthCallback.tsx), which is why the success event here
 * records a hand-off rather than a session.
 *
 * It takes no `set`: this deliberately writes no store state (see below), and
 * the vestigial parameter it used to accept failed `tsc` under
 * noUnusedParameters, breaking the production build while vitest stayed green.
 */
const runOAuthSignIn = async (
  provider: OAuthProvider,
  state?: OAuthRedirectState,
): Promise<void> => {
  // The global `loading` flag is deliberately untouched. AuthGuard swaps the
  // entire tree for a full-screen LoadingSpinner while it is true, so setting
  // it here tore the sign-in screen down and rebuilt it in the gap before the
  // browser navigated to the provider — indistinguishable from a page refresh.
  // AuthScreen already shows a per-provider pending state on the button that
  // was clicked, which acknowledges the click without unmounting anything.
  const startedAt = Date.now()
  const transport = oauthCallbackTransport()

  await trackAuthFunnelEvent('oauth_started', {
    auth_method: provider,
    oauth_callback_transport: transport,
  })

  try {
    await startOAuthSignIn(provider, state)
  } catch (error) {
    await trackAuthFunnelEvent('oauth_failed', {
      auth_method: provider,
      oauth_callback_transport: transport,
      failure_reason: normalizeFailureReason(error),
      latency_ms: Date.now() - startedAt,
    })
    logger.error(`[AuthStore] ${provider} sign-in failed:`, error)
    throw error
  }
}

export const useAuthStore = create<AuthState>((set, get) => ({
  user: null,
  session: null,
  loading: true,
  authError: null,
  initialized: false,

  setUser: (user: User | null) => set({ user }),
  setSession: (session: Session | null) => set({ session }),
  setLoading: (loading: boolean) => set({ loading }),
  clearAuthError: () => set({ authError: null }),

  signIn: async (email: string, password: string) => {
    const startedAt = Date.now()
    await trackAuthFunnelEvent('login_attempted', {
      auth_method: 'password',
    })

    // Don't set loading state here to avoid remounting components
    const { data, error } = await supabase.auth.signInWithPassword({
      email,
      password,
    })

    if (error) {
      await trackAuthFunnelEvent('login_failed', {
        auth_method: 'password',
        failure_reason: normalizeFailureReason(error),
        latency_ms: Date.now() - startedAt,
      })
      throw error
    }

    set({
      user: data.user,
      session: data.session,
    })
  },

  /**
   * Attach an email to the CURRENT (anonymous) user — the email twin of
   * linkOAuthIdentity.
   *
   * `signUp` is NOT the call for this, though it was used here for a long time
   * and the old comment claimed it "upgrades the anonymous user in place". It
   * does not: signUp POSTs to /signup, which either mints a SEPARATE user or
   * returns a user with no session, leaving the anonymous account — its org,
   * its redeemed coupons, its onboarding progress — untouched and still
   * anonymous. The user then came back from the confirmation email to the same
   * "Finish setting up your account" modal, forever.
   *
   * Supabase upgrades an anonymous session in place via `updateUser`, which
   * PUTs /user with the existing session's access token, so the email attaches
   * to that user id and nothing is migrated. GoTrue answers it by sending an
   * EMAIL CHANGE verification (see its UserUpdate handler: an anonymous user
   * getting an email goes down the `sendEmailChange` path), which is why
   * verification here is `type: 'email_change'` and not `'signup'`.
   *
   * ── Why both fields still go in the FIRST call ───────────────────────────
   *
   * An earlier version of this comment argued for sending email and password
   * together and stopped there. That was half right, and the missing half
   * trapped users. Sending both IS required on the first attempt: GoTrue
   * refuses a password-only update on an anonymous user, rejecting exactly
   * `password != "" && email == "" && phone == ""`. So there is no "verify the
   * email first, then set the password" ordering available to us.
   *
   * ── The trap that ordering creates, and the retry that escapes it ────────
   *
   * What the old comment missed is that GoTrue's UserUpdate handler processes
   * the two fields in a fixed order within one transaction:
   *
   *   1. password → SetPassword + UpdatePassword   (applied IMMEDIATELY)
   *   2. email    → sendEmailChange                (left PENDING)
   *
   * So any first attempt that half-lands — the mail send fails, the user
   * abandons the tab, the address is never confirmed — leaves an account with
   * a password set and no verified email. On a retry with the SAME password,
   * GoTrue reaches step 1, finds `isSamePassword`, and returns
   * `422 same_password` BEFORE REACHING STEP 2. No email is sent, and the user
   * is told to pick a different password for an account they do not believe
   * they have. That is a dead end reached by doing the obvious thing twice.
   *
   * The escape is to retry with the email ALONE. Omitting `password` skips the
   * password branch entirely, so GoTrue goes straight to sendEmailChange and
   * the user gets their verification email. `same_password` is therefore not
   * an error here — it is the server telling us the password half is already
   * done, and the only thing left is verification.
   */
  linkEmailIdentity: async (email: string, password: string, state?: OAuthRedirectState) => {
    const redirectTo = withOAuthState(await getOAuthRedirectUrl(), {
      source: 'link',
      ...state,
    })

    logger.info('[AuthStore] linkEmailIdentity: upgrading anonymous user in place', {
      redirectTo,
    })

    let { data, error } = await supabase.auth.updateUser(
      { email, password },
      { emailRedirectTo: redirectTo },
    )

    if (error && (error as { code?: string }).code === 'same_password') {
      // The account is already half-upgraded: the password landed on a
      // previous attempt, the email never got confirmed. Re-send the email on
      // its own rather than reporting a failure the user cannot act on.
      logger.info(
        '[AuthStore] linkEmailIdentity: password already set; re-sending verification only',
        { userId: get().user?.id },
      )
      ;({ data, error } = await supabase.auth.updateUser(
        { email },
        { emailRedirectTo: redirectTo },
      ))
    }

    if (error) {
      // GoTrue answers a taken address with email_exists / "already been
      // registered". Surface it as-is — the callers turn it into the "that
      // email is already attached to another account" copy, and we must NOT
      // sign the user into that account, which would strand the work sitting
      // in the session they are trying to upgrade.
      logger.error('[AuthStore] linkEmailIdentity failed', error)
      throw error
    }

    // updateUser returns the SAME user (same id) and issues no new session, so
    // the session is useless as a "did it link?" signal — the caller already
    // HAS one, the anonymous one, and reading that as success is precisely the
    // bug this replaces.
    //
    // The honest signal is the user itself. With confirmations on, GoTrue
    // returns the address as PENDING (`new_email`) and leaves `email` unset
    // until it is verified; with confirmations off it lands on `email`
    // straight away and the account is linked with nothing left to do.
    const linkedEmail = data.user?.email
    const verificationRequired = !linkedEmail || linkedEmail !== email

    set({ user: data.user ?? get().user })

    logger.info('[AuthStore] linkEmailIdentity: updateUser accepted', {
      userId: data.user?.id,
      verificationRequired,
    })

    return { user: data.user, verificationRequired }
  },

  // Provider sign-in — ONE implementation for every provider and every surface.
  //
  // This used to be three near-identical copies, each split down the middle by
  // an `isElectron` branch: the desktop half called a SERVER RPC
  // (SystemService/StartOAuthSignIn) that ran the CLI login flow inside the
  // hosted API pod — opening a browser and listening on 127.0.0.1 THERE, which
  // is not the user's machine. Against prod it failed closed:
  //   "auth provider not configured: RELIANT_AUTH_URL must be set"
  //
  // Now both surfaces run exactly the same code. startOAuthSignIn() differs
  // only in where the provider is told to redirect (see lib/oauth-signin.ts),
  // and the session is established by the SAME /auth/callback route with the
  // SAME exchangeCodeForSession call in both. Nothing about sign-in depends on
  // the daemon, so it works whether the daemon is local, remote, or absent —
  // unlike Claude/Codex OAuth, whose credentials are daemon-scoped by design.
  signInWithGoogle: async () => {
    await runOAuthSignIn('google')
  },

  signInWithGithub: async (state?: OAuthRedirectState) => {
    // The Supabase GitHub provider is sign-in only (0 scopes). We never persist
    // the provider_token here — repo access comes from the dedicated
    // /auth/github/authorize custom flow.
    await runOAuthSignIn('github', state)
  },

  signInWithApple: async () => {
    await runOAuthSignIn('apple')
  },


  signInAnonymously: async () => {
    await trackAuthFunnelEvent('login_attempted', {
      auth_method: 'anonymous',
    })

    const { data, error } = await supabase.auth.signInAnonymously()

    if (error) {
      await trackAuthFunnelEvent('login_failed', {
        auth_method: 'anonymous',
        failure_reason: normalizeFailureReason(error),
      })
      throw error
    }

    set({
      user: data.user,
      session: data.session,
    })

    await trackAuthFunnelEvent('login_succeeded', {
      auth_method: 'anonymous',
    })
  },

  signOut: async () => {
    set({ loading: true })
    try {
      // Clear API key if present
      localStorage.removeItem('reliant-api-key')

      const { error } = await supabase.auth.signOut()
      if (error) throw error

      // NOTE: supabase.auth.signOut() will automatically call our storage adapter's
      // removeItem() which clears the auth file. No need for manual authClear().

      // Clear all store data
      // Import these dynamically to avoid circular dependencies
      const { useChatStore } = await import('./chatStore')
      const { useProjectStore } = await import('./projectStore')
      const { useWorktreeStore } = await import('./worktreeStore')
      const { useChatNavigationStore } = await import('./chatNavigationStore')
      const { useAttachmentStore } = await import('./attachmentStore')
      const { useTasksStore } = await import('./tasksStore')
      const { useProcessStore } = await import('./processStore')

      // Reset all stores (order matters: navigation first to trigger chat cleanup, then everything else)
      useChatNavigationStore.getState().reset()
      useChatStore.getState().reset()
      useProjectStore.getState().reset()
      useWorktreeStore.getState().reset()
      useAttachmentStore.getState().reset()
      useTasksStore.getState().reset()
      useProcessStore.getState().reset()

      // Drop every cached answer about who the user is.
      //
      // The QueryClient is a module-level singleton and the onboarding queries
      // are keyed on ['onboarding', …] with no user id in the key, so without
      // this the NEXT user inherits this one's answers for a full staleTime
      // window. `currentUser.onboardingCompleted` is the gate ModernApp uses to
      // decide whether to onboard at all — a brand-new anonymous account read
      // the departed user's `true` and was sent straight into the app, never
      // seeing onboarding. `cloud.getCurrentUser` keeps its own 30s promise
      // cache for the same data, which is why clearing the query cache alone is
      // not enough.
      const { resetUserCache } = await import(
        '@/services/controlPlane/onboarding'
      )
      resetUserCache()
      queryClient.clear()

      set({
        user: null,
        session: null,
        loading: false,
      })
    } catch (error) {
      set({ loading: false })
      throw error
    }
  },

  // Attach a provider identity to the CURRENT user. This is linkIdentity, not
  // signInWithOAuth: an anonymous user keeps their existing account (and its
  // chats/workspaces) and simply gains a real identity on it.
  // Deliberately does NOT set the store's global `loading`. AuthGuard renders
  // a full-screen LoadingSpinner whenever it is true, so flipping it here
  // unmounted the whole tree for the few hundred ms before the browser left
  // for the provider — which reads as the page refreshing, not as progress.
  // The click is acknowledged by the caller's own per-provider pending state
  // (LinkIdentityForm's pendingProvider -> OAuthButton), which leaves the page
  // on screen. Same reasoning as signIn's note above.
  linkOAuthIdentity: async (provider: LinkableProvider, state?: OAuthRedirectState) => {
    try {
      // Thread OAuth round-trip state (source/returnTo) onto the redirect URL
      // so the /auth/callback handler can land the user back where the link
      // flow was triggered (e.g. the admin billing page via /upgrade).
      const redirectTo = withOAuthState(await getOAuthRedirectUrl(), state ?? {})
      logger.info('[AuthStore] linkOAuthIdentity: Starting link flow', {
        provider,
        isElectron,
        redirectTo,
      })

      const { data, error } = await supabase.auth.linkIdentity({
        provider,
        options: {
          redirectTo,
          skipBrowserRedirect: true,
        },
      })

      logger.info('[AuthStore] linkOAuthIdentity: linkIdentity response', {
        provider,
        hasData: !!data,
        hasUrl: !!data?.url,
        urlPreview: data?.url?.substring(0, 100),
        error: error?.message,
        errorCode: error?.code,
      })

      if (error) {
        // The identity already belongs to some other account. Say so plainly —
        // we must NOT sign them in as that account, which would strand the work
        // sitting in the session they are trying to upgrade.
        if (error.message?.includes('already linked') ||
            error.message?.includes('identity already exists') ||
            error.code === 'identity_already_exists') {
          throw new Error(`This ${providerLabels[provider]} account is already linked to an existing account. Please sign out and sign in with ${providerLabels[provider]} instead.`)
        }
        throw error
      }

      if (isElectron && data?.url && window.electronAPI) {
        logger.info('[AuthStore] linkOAuthIdentity: Opening external browser', { provider })
        await window.electronAPI.openExternal(data.url)
      } else if (data?.url) {
        window.location.href = data.url
      } else {
        logger.error('[AuthStore] linkOAuthIdentity: No link URL returned from Supabase', { provider })
      }
    } catch (error) {
      logger.error('[AuthStore] linkOAuthIdentity: Error:', error)
      throw error
    }
  },

  linkGoogleAccount: (state?: OAuthRedirectState) => get().linkOAuthIdentity('google', state),
  linkAppleAccount: (state?: OAuthRedirectState) => get().linkOAuthIdentity('apple', state),
  linkGithubAccount: (state?: OAuthRedirectState) => get().linkOAuthIdentity('github', state),

  unlinkIdentity: async (identityId: string) => {
    set({ loading: true })
    try {
      // Get the full identity object from user.identities
      const { data: { user } } = await supabase.auth.getUser()

      if (!user) {
        throw new Error('User not found')
      }

      const identity = user.identities?.find((id) => id.id === identityId)

      if (!identity) {
        throw new Error('Identity not found')
      }

      // Safety check: prevent unlinking last identity
      if (user.identities && user.identities.length <= 1) {
        throw new Error('Cannot unlink your only authentication method')
      }

      // Pass the full identity object to Supabase
      const { error } = await supabase.auth.unlinkIdentity(identity)

      if (error) throw error

      // Refresh user data to get updated identities
      const { data: { user: updatedUser } } = await supabase.auth.getUser()
      set({ user: updatedUser, loading: false })
    } catch (error) {
      set({ loading: false })
      throw error
    }
  },

  /**
   * The primary sign-in: mail a 6-digit code (and a link) to an address,
   * whether or not it belongs to an account.
   *
   * `shouldCreateUser: true` is the entire anti-enumeration argument, and it
   * is structural rather than a matter of careful copy. GoTrue answers a known
   * address and an unknown one with the SAME `200 {}` here — one gets a
   * magiclink token, the other gets a signup token, and the client cannot tell
   * which, because the response body is empty either way. There is no branch
   * to leak from and no error to read. The password screen had to buy this
   * property with a carefully-worded ambiguous prompt; this gets it for free.
   *
   * `emailRedirectTo` matters because the SAME message carries a clickable
   * link built from `{{ .TokenHash }}`. Without it GoTrue falls back to the
   * project's site URL and the link lands somewhere that cannot complete a
   * sign-in. Pointed at /auth/callback, which verifies token_hash + type
   * generically — so the code and the link finish the same sign-in.
   */
  sendEmailSignInCode: async (email: string, state?: OAuthRedirectState) => {
    await trackAuthFunnelEvent('login_attempted', { auth_method: 'email_otp' })

    const { error } = await supabase.auth.signInWithOtp({
      email,
      options: {
        shouldCreateUser: true,
        emailRedirectTo: withOAuthState(await getOAuthRedirectUrl(), {
          source: 'signin',
          ...state,
        }),
      },
    })

    if (error) {
      // Notably includes over_email_send_rate_limit. Surfacing it is the
      // point: reporting success for a send that did not happen leaves the
      // user staring at a code-entry box waiting for mail that is not coming.
      await trackAuthFunnelEvent('login_failed', {
        auth_method: 'email_otp',
        failure_reason: normalizeFailureReason(error),
      })
      logger.error('[AuthStore] Failed to send email sign-in code:', error)
      throw error
    }
  },

  /**
   * Redeem the 6-digit code from sendEmailSignInCode.
   *
   * `type: 'email'` and not 'signup' or 'magiclink'. Those two name the
   * COLUMN GoTrue looks the token up in — confirmation_token for a brand-new
   * account, recovery_token for a returning one — and this flow deliberately
   * cannot know which population the user is in. 'email' is supabase-js's
   * union for exactly this case: it is the type paired with signInWithOtp and
   * it matches either token. Hardcoding one of the specific types would fail
   * with "expired or invalid" for half of all users, on a code that is fine.
   */
  verifyEmailSignInCode: async (email: string, code: string) => {
    const { data, error } = await supabase.auth.verifyOtp({
      email,
      token: code,
      type: 'email',
    })

    if (error) {
      await trackAuthFunnelEvent('login_failed', {
        auth_method: 'email_otp',
        failure_reason: normalizeFailureReason(error),
      })
      logger.error('[AuthStore] Email sign-in code verification failed:', error)
      throw error
    }

    set({ user: data.user, session: data.session })
    await trackAuthFunnelEvent('login_succeeded', { auth_method: 'email_otp' })
  },

  sendPasswordResetOTP: async (email: string) => {
    try {
      const redirectTo = await getOAuthRedirectUrl()
      const { error } = await supabase.auth.resetPasswordForEmail(email, {
        redirectTo,
      })

      if (error) {
        logger.error('[AuthStore] Failed to send password reset OTP:', error)
        throw error
      }
    } catch (error) {
      logger.error('[AuthStore] sendPasswordResetOTP error:', error)
      throw error
    }
  },

  verifyPasswordResetOTP: async (email: string, code: string) => {
    try {
      const { data, error } = await supabase.auth.verifyOtp({
        email,
        token: code,
        type: 'recovery',
      })

      if (error) {
        logger.error('[AuthStore] OTP verification failed:', error)
        throw error
      }

      // Update auth state with the new session
      set({
        user: data.user,
        session: data.session,
      })
    } catch (error) {
      logger.error('[AuthStore] verifyPasswordResetOTP error:', error)
      throw error
    }
  },

  /**
   * Re-send the confirmation for a user created by `signUp` — and ONLY that
   * user.
   *
   * The distinction from sendEmailIdentityVerification is not stylistic, it is
   * which database column GoTrue reads. `resend` looks the account up by
   * `users.email`, which a signUp user HAS (unconfirmed, but populated), so
   * `type: 'signup'` finds them and re-sends against `confirmation_token`.
   *
   * An anonymous user being upgraded has an empty `users.email` — their
   * address sits in `users.email_change` — so this call would find nobody and
   * return a bare 200 having sent nothing. That flow must use
   * sendEmailIdentityVerification instead. Sending a signup resend down the
   * upgrade path is the original "returns 200, no email arrives" bug; do not
   * reintroduce it by making this the generic resend.
   */
  resendSignupVerification: async (email: string) => {
    const { error } = await supabase.auth.resend({ type: 'signup', email })

    if (error) {
      logger.error('[AuthStore] Failed to resend signup verification:', error)
      throw error
    }
  },

  verifyEmailOTP: async (code: string, emailOverride?: string) => {
    try {
      const { user } = get()

      const emailToVerify = emailOverride ?? user?.email
      if (!emailToVerify) {
        throw new Error('No user email found')
      }

      const { data, error } = await supabase.auth.verifyOtp({
        email: emailToVerify,
        token: code,
        type: 'signup',
      })

      if (error) {
        logger.error('[AuthStore] Email OTP verification failed:', error)
        throw error
      }

      // verifyOtp can return a fresh session/user. Use it directly when available.
      if (data.session || data.user) {
        set({
          user: data.user ?? null,
          session: data.session ?? null,
        })
      } else {
        await get().refreshSession()
      }
    } catch (error) {
      logger.error('[AuthStore] verifyEmailOTP error:', error)
      throw error
    }
  },

  /**
   * Verify the 6-digit code from the email-identity link flow.
   *
   * Distinct from verifyEmailOTP, which uses `type: 'signup'` for a user
   * created by signUp. An anonymous user upgraded via updateUser has a pending
   * EMAIL CHANGE, so its one-time token lives in GoTrue's
   * email_change_token_new column and only `type: 'email_change'` matches it —
   * verifying with 'signup' looks up confirmation_token and fails with "Token
   * has expired or is invalid" even when the code is correct.
   *
   * On success GoTrue reloads the user (regenerating is_anonymous, now false),
   * creates the email identity on the SAME user id, and issues a session.
   */
  verifyEmailIdentityOTP: async (code: string, email: string) => {
    const { data, error } = await supabase.auth.verifyOtp({
      email,
      token: code,
      type: 'email_change',
    })

    if (error) {
      logger.error('[AuthStore] Email identity verification failed:', error)
      throw error
    }

    if (data.session || data.user) {
      set({ user: data.user ?? null, session: data.session ?? null })
    } else {
      await get().refreshSession()
    }
  },

  /**
   * Re-send the email-identity verification — via `updateUser`, NOT
   * `supabase.auth.resend`.
   *
   * This is the "resend returns 200 but no email ever arrives" bug, and the
   * reason is that `resend` CANNOT FIND THIS USER. GoTrue's Resend handler
   * resolves the account with
   *
   *   models.FindUserByEmailAndAudience(db, params.Email, aud)
   *     → WHERE LOWER(email) = ?          -- the users.EMAIL column
   *
   * An anonymous user mid-upgrade has an EMPTY `users.email`: the address they
   * typed lives in `users.email_change` until it is confirmed. Looking them up
   * by that pending address therefore misses, hits `IsNotFoundError`, and
   * returns `200 {}` having sent nothing — GoTrue answers a resend for an
   * unknown address with a bare 200 on purpose, so the endpoint cannot be used
   * to enumerate accounts.
   *
   * That is true for `type: 'signup'` AND for `type: 'email_change'`. The
   * earlier fix here swapped one for the other and changed nothing observable,
   * because the lookup fails before the type is ever consulted. Both variants
   * are silently dead for precisely the user who needs them.
   *
   * `updateUser` works because it identifies the user from the session's
   * access token rather than from an email lookup, and re-entering it with the
   * same pending address runs `sendEmailChange` again — a genuine resend.
   *
   * The password is deliberately omitted: including it would re-enter GoTrue's
   * password branch and fail with `422 same_password` before any mail is sent
   * (see linkEmailIdentity). Carries the same round-trip state as the first
   * send — a resent link that dropped `returnTo` would land an onboarding user
   * at step one with every answer gone.
   */
  sendEmailIdentityVerification: async (email: string, state?: OAuthRedirectState) => {
    const { error } = await supabase.auth.updateUser(
      { email },
      {
        emailRedirectTo: withOAuthState(await getOAuthRedirectUrl(), {
          source: 'link',
          ...state,
        }),
      },
    )

    if (error) {
      // Notably includes over_email_send_rate_limit. Surfacing it is the point:
      // reporting success for a send that did not happen is what left the user
      // waiting for an email that was never going to arrive.
      logger.error('[AuthStore] Failed to send email identity verification:', error)
      throw error
    }
  },

  updatePassword: async (newPassword: string) => {
    try {
      const { error } = await supabase.auth.updateUser({
        password: newPassword,
      })

      if (error) {
        logger.error('[AuthStore] Password update error:', error)
        throw error
      }

      // Refresh the session to ensure everything is in sync
      await get().refreshSession()
    } catch (error) {
      logger.error('[AuthStore] Failed to update password:', error)
      throw error
    }
  },

  setApiKeySession: (apiKey: string) => {
    localStorage.setItem('reliant-api-key', apiKey)
    set({
      user: {
        id: 'apikey-user',
        email: 'apikey@localhost',
        email_confirmed_at: new Date().toISOString(),
        is_anonymous: false,
      } as User,
      session: { access_token: apiKey } as Session,
      loading: false,
      initialized: true,
    })
  },

  initialize: async () => {
    if (get().initialized) return

    // Restore API key session from localStorage
    const storedApiKey = localStorage.getItem('reliant-api-key')
    if (storedApiKey) {
      logger.info('[AuthStore] Restoring API key session')
      set({
        user: {
          id: 'apikey-user',
          email: 'apikey@localhost',
          email_confirmed_at: new Date().toISOString(),
          is_anonymous: false,
        } as User,
        session: { access_token: storedApiKey } as Session,
        loading: false,
        initialized: true,
      })
      return
    }

    // Mock mode: skip auth entirely and set a mock user
    const isMockMode = (window as unknown as { __MOCK_MODE__?: boolean }).__MOCK_MODE__;
    if (isMockMode) {
      set({
        user: { id: 'mock-user', email: 'mock@example.com' } as User,
        session: { access_token: 'mock-token' } as Session,
        loading: false,
        initialized: true,
      })
      return
    }

    set({ loading: true })

    // A permanently unreadable stored session must become a real auth state.
    //
    // Without this the app half-authenticates: the storage adapter can only
    // answer null, so `getSession()` reports no session and `getToken()`
    // returns null, while this store still holds a user from a prior
    // `onAuthStateChange` — so the UI shows signed-in and every RPC goes out
    // with no Authorization header and comes back "missing authorization
    // token". Dropping the stale in-memory session converts that into the
    // honest state: signed out, with a message explaining why.
    setAuthStorageUnreadableHandler(() => {
      logger.warn('[AuthStore] Stored session unreadable; forcing re-authentication')
      set({
        user: null,
        session: null,
        loading: false,
        initialized: true,
        authError:
          'Your saved sign-in could not be read from this device’s secure storage and has been cleared. Please sign in again.',
      })
    })

    try {
      // Supabase will automatically load session from custom storage adapter
      // No need to manually load from file storage anymore
      const { data: { session } } = await supabase.auth.getSession()

      set({
        user: session?.user ?? null,
        session: session,
        loading: false,
        initialized: true,
      })

      // Set up auth state listener to keep store in sync
      supabase.auth.onAuthStateChange(async (_event: AuthChangeEvent, session: Session | null) => {
        set({
          user: session?.user ?? null,
          session: session,
        })

        // Update Sentry user context for error correlation
        setSentryUser(session?.user ? { id: session.user.id, email: session.user.email } : null)

        // The Supabase GitHub provider is sign-in only (0 scopes); we never
        // persist its provider_token. Repo access is owned by the dedicated
        // /auth/github/authorize flow, which writes git_credentials directly.

        // NOTE: Supabase automatically saves session through custom storage adapter
      })

      // OAuth callback listener is registered at initialize start (including dev mode)
    } catch (error) {
      logger.error('Failed to initialize auth:', error)
      set({
        loading: false,
        initialized: true,
      })
    }
  },

  refreshSession: async () => {
    try {
      const { data: { session }, error } = await supabase.auth.refreshSession()

      if (error) throw error

      set({
        user: session?.user ?? null,
        session: session,
      })
    } catch (error) {
      logger.error('Failed to refresh session:', error)
      set({
        user: null,
        session: null,
      })
    }
  },
}))