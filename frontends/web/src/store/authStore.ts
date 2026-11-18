import { create } from 'zustand'
import type { User, Session, AuthChangeEvent } from '@supabase/supabase-js'
import { supabase, setAuthStorageUnreadableHandler } from '@/lib/supabase'
import { logger } from '@/lib/logger'
import { getAppURL } from '@/lib/constants'
import { setTelemetryUserId } from '@/lib/telemetry'
import { trackEvent } from '@/lib/analytics'
import { queryClient } from '@/lib/query-client'
import { setSessionOwner } from '@/lib/auth/sessionOwner'
import { getReaderSession, setReaderPrincipal } from '@/components/Forge/AuditedAccess/readerSession'
import {
  startOAuthSignIn,
  oauthCallbackTransport,
  type OAuthProvider,
} from '@/lib/oauth-signin'

const isElectron = !!window.electronAPI

/**
 * Drops everything held for the signed-in user: the user-owned stores, the
 * query cache and the cached identity answers.
 *
 * Runs on EVERY identity transition, not only this tab's signOut(): with the
 * shared session cookie (lib/auth/sharedSession.ts) a sign-out or a sign-in as
 * another user on forge.* reaches app.* only as an auth event, and adopting
 * the next user's session over the previous user's chats, projects and cached
 * answers would show one tenant's data to another. Idempotent. Only
 * commitIdentity calls it, inside the serialized transition queue.
 */
const clearUserScopedState = async (): Promise<void> => {
  // Clear all store data
  // Import these dynamically to avoid circular dependencies
  const { useChatStore } = await import('./chatStore')
  const { useProjectStore } = await import('./projectStore')
  const { useWorktreeStore } = await import('./worktreeStore')
  const { resetWorktreeAnnouncements } = await import('../hooks/worktree-queries')
  const { useChatNavigationStore } = await import('./chatNavigationStore')
  const { useAttachmentStore } = await import('./attachmentStore')

  // Reset all stores (order matters: navigation first to trigger chat cleanup, then everything else)
  useChatNavigationStore.getState().reset()
  useChatStore.getState().reset()
  useProjectStore.getState().reset()
  useWorktreeStore.getState().reset()
  // Workspace rows are in the query cache (cleared below); forget which
  // creates this session was waiting on.
  resetWorktreeAnnouncements()
  useAttachmentStore.getState().reset()

  // The per-account singletons AuthInitializer fills on sign-in: the model
  // list (which carries the user's local daemons, machine names and
  // endpoints), the API-key check and the onboarding checklist. AuthInitializer
  // re-runs its startup for the next user id; these must be empty by then.
  const { useGlobalDataStore } = await import('./globalDataStore')
  const { useApiKeySetupStore } = await import('./apiKeySetupStore')
  const { useOnboardingChecklistStore } = await import('./onboardingChecklistStore')
  useGlobalDataStore.getState().reset()
  useApiKeySetupStore.getState().reset()
  useOnboardingChecklistStore.getState().reset()
  // The product tour's progress and its debounced save: left alive, a save
  // queued for this user would write their progress into the next user's
  // settings, and a load in flight would install this user's journeys.
  const { useTourStore } = await import('./tourStore')
  useTourStore.getState().reset()
  // The settings cache the checklist (and every readSetting) is served from;
  // left initialized it would answer the next user with this one's settings.
  const { settingsSync } = await import('@/services/settingsSync')
  settingsSync.reset()

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
  await queryClient.cancelQueries()
  queryClient.clear()
}

// The user whose chats, projects and cached answers this page may still hold:
// the last identity installed with the previous user's state cleared first.
// Tracked apart from the store's `user`, which several paths (a failed
// refresh, an unreadable stored session) set to null WITHOUT clearing that
// state, so "the store is signed out" never means "nothing is left to clear".
let scopedStateUserId: string | null = null

const CLEANUP_FAILED_MESSAGE =
  'Your previous session could not be cleared from this page. Please reload the page to continue.'

type SetAuthState = (partial: Partial<AuthState>) => void

type AdoptOutcome = 'installed' | 'refused' | 'superseded'

// The number of adoptions ever requested, and the last one in line: each
// commit waits for the one before it.
let adoptionSeq = 0
let adoptionTail: Promise<AdoptOutcome> = Promise.resolve('installed')

