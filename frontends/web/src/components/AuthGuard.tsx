import { useEffect, useRef } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useNavigate, useRouterState, useSearch } from '@tanstack/react-router'
import { useAuthMode } from '@/hooks/useAuthMode'
import { ApiKeyLogin } from './ApiKeyLogin'
import { LoadingSpinner } from './Layout/LoadingSpinner'
import { loadConfig } from '@/lib/config_gen'
import { signInRedirectFor } from '@/lib/returnTo'

const allowAnonymousAutoSignIn = (): boolean => loadConfig().AUTO_ANON_SIGN_IN === 'true'

interface AuthGuardProps {
  children: React.ReactNode
  requireAuth?: boolean
  requireEmailVerification?: boolean
  /**
   * Send a signed-out visitor to sign in with `?redirect=<this page>`, so a
   * deep link ("Open in Reliant" from the forge console, a link in an email)
   * lands where it was going instead of on `/`. The sign-in screen honours a
   * same-origin `redirect` through email, OAuth and magic links alike.
   */
  returnHere?: boolean
}

export function AuthGuard({ 
  children, 
  requireAuth = true,
  requireEmailVerification = false,
  returnHere = false,
}: AuthGuardProps) {
  const { user, loading, initialized, initialize, signInAnonymously } = useAuthStore()
  const { authMode, loading: authModeLoading } = useAuthMode()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { redirect?: string } | undefined
  const anonSignInAttempted = useRef(false)
  const here = useRouterState({ select: (s) => s.location.href })
  const signInRedirect = returnHere ? signInRedirectFor(here) : undefined

  useEffect(() => {
    if (!initialized) {
      initialize()
    }
  }, [initialized, initialize])

  // Note: Global data prefetch is now handled by AuthInitializer (single instance)
  // to prevent multiple AuthGuard instances from racing

  useEffect(() => {
    if (!initialized || loading || authModeLoading) return

    // Check authentication first
    if (requireAuth && !user) {
      // For apikey mode, don't navigate — AuthGuard renders ApiKeyLogin inline
      if (authMode === 'apikey') return

      // Optional zero-friction onboarding: sign in anonymously when explicitly enabled.
      if (authMode === 'supabase' && allowAnonymousAutoSignIn() && !anonSignInAttempted.current) {
        anonSignInAttempted.current = true
        signInAnonymously().catch(() => {
          navigate({ to: '/auth', search: { redirect: signInRedirect } })
        })
        return
      }

      navigate({ to: '/auth', search: { redirect: signInRedirect } })
      return
    }
    
    if (!requireAuth && user) {
      // If there's a redirect param (e.g. from admin-web), go there instead of home
      if (search?.redirect) {
        window.location.href = search.redirect
      } else {
        navigate({ to: '/', search: {} })
      }
      return
    }

    // Then check email verification (skip for anonymous users and apikey mode)
    if (requireEmailVerification && authMode === 'supabase' && user && !user.is_anonymous && !user.email_confirmed_at) {
      navigate({ to: '/verify-email' })
      return
    }
  }, [user, loading, initialized, requireAuth, requireEmailVerification, navigate, search, authMode, authModeLoading, signInAnonymously, signInRedirect])

  // Auth is still resolving, or an effect above is about to redirect. These are
  // never instant: authMode alone costs a /health round-trip, so returning null
  // renders a blank white page for ~1s and the subsequent redirect reads as
  // "the page just refreshed and nothing happened". Show the same spinner the
  // app boots with instead, so the wait looks like loading rather than a fault.
  if (!initialized || loading || authModeLoading) {
    return <LoadingSpinner />
  }

  if (requireAuth && !user) {
    // Show API key login inline for apikey mode
    if (authMode === 'apikey') return <ApiKeyLogin />
    // Anonymous sign-in is in progress, or we're navigating to /auth.
    return <LoadingSpinner />
  }

  if (!requireAuth && user) {
    return <LoadingSpinner />
  }

  // Check email verification requirement (skip for anonymous users and apikey mode)
  if (requireEmailVerification && authMode === 'supabase' && user && !user.is_anonymous && !user.email_confirmed_at) {
    return null
  }

  return <>{children}</>
}