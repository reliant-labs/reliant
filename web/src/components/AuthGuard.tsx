import { useEffect, useRef } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useAuthMode } from '@/hooks/useAuthMode'
import { ApiKeyLogin } from './ApiKeyLogin'

const allowAnonymousAutoSignIn = import.meta.env.VITE_AUTO_ANON_SIGN_IN === 'true'

interface AuthGuardProps {
  children: React.ReactNode
  requireAuth?: boolean
  requireEmailVerification?: boolean
}

export function AuthGuard({ 
  children, 
  requireAuth = true,
  requireEmailVerification = false 
}: AuthGuardProps) {
  const { user, loading, initialized, initialize, signInAnonymously } = useAuthStore()
  const { authMode, loading: authModeLoading } = useAuthMode()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { redirect?: string } | undefined
  const anonSignInAttempted = useRef(false)

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
      if (authMode === 'supabase' && allowAnonymousAutoSignIn && !anonSignInAttempted.current) {
        anonSignInAttempted.current = true
        signInAnonymously().catch(() => {
          navigate({ to: '/auth', search: { redirect: undefined } })
        })
        return
      }

      navigate({ to: '/auth', search: { redirect: undefined } })
      return
    }
    
    if (!requireAuth && user) {
      // If there's a redirect param (e.g. from admin-web), go there instead of home
      if (search?.redirect) {
        window.location.href = search.redirect
      } else {
        navigate({ to: '/' })
      }
      return
    }

    // Then check email verification (skip for anonymous users and apikey mode)
    if (requireEmailVerification && authMode === 'supabase' && user && !user.is_anonymous && !user.email_confirmed_at) {
      navigate({ to: '/verify-email' })
      return
    }
  }, [user, loading, initialized, requireAuth, requireEmailVerification, navigate, search, authMode, authModeLoading, signInAnonymously])

  if (!initialized || loading || authModeLoading) {
    return null
  }

  if (requireAuth && !user) {
    // Show API key login inline for apikey mode
    if (authMode === 'apikey') return <ApiKeyLogin />
    // Show nothing while anonymous sign-in is in progress
    return null
  }

  if (!requireAuth && user) {
    return null
  }

  // Check email verification requirement (skip for anonymous users and apikey mode)
  if (requireEmailVerification && authMode === 'supabase' && user && !user.is_anonymous && !user.email_confirmed_at) {
    return null
  }

  return <>{children}</>
}