/**
 * The one way a user and session are installed in the store: every sign-in,
 * refresh, verification and auth event goes through it.
 *
 * A different identity from the one whose state the page holds (signed out,
 * or another user signed in, often on another host sharing the session
 * cookie) clears that state BEFORE the next one is installed. Fail closed: if
 * the cleanup fails, NO identity is installed, the store goes signed out with
 * an authError, and the cleanup stays owed, so every later adoption (including
 * one for the same next user) retries it first. Returns whether the identity
 * was installed; a caller that reports success to the user must check it.
 */
const adoptIdentity = async (
  set: SetAuthState,
  user: User | null,
  session: Session | null,
  // Clear the user-scoped state even when the page records nobody's: this
  // tab's own signOut(), which must leave nothing behind whatever the store
  // last believed. Ignored for a sign-in, whose clearing follows the rule above.
  forceClear = false,
): Promise<boolean> => {
  // Transitions run one at a time, in the order they were requested, and one
  // requested later supersedes any still waiting: adopting B awaits a cleanup,
  // and an event for C arriving meanwhile must not be overwritten by B
  // finishing second. A superseded call answers with the outcome of the
  // transition that replaced it, which is the identity the page now holds.
  // Audited reader authority stops the moment another identity is requested,
  // not when its turn comes: the cleanup below awaits, and meanwhile requests
  // already carry the next identity's token (it is in supabase's storage), so
  // reading on as the previous principal would pair A's view with B's
  // credential. The store subscription installs the next principal once the
  // identity is.
  const nextReader = user && !user.is_anonymous ? user.id : null
  if (getReaderSession().principalId !== nextReader) setReaderPrincipal(null)
  const ticket = ++adoptionSeq
  const run = adoptionTail
    .then(() => commitIdentity(set, user, session, ticket, forceClear))
    .catch((): AdoptOutcome => 'refused')
  adoptionTail = run
  let outcome = await run
  while (outcome === 'superseded') outcome = await adoptionTail
  return outcome === 'installed'
}

const commitIdentity = async (
  set: SetAuthState,
  user: User | null,
  session: Session | null,
  ticket: number,
  forceClear: boolean,
): Promise<AdoptOutcome> => {
  if (ticket !== adoptionSeq) return 'superseded'
  const nextUserId = user?.id ?? null
  if (
    (scopedStateUserId !== null && scopedStateUserId !== nextUserId) ||
    (forceClear && nextUserId === null)
  ) {
    // From here on the page acts for nobody: writes queued for the previous
    // user that are still waiting to send are dropped (lib/auth/sessionOwner).
    setSessionOwner(null)
    try {
      await clearUserScopedState()
    } catch (error) {
      logger.error('[AuthStore] Clearing the previous user\'s state failed; staying signed out:', error)
      set({ user: null, session: null, authError: CLEANUP_FAILED_MESSAGE })
      setTelemetryUserId(null)
      return 'refused'
    }
    // The page now holds nobody's state, whichever identity is installed next.
    scopedStateUserId = null
  }
  if (ticket !== adoptionSeq) return 'superseded'
  scopedStateUserId = nextUserId
  setSessionOwner(nextUserId)
  set({ user, session })
  return 'installed'
}

// What the auth listener last saw supabase's storage hold, and how many
// events it has seen. The listener's events arrive in storage order; a store
// action's return value does not: supabase emits the action's own event before
// returning, so any event seen after that is newer than what the action holds.
let observedEvents = 0
let observedIdentity: { user: User | null; session: Session | null } | null = null

/** A mark taken when a store action (or the /auth/callback exchange) starts. */
export const identityActionMark = (): number => observedEvents

/**
 * adoptIdentity for the session a supabase call returned. If, since `mark`,
 * the listener saw storage move to a different user (another host signed in
 * or out through the shared cookie while the call was in flight), the
 * returned identity is already stale: the latest one seen is adopted instead,
 * so an action finishing late never reinstalls an identity storage has left.
 */
const adoptReturnedIdentity = (
  set: SetAuthState,
  user: User | null,
  session: Session | null,
  mark: number,
  forceClear = false,
): Promise<boolean> => {
  if (
    observedEvents > mark &&
    observedIdentity !== null &&
    (observedIdentity.user?.id ?? null) !== (user?.id ?? null)
  ) {
    logger.info('[AuthStore] Session changed while the call was in flight; adopting the newer identity')
    return adoptIdentity(set, observedIdentity.user, observedIdentity.session)
  }
  return adoptIdentity(set, user, session, forceClear)
}

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

