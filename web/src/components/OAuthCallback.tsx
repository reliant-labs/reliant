import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { supabase } from '@/lib/supabase'
import { useAuthStore } from '@/store/authStore'
import { logger } from '@/lib/logger'
import { githubCredentialSync } from '@/lib/githubCredentialSync'
import { GradientBackground } from './GradientBackground'
import { BrandMark } from './icons/BrandMark'

export function OAuthCallback() {
  const navigate = useNavigate()
  const search = useSearch({ from: '/auth/callback' })
  const { setUser, setSession } = useAuthStore()
  const [error, setError] = useState<string | null>(null)
  const exchanged = useRef(false)

  useEffect(() => {
    if (exchanged.current) return
    exchanged.current = true

    const handleCallback = async () => {
      const { code, error: errorParam, error_description: errorDescription, source, returnTo } = search

      if (errorParam) {
        let friendlyMessage = 'Authentication failed'

        if (errorDescription?.includes('Multiple accounts') || errorDescription?.includes('same email')) {
          friendlyMessage = 'An account with this email already exists. Please sign in with your existing method first, then link your OAuth provider in settings.'
        } else if (errorDescription?.includes('denied') || errorDescription?.includes('cancelled')) {
          friendlyMessage = 'Authorization cancelled. Please try again.'
        } else if (errorDescription) {
          friendlyMessage = errorDescription.replace(/\+/g, ' ')
        }

        logger.error('[OAuthCallback] Error from provider', {
          error: errorParam,
          errorDescription,
        })
        setError(friendlyMessage)
        return
      }

      if (!code) {
        logger.error('[OAuthCallback] Missing authorization code in callback URL')
        setError('Invalid authentication callback. Please try signing in again.')
        return
      }

      try {
        logger.info('[OAuthCallback] Exchanging authorization code for session')
        const { data, error: exchangeError } = await supabase.auth.exchangeCodeForSession(code)

        if (exchangeError) {
          logger.error('[OAuthCallback] Code exchange failed', exchangeError)
          setError(exchangeError.message)
          return
        }

        setUser(data.user)
        setSession(data.session)

        // Capture the transient provider_token before it disappears.
        // This is the primary capture point — exchangeCodeForSession reliably
        // includes provider_token, unlike onAuthStateChange which may not.
        // We AWAIT the sync (with retries) so the credential lands before the
        // user navigates to the main app and any "Reconnect GitHub" gates run.
        // Failures do not block navigation — banner UI surfaces them.
        const provider = data.user?.app_metadata?.provider
        logger.info('[OAuthCallback] post-OAuth session state', {
          provider,
          hasProviderToken: !!data.session?.provider_token,
          userId: data.user?.id,
        })
        if (provider === 'github' && data.session?.provider_token) {
          const trigger: 'signin' | 'link' = source === 'link' ? 'link' : 'signin'
          await githubCredentialSync.sync(data.session.provider_token, 'repo', trigger)
        }

        if (returnTo && returnTo.startsWith('/') && !returnTo.startsWith('//')) {
          // Restore the originating URL (preserves onboarding plan param).
          // Only honor same-origin relative paths to avoid open-redirect issues.
          window.location.assign(returnTo)
          return
        }

        navigate({ to: '/', search: {} })
      } catch (err) {
        logger.error('[OAuthCallback] Unexpected callback error', err)
        setError(err instanceof Error ? err.message : 'Authentication failed')
      }
    }

    void handleCallback()
  }, [navigate, search, setSession, setUser])

  if (error) {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <BrandMark className="h-8 w-8" />
              <h2 className="text-xl font-semibold text-destructive">Authentication Failed</h2>
            </div>
            <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
              <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
            </div>
            <button
              onClick={() => navigate({ to: '/auth', search: { redirect: undefined } })}
              className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 transition-colors"
            >
              Back to Sign In
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
      <GradientBackground />
      <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8">
          <div className="flex flex-col items-center gap-4">
            <BrandMark className="h-8 w-8" />
            <h2 className="text-lg font-medium">Completing sign in...</h2>
            <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
          </div>
        </div>
      </div>
    </div>
  )
}