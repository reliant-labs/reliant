import { useCallback, useEffect, useRef, useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import { isForgeAuth } from '@/lib/auth/forge-api'
import { useAuth } from '@/lib/auth/use-auth'
import { RouteGuard } from '@/lib/auth/route-guard'
import { isSafeReturnTo } from '@/lib/returnTo'
import { useNavigate, useRouterState, useSearch } from '@tanstack/react-router'
import { useAuthMode } from '@/hooks/useAuthMode'
import { ApiKeyLogin } from './ApiKeyLogin'
import { LoadingSpinner } from './Layout/LoadingSpinner'
import { loadConfig } from '@/lib/config_gen'
import { signInRedirectFor } from '@/lib/returnTo'
import { authPath, homePath, verifyEmailPath } from '@/lib/paths'
import { isForgeShell } from '@/lib/shell'
import { GUARDED_ROUTE_IDS, landingJourneyHoldsSignIn } from './ForgeShell/landingJourney'
import { orgFreeRouteId } from '@/lib/paths/org'

const allowAnonymousAutoSignIn = (): boolean => loadConfig().AUTO_ANON_SIGN_IN === 'true'

interface AuthGuardProps {
  children: React.ReactNode
  requireAuth?: boolean
  requireEmailVerification?: boolean
  /**
   * Send a signed-out visitor to sign in with `?redirect=<this page>`, so a
   * deep link (a shared console link, a link in an email)
   * lands where it was going instead of on `/`. The sign-in screen honours a
   * same-origin `redirect` through email, OAuth and magic links alike.
   */
  returnHere?: boolean
}

/**
 * Gates a route on who is signed in. With forge sign-in (AUTH_PROVIDER
 * "forge") ForgeAuthGuard decides; otherwise SupabaseAuthGuard, unchanged.
 */
export function AuthGuard(props: AuthGuardProps) {
  return isForgeAuth() ? <ForgeAuthGuard {...props} /> : <SupabaseAuthGuard {...props} />
}

/**
 * The forge-mode guard: the route guard's semantics (lib/auth/route-guard.tsx).
 * Public pages are the auth pages; every other page needs an identity, and a
 * signed-out visitor is sent to `/auth` with the way back. There is no
 * anonymous auto-sign-in (forge sign-in has no guest sessions) and no
 * email-confirmation stop: the broker issues no session for an unconfirmed
 * address, so every forge identity is verified. `returnHere` and
 * `requireEmailVerification` are therefore moot: the way back is always kept.
 */
function ForgeAuthGuard({ children, requireAuth = true }: AuthGuardProps) {
  const { identity, loading, initialized } = useAuth()
  const initialize = useAuthStore((s) => s.initialize)
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { redirect?: string } | undefined
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const here = useRouterState({ select: (s) => s.location.href })
  const journeyLayout = useRouterState({ select: (s) => s.matches.some((m) => GUARDED_ROUTE_IDS.includes(orgFreeRouteId(m.routeId))) })
  // As in SupabaseAuthGuard: the forge brand's landing journey may be taking
  // this visitor to the public demo, and sign-in waits rather than racing it.
  const [journeyHoldsSignIn] = useState(() => journeyLayout && requireAuth && isForgeShell() && landingJourneyHoldsSignIn(here))

  useEffect(() => {
    if (!initialized) {
      initialize()
    }
  }, [initialized, initialize])

  // A signed-in visitor on a signed-out page (`/auth`) goes on: to the
  // `redirect` it carries when that is a page of this app, else home.
  useEffect(() => {
    if (requireAuth || !initialized || loading || !identity) return
    const redirect = search?.redirect
    if (isSafeReturnTo(redirect)) {
      window.location.href = redirect
    } else {
      navigate({ to: homePath(), search: {} })
    }
  }, [requireAuth, initialized, loading, identity, search, navigate])

  // The route guard's way to sign-in. Stable, or its effect would navigate
  // again on every render while the visitor is blocked.
  const toSignIn = useCallback(
    (href: string) => {
      if (journeyHoldsSignIn) return
      navigate({ href })
    },
    [navigate, journeyHoldsSignIn],
  )

  // Decided by `identity` alone. Not by useAuthMode: reliant-api's /health
  // reports its bearer mode ("apikey" in dev), which says nothing about the
  // broker's cookie sessions.
  if (!initialized) return <LoadingSpinner />

  if (requireAuth) {
    return (
      <RouteGuard pathname={pathname} navigate={toSignIn}>
        {children}
      </RouteGuard>
    )
  }

  // Signed in on a signed-out page: on the way out (above).
  if (identity) return <LoadingSpinner />

  return <>{children}</>
}

function SupabaseAuthGuard({ 
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
  const journeyLayout = useRouterState({ select: (s) => s.matches.some((m) => GUARDED_ROUTE_IDS.includes(orgFreeRouteId(m.routeId))) })
  const signInRedirect = returnHere ? signInRedirectFor(here) : undefined
  // On the forge brand (lib/shell.ts), a signed-out visitor the landing page
  // sent for the forge journey is taken to the public demo instead
  // (ForgeLandingJourney, at the root): sign-in waits rather than racing that
  // navigation. Decided once: while the journey's navigation is under way the
  // location already carries its `?tour=` and the held choice is spent, and
  // re-deciding then would send the visitor to sign in after all.
  const [journeyHoldsSignIn] = useState(() => journeyLayout && requireAuth && isForgeShell() && landingJourneyHoldsSignIn(here))

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
      // Already on the way: the location moves to the sign-in page before
      // this layout unmounts, and asking again from there would replace the
      // `redirect` back to this page with none.
      if (here.split(/[?#]/, 1)[0] === authPath()) return
      // For apikey mode, don't navigate — AuthGuard renders ApiKeyLogin inline
      if (authMode === 'apikey') return
      // The landing journey is taking this visitor to the demo (above).
      if (journeyHoldsSignIn) return

      // Optional zero-friction onboarding: sign in anonymously when explicitly enabled.
      if (authMode === 'supabase' && allowAnonymousAutoSignIn() && !anonSignInAttempted.current) {
        anonSignInAttempted.current = true
        signInAnonymously().catch(() => {
          navigate({ to: authPath(), search: { redirect: signInRedirect } })
        })
        return
      }

      navigate({ to: authPath(), search: { redirect: signInRedirect } })
      return
    }
    
    if (!requireAuth && user) {
      // If there's a redirect param (e.g. from admin-web), go there instead of home
      if (search?.redirect) {
        window.location.href = search.redirect
      } else {
        navigate({ to: homePath(), search: {} })
      }
      return
    }

    // Then check email verification (skip for anonymous users and apikey mode)
    if (requireEmailVerification && authMode === 'supabase' && user && !user.is_anonymous && !user.email_confirmed_at) {
      navigate({ to: verifyEmailPath() })
      return
    }
  }, [user, loading, initialized, requireAuth, requireEmailVerification, navigate, search, authMode, authModeLoading, signInAnonymously, signInRedirect, journeyHoldsSignIn, here])

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