// The pre-auth funnel, on every platform, through the same product-event pipe
// as the rest of the app.
const trackAuthFunnelEvent = (eventName: string, metadata: Record<string, string | number | boolean> = {}): void => {
  trackEvent(eventName, metadata)
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
  signInWithGoogle: (state?: OAuthRedirectState) => Promise<void>
  signInWithGithub: (state?: OAuthRedirectState) => Promise<void>
  signInWithApple: (state?: OAuthRedirectState) => Promise<void>
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
  adoptSession: (tokens: { accessToken: string; refreshToken: string }) => Promise<User>
  // Install a session supabase already holds (an /auth/callback exchange),
  // clearing the previous user's state first; false when that failed and
  // nothing was installed (authError says why). `mark` is identityActionMark()
  // taken before the exchange began: a different user that storage moved to
  // meanwhile is adopted instead of the exchange's stale result.
  installIdentity: (user: User | null, session: Session | null, mark?: number) => Promise<boolean>
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

  trackAuthFunnelEvent('oauth_started', {
    auth_method: provider,
    oauth_callback_transport: transport,
  })

  try {
    await startOAuthSignIn(provider, state)
  } catch (error) {
    trackAuthFunnelEvent('oauth_failed', {
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
    const mark = identityActionMark()
    const startedAt = Date.now()
    trackAuthFunnelEvent('login_attempted', {
      auth_method: 'password',
    })

    // Don't set loading state here to avoid remounting components
    const { data, error } = await supabase.auth.signInWithPassword({
      email,
      password,
    })

    if (error) {
      trackAuthFunnelEvent('login_failed', {
        auth_method: 'password',
        failure_reason: normalizeFailureReason(error),
        latency_ms: Date.now() - startedAt,
      })
      throw error
    }

    if (!(await adoptReturnedIdentity(set, data.user, data.session, mark))) {
      throw new Error(CLEANUP_FAILED_MESSAGE)
    }
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
  signInWithGoogle: async (state?: OAuthRedirectState) => {
    await runOAuthSignIn('google', state)
  },

  signInWithGithub: async (state?: OAuthRedirectState) => {
    // Whether the provider_token is kept depends on which GitHub app backs the
    // Supabase provider: a GitHub App user token (`ghu_`) is persisted by
    // OAuthCallback via githubCredentialSync, a classic sign-in-only token
    // (`gho_`, 0 scopes) is not, and repo access then comes from the dedicated
    // /auth/github/authorize connect flow.
    await runOAuthSignIn('github', state)
  },

  signInWithApple: async (state?: OAuthRedirectState) => {
    await runOAuthSignIn('apple', state)
  },


  signInAnonymously: async () => {
    const mark = identityActionMark()
    trackAuthFunnelEvent('login_attempted', {
      auth_method: 'anonymous',
    })

    const { data, error } = await supabase.auth.signInAnonymously()

    if (error) {
      trackAuthFunnelEvent('login_failed', {
        auth_method: 'anonymous',
        failure_reason: normalizeFailureReason(error),
      })
      throw error
    }

    if (!(await adoptReturnedIdentity(set, data.user, data.session, mark))) {
      throw new Error(CLEANUP_FAILED_MESSAGE)
    }

    trackAuthFunnelEvent('login_succeeded', {
      auth_method: 'anonymous',
    })
  },

  // Install a session the control plane released to this browser (the /claim
  // page: OpenClaimSession hands over the anonymous user that deployed a site).
  // Unlike `setSession` above, which only writes this store, this goes through
  // supabase so the session is persisted and refreshed like any other, and
  // every RPC after it carries the user's token. The tokens are never logged.
  adoptSession: async ({ accessToken, refreshToken }) => {
    const mark = identityActionMark()
    const { data, error } = await supabase.auth.setSession({
      access_token: accessToken,
      refresh_token: refreshToken,
    })
    if (error) {
      logger.error('[AuthStore] adoptSession failed', { code: (error as { code?: string }).code })
      throw error
    }
    if (!data.user || !data.session) {
      throw new Error('The released session could not be installed')
    }
    if (!(await adoptReturnedIdentity(set, data.user, data.session, mark))) {
      throw new Error(CLEANUP_FAILED_MESSAGE)
    }
    return data.user
  },

  installIdentity: (user: User | null, session: Session | null, mark?: number) =>
    mark === undefined
      ? adoptIdentity(set, user, session)
      : adoptReturnedIdentity(set, user, session, mark),

  signOut: async () => {
    const mark = identityActionMark()
    // Stop reader authority before any network/cleanup awaits, even if logout fails.
    set({ user: null, session: null, loading: true })
    try {
      // Clear API key if present
      localStorage.removeItem('reliant-api-key')

      const { error } = await supabase.auth.signOut()
      if (error) throw error

      // NOTE: supabase.auth.signOut() will automatically call our storage adapter's
      // removeItem() which clears the auth file. No need for manual authClear().

      // The cleanup runs inside the serialized transition, never before it,
      // and against the mark taken before the sign-out began: a sign-in that
      // reached this page meanwhile (another host, through the shared session
      // cookie) is what gets adopted, and its stores are not reset under it.
      if (!(await adoptReturnedIdentity(set, null, null, mark, true))) {
        throw new Error(CLEANUP_FAILED_MESSAGE)
      }
      set({ loading: false })
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
    trackAuthFunnelEvent('login_attempted', { auth_method: 'email_otp' })

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
      trackAuthFunnelEvent('login_failed', {
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
    const mark = identityActionMark()
    const { data, error } = await supabase.auth.verifyOtp({
      email,
      token: code,
      type: 'email',
    })

    if (error) {
      trackAuthFunnelEvent('login_failed', {
        auth_method: 'email_otp',
        failure_reason: normalizeFailureReason(error),
      })
      logger.error('[AuthStore] Email sign-in code verification failed:', error)
      throw error
    }

    if (!(await adoptReturnedIdentity(set, data.user, data.session, mark))) {
      throw new Error(CLEANUP_FAILED_MESSAGE)
    }
    trackAuthFunnelEvent('login_succeeded', { auth_method: 'email_otp' })
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
    const mark = identityActionMark()
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
      if (!(await adoptReturnedIdentity(set, data.user, data.session, mark))) {
        throw new Error(CLEANUP_FAILED_MESSAGE)
      }
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
    const mark = identityActionMark()
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
        if (!(await adoptReturnedIdentity(set, data.user ?? null, data.session ?? null, mark))) {
          throw new Error(CLEANUP_FAILED_MESSAGE)
        }
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
    const mark = identityActionMark()
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
      if (!(await adoptReturnedIdentity(set, data.user ?? null, data.session ?? null, mark))) {
        throw new Error(CLEANUP_FAILED_MESSAGE)
      }
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
    // API-key sessions share a synthetic id: explicit replacement must still
    // invalidate reader authority without putting the credential in a key.
    if (get().session?.access_token !== apiKey) setReaderPrincipal(null, true)
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

      await adoptIdentity(set, session?.user ?? null, session)
      set({ loading: false, initialized: true })

      // Set up auth state listener to keep store in sync. A different
      // identity (signed out, or another user signed in on another host
      // sharing the session cookie) clears the previous user's state first,
      // and installs nothing if that fails: see adoptIdentity.
      supabase.auth.onAuthStateChange(async (_event: AuthChangeEvent, session: Session | null) => {
        observedEvents++
        observedIdentity = { user: session?.user ?? null, session }
        if (!(await adoptIdentity(set, session?.user ?? null, session))) return

        // Attach the user to telemetry for error correlation
        setTelemetryUserId(session?.user ? { id: session.user.id } : null)

        // provider_token is deliberately not persisted here: this listener
        // also fires on every token refresh and tab focus, when the provider
        // token is gone or stale. The one-shot capture lives in OAuthCallback,
        // which sees the session straight after the sign-in exchange.

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
    const mark = identityActionMark()
    try {
      const { data: { session }, error } = await supabase.auth.refreshSession()

      if (error) throw error

      await adoptReturnedIdentity(set, session?.user ?? null, session, mark)
    } catch (error) {
      logger.error('Failed to refresh session:', error)
      set({
        user: null,
        session: null,
      })
    }
  },
}))

// One synchronous boundary covers every writer: explicit sign-in/out, adoptSession,
// auth events, restoration, refresh failure and setUser. Token refresh for the same
// identity is NOT a new reader. The neutral consumer never imports real auth.
const readerId = (state: Pick<AuthState, 'user' | 'session'>) => {
  // Split setter writes may temporarily disagree: fail closed until user and
  // session are aligned, rather than reading with A's view and B's credential.
  if (!state.session) return null
  if (state.session.user?.id && state.session.user.id !== state.user?.id) return null
  return state.user?.is_anonymous ? null : state.user?.id ?? null
}
setReaderPrincipal(readerId(useAuthStore.getState()))
useAuthStore.subscribe((state, previous) => {
  if (readerId(state) !== readerId(previous) || readerId(state) === 'apikey-user') setReaderPrincipal(readerId(state))
})