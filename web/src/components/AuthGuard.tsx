import { useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useNavigate } from '@tanstack/react-router'

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
  const { user, loading, initialized, initialize } = useAuthStore()
  const navigate = useNavigate()

  useEffect(() => {
    if (!initialized) {
      initialize()
    }
  }, [initialized, initialize])

  // Note: Global data prefetch is now handled by AuthInitializer (single instance)
  // to prevent multiple AuthGuard instances from racing

  useEffect(() => {
    if (!initialized || loading) return

    // Check authentication first
    if (requireAuth && !user) {
      navigate({ to: '/auth' })
      return
    }
    
    if (!requireAuth && user) {
      navigate({ to: '/' })
      return
    }

    // Then check email verification (skip for anonymous users)
    if (requireEmailVerification && user && !user.is_anonymous && !user.email_confirmed_at) {
      navigate({ to: '/verify-email' })
      return
    }
  }, [user, loading, initialized, requireAuth, requireEmailVerification, navigate])

  if (!initialized || loading) {
    return null
  }

  if (requireAuth && !user) {
    return null
  }

  if (!requireAuth && user) {
    return null
  }

  // Check email verification requirement (skip for anonymous users)
  if (requireEmailVerification && user && !user.is_anonymous && !user.email_confirmed_at) {
    return null
  }

  return <>{children}</>
}
