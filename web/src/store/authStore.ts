import { create } from 'zustand'
import type { User, Session, AuthChangeEvent } from '@supabase/supabase-js'
import { supabase } from '@/lib/supabase'
import { logger } from '@/lib/logger'
import { setSentryUser } from '@/lib/sentry'
import { devAuthGrpc } from '@/api/grpc-unauth'

const isElectron = !!window.electronAPI
const getOAuthRedirectUrl = async (): Promise<string> => {
  if (!isElectron || !window.electronAPI?.getOAuthRedirectUrl) {
    return `${window.location.origin}/auth/callback`
  }

  const response = await window.electronAPI.getOAuthRedirectUrl()
  if (!response?.success || !response.redirectUrl) {
    throw new Error(response?.error || 'Failed to initialize OAuth callback listener')
  }

  return response.redirectUrl
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

let oauthCallbackListenerRegistered = false

const setupElectronOAuthCallbackListener = (setState: (state: Partial<AuthState>) => void) => {
  if (!isElectron || !window.electronAPI?.onOAuthCallback || oauthCallbackListenerRegistered) {
    return
  }

  oauthCallbackListenerRegistered = true

  window.electronAPI.onOAuthCallback(async (callbackUrl: string) => {
    logger.info('[AuthStore] OAuth callback received:', callbackUrl)
    try {
      // Parse callback URL
      const url = new URL(callbackUrl)

      // Check for error in URL params
      const errorParam = url.searchParams.get('error')
      const errorDescription = url.searchParams.get('error_description')
      if (errorParam) {
        logger.error('[AuthStore] OAuth error in callback:', errorParam, errorDescription)
        if (errorDescription?.includes('already linked') ||
            errorDescription?.includes('identity already exists') ||
            errorParam === 'identity_already_exists') {
          logger.warn('[AuthStore] Identity already exists on another account')
          setState({ authError: 'This account is already linked to an existing user. Please sign out and sign in with that account instead.' })
        } else {
          setState({ authError: errorDescription || errorParam })
        }
        throw new Error(errorDescription || errorParam)
      }

      // Check if it's a PKCE flow (with code parameter)
      const code = url.searchParams.get('code')
      logger.info('[AuthStore] OAuth callback params:', {
        hasCode: !!code,
        hasHash: !!url.hash,
        searchParams: url.search
      })

      if (code) {
        logger.info('[AuthStore] Exchanging code for session...')
        const { data, error } = await supabase.auth.exchangeCodeForSession(code)

        if (error) {
          logger.error('[AuthStore] OAuth code exchange error:', error)
          logger.error('[AuthStore] Error details:', {
            code: error.code,
            message: error.message,
            status: error.status,
          })
          throw error
        }

        logger.info('[AuthStore] Code exchange successful:', {
          userId: data.user?.id,
          email: data.user?.email,
          identities: data.user?.identities?.map(i => i.provider),
        })

        // The Supabase GitHub provider is sign-in only (0 scopes) — its
        // provider_token has no useful scopes and expires in 8h with no
        // refresh token. Repo access goes through the dedicated
        // /auth/github/authorize flow, which writes the long-lived token
        // directly to git_credentials. See ../OnboardingFlow/steps/
        // GitHubConnectStep.tsx and Settings/GitConnectionsSettings.tsx.

        setState({
          user: data.user,
          session: data.session,
        })

        await trackAuthFunnelEvent('oauth_succeeded', {
          auth_method: data.user?.app_metadata?.provider || data.user?.identities?.[0]?.provider || 'oauth',
          oauth_callback_transport: isElectron ? 'localhost' : 'web',
        })
      } else {
        logger.error('[AuthStore] Invalid OAuth callback URL format - no code')
      }
    } catch (error) {
      logger.error('[AuthStore] Failed to handle OAuth callback:', error)
    }
  })
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
  signUp: (email: string, password: string) => Promise<{ user: User | null; session: Session | null }>
  signInWithGoogle: () => Promise<void>
  signInWithGithub: (state?: OAuthRedirectState) => Promise<void>
  signInWithApple: () => Promise<void>
  linkGoogleAccount: () => Promise<void>
  linkAppleAccount: () => Promise<void>
  unlinkIdentity: (identityId: string) => Promise<void>
  sendPasswordResetOTP: (email: string) => Promise<void>
  verifyPasswordResetOTP: (email: string, code: string) => Promise<void>
  updatePassword: (newPassword: string) => Promise<void>
  sendEmailVerificationOTP: (emailOverride?: string) => Promise<void>
  verifyEmailOTP: (code: string, emailOverride?: string) => Promise<void>
  signInAnonymously: () => Promise<void>
  signOut: () => Promise<void>
  setApiKeySession: (apiKey: string) => void

  initialize: () => Promise<void>
  refreshSession: () => Promise<void>
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

  signUp: async (email: string, password: string) => {
    await trackAuthFunnelEvent('signup_attempted', {
      auth_method: 'password',
    })

    // Don't set loading state here to avoid remounting components
    const { data, error } = await supabase.auth.signUp({
      email,
      password,
      options: {
        // For Electron apps, we don't want to redirect after email confirmation
        // The email link will just confirm the email without opening the app
        emailRedirectTo: undefined,
      }
    })

    if (error) throw error

    // With email confirmation enabled, signup commonly returns user + null session.
    // We intentionally do NOT infer "email already in use" from this response shape,
    // because Supabase can return null session for legitimate new signups as well.

    set({
      user: data.user,
      session: data.session,
    })

    if (data.session && data.user) {
      await trackAuthFunnelEvent('signup_succeeded', {
        auth_method: 'password',
      })
    }

    return { user: data.user, session: data.session }
  },

  signInWithGoogle: async () => {
    set({ loading: true })
    const startedAt = Date.now()
    await trackAuthFunnelEvent('oauth_started', {
      auth_method: 'google',
      oauth_callback_transport: isElectron ? 'localhost' : 'web',
    })

    try {
      if (isElectron) {
        const oauthSession = await devAuthGrpc.startOAuthSignIn('google')
        const { data, error } = await supabase.auth.setSession({
          access_token: oauthSession.accessToken,
          refresh_token: oauthSession.refreshToken,
        })

        if (error) {
          logger.error('[AuthStore] Electron Google session hydration failed:', error)
          set({ loading: false })
          throw error
        }

        set({
          user: data.user,
          session: data.session,
          loading: false,
        })

        await trackAuthFunnelEvent('oauth_succeeded', {
          auth_method: 'google',
          oauth_callback_transport: 'localhost',
        })
        return
      }

      const redirectTo = await getOAuthRedirectUrl()
      const { data, error } = await supabase.auth.signInWithOAuth({
        provider: 'google',
        options: {
          redirectTo,
          skipBrowserRedirect: true,
        },
      })

      if (error) {
        logger.error('[AuthStore] OAuth error:', error)
        set({ loading: false })
        throw error
      }

      if (data?.url) {
        window.location.href = data.url
      } else {
        logger.error('[AuthStore] No OAuth URL returned from Supabase')
      }

      set({ loading: false })
    } catch (error) {
      await trackAuthFunnelEvent('oauth_failed', {
        auth_method: 'google',
        oauth_callback_transport: isElectron ? 'localhost' : 'web',
        failure_reason: normalizeFailureReason(error),
        latency_ms: Date.now() - startedAt,
      })
      logger.error('[AuthStore] Catch block error:', error)
      set({ loading: false })
      throw error
    }
  },

  signInWithGithub: async (state?: OAuthRedirectState) => {
    set({ loading: true })
    const startedAt = Date.now()
    await trackAuthFunnelEvent('oauth_started', {
      auth_method: 'github',
      oauth_callback_transport: isElectron ? 'localhost' : 'web',
    })

    try {
      if (isElectron) {
        const oauthSession = await devAuthGrpc.startOAuthSignIn('github')
        const { data, error } = await supabase.auth.setSession({
          access_token: oauthSession.accessToken,
          refresh_token: oauthSession.refreshToken,
        })

        if (error) {
          logger.error('[AuthStore] Electron GitHub session hydration failed:', error)
          set({ loading: false })
          throw error
        }

        // The Supabase GitHub provider is sign-in only (0 scopes). We never
        // persist the provider_token here — repo access comes from the
        // dedicated /auth/github/authorize custom flow.

        set({
          user: data.user,
          session: data.session,
          loading: false,
        })

        await trackAuthFunnelEvent('oauth_succeeded', {
          auth_method: 'github',
          oauth_callback_transport: 'localhost',
        })
        return
      }

      const redirectTo = withOAuthState(await getOAuthRedirectUrl(), state ?? {})
      const { data, error } = await supabase.auth.signInWithOAuth({
        provider: 'github',
        options: {
          redirectTo,
          skipBrowserRedirect: true,
        },
      })

      if (error) {
        logger.error('[AuthStore] OAuth error:', error)
        set({ loading: false })
        throw error
      }

      if (data?.url) {
        window.location.href = data.url
      } else {
        logger.error('[AuthStore] No OAuth URL returned from Supabase')
      }

      set({ loading: false })
    } catch (error) {
      await trackAuthFunnelEvent('oauth_failed', {
        auth_method: 'github',
        oauth_callback_transport: isElectron ? 'localhost' : 'web',
        failure_reason: normalizeFailureReason(error),
        latency_ms: Date.now() - startedAt,
      })
      logger.error('[AuthStore] Catch block error:', error)
      set({ loading: false })
      throw error
    }
  },

  signInWithApple: async () => {
    set({ loading: true })
    const startedAt = Date.now()
    await trackAuthFunnelEvent('oauth_started', {
      auth_method: 'apple',
      oauth_callback_transport: isElectron ? 'localhost' : 'web',
    })

    try {
      if (isElectron) {
        const oauthSession = await devAuthGrpc.startOAuthSignIn('apple')
        const { data, error } = await supabase.auth.setSession({
          access_token: oauthSession.accessToken,
          refresh_token: oauthSession.refreshToken,
        })

        if (error) {
          logger.error('[AuthStore] Electron Apple session hydration failed:', error)
          set({ loading: false })
          throw error
        }

        set({
          user: data.user,
          session: data.session,
          loading: false,
        })

        await trackAuthFunnelEvent('oauth_succeeded', {
          auth_method: 'apple',
          oauth_callback_transport: 'localhost',
        })
        return
      }

      const redirectTo = await getOAuthRedirectUrl()
      const { data, error } = await supabase.auth.signInWithOAuth({
        provider: 'apple',
        options: {
          redirectTo,
          skipBrowserRedirect: true,
        },
      })

      if (error) {
        logger.error('[AuthStore] OAuth error:', error)
        set({ loading: false })
        throw error
      }

      if (data?.url) {
        window.location.href = data.url
      } else {
        logger.error('[AuthStore] No OAuth URL returned from Supabase')
      }

      set({ loading: false })
    } catch (error) {
      await trackAuthFunnelEvent('oauth_failed', {
        auth_method: 'apple',
        oauth_callback_transport: isElectron ? 'localhost' : 'web',
        failure_reason: normalizeFailureReason(error),
        latency_ms: Date.now() - startedAt,
      })
      logger.error('[AuthStore] Catch block error:', error)
      set({ loading: false })
      throw error
    }
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

  linkGoogleAccount: async () => {
    set({ loading: true })
    try {
      const redirectTo = await getOAuthRedirectUrl()
      logger.info('[AuthStore] linkGoogleAccount: Starting link flow', {
        isElectron,
        redirectTo,
      })

      const { data, error } = await supabase.auth.linkIdentity({
        provider: 'google',
        options: {
          redirectTo,
          skipBrowserRedirect: true,
        },
      })

      logger.info('[AuthStore] linkGoogleAccount: linkIdentity response', {
        hasData: !!data,
        hasUrl: !!data?.url,
        urlPreview: data?.url?.substring(0, 100),
        error: error?.message,
        errorCode: error?.code,
      })

      if (error) {
        // Check if identity already exists on another account
        if (error.message?.includes('already linked') ||
            error.message?.includes('identity already exists') ||
            error.code === 'identity_already_exists') {
          throw new Error('This Google account is already linked to an existing account. Please sign out and sign in with Google instead.')
        }
        throw error
      }

      if (isElectron && data?.url && window.electronAPI) {
        logger.info('[AuthStore] linkGoogleAccount: Opening external browser')
        await window.electronAPI.openExternal(data.url)
        logger.info('[AuthStore] linkGoogleAccount: External browser opened')
      } else if (data?.url) {
        window.location.href = data.url
      } else {
        logger.error('[AuthStore] linkGoogleAccount: No link URL returned from Supabase')
      }

      set({ loading: false })
    } catch (error) {
      logger.error('[AuthStore] linkGoogleAccount: Error:', error)
      set({ loading: false })
      throw error
    }
  },

  linkAppleAccount: async () => {
    set({ loading: true })
    try {
      const redirectTo = await getOAuthRedirectUrl()
      const { data, error } = await supabase.auth.linkIdentity({
        provider: 'apple',
        options: {
          redirectTo,
          skipBrowserRedirect: true,
        },
      })

      if (error) throw error

      if (isElectron && data?.url && window.electronAPI) {
        await window.electronAPI.openExternal(data.url)
      } else if (data?.url) {
        window.location.href = data.url
      } else {
        logger.error('[AuthStore] linkAppleAccount: No link URL returned from Supabase')
      }

      set({ loading: false })
    } catch (error) {
      set({ loading: false })
      throw error
    }
  },

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

  sendEmailVerificationOTP: async (emailOverride?: string) => {
    try {
      const { user } = get()

      const emailToUse = emailOverride ?? user?.email

      if (!emailToUse) {
        throw new Error('No user email found')
      }

      if (user?.email_confirmed_at) {
        throw new Error('Email already verified')
      }

      const { error } = await supabase.auth.resend({
        type: 'signup',
        email: emailToUse,
      })

      if (error) {
        logger.error('[AuthStore] Failed to send verification OTP:', error)
        throw error
      }
    } catch (error) {
      logger.error('[AuthStore] sendEmailVerificationOTP error:', error)
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

    // Always register OAuth callback listener in Electron, including dev mode.
    setupElectronOAuthCallbackListener(set)

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