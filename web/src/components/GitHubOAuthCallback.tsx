import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { gitService } from '@/services/controlPlane/git'
import { logger } from '@/lib/logger'
import { GradientBackground } from './GradientBackground'
import { BrandMark } from './icons/BrandMark'

/**
 * GitHubOAuthCallback owns the app-side `/auth/github/callback` route — the
 * landing page GitHub redirects to after the user authorizes the Reliant OAuth
 * app (the connect flow that writes git_credentials, NOT Supabase sign-in,
 * which lives at /auth/callback).
 *
 * Why the app owns this route: the control-plane still exposes a
 * GET /auth/github/callback handler that exchanges + redirects, and in dev that
 * worked because Vite proxied the path to the backend. On prod (Firebase) the
 * hosting layer only does SPA-rewrites and CANNOT proxy to the GKE backend, so
 * the callback URL must resolve to an app route. This component is that route:
 * it reads `code` + signed `state`, calls the ExchangeGithubOAuthCode RPC
 * (same-origin in dev via the /controlplane.v1. proxy, CORS-clean in prod), and
 * on success hops to the decoded returnTo with `?github_connected=true` so the
 * existing listeners (githubCredentialSync, ModernApp, OnboardingRoute) react
 * exactly as they did to the old backend redirect.
 */
export function GitHubOAuthCallback() {
  const navigate = useNavigate()
  const search = useSearch({ from: '/auth/github/callback' })
  const [error, setError] = useState<string | null>(null)
  const exchanged = useRef(false)

  useEffect(() => {
    if (exchanged.current) return
    exchanged.current = true

    const run = async () => {
      const { code, state, error: errorParam, error_description: errorDescription } = search

      // GitHub denied / errored before we ever get a code.
      if (errorParam) {
        logger.error('[GitHubOAuthCallback] Error from GitHub', { error: errorParam, errorDescription })
        landBackHome('github_error', errorParam, errorDescription)
        return
      }

      if (!code || !state) {
        logger.error('[GitHubOAuthCallback] Missing code or state in callback URL')
        setError('Invalid GitHub callback. Please try connecting again.')
        return
      }

      try {
        const res = await gitService.exchangeGithubOAuthCode(code, state)
        if (!res.ok) {
          logger.error('[GitHubOAuthCallback] Exchange reported failure', { error: res.error })
          landBackHome('github_error', res.error || 'github_error', undefined, res.returnTo)
          return
        }
        logger.info('[GitHubOAuthCallback] GitHub credential connected')
        landBackHome('github_connected', undefined, undefined, res.returnTo)
      } catch (err) {
        logger.error('[GitHubOAuthCallback] Unexpected exchange error', err)
        setError(err instanceof Error ? err.message : 'Failed to connect GitHub')
      }
    }

    // landBackHome restores the originating app location, merging the
    // github_connected / github_error signals into its query string so the
    // app-level listeners fire — identical to the params the old backend
    // redirect appended. returnTo is honored only when it is a same-origin
    // relative path (open-redirect guard); otherwise we land on '/'.
    const landBackHome = (
      kind: 'github_connected' | 'github_error',
      code?: string,
      msg?: string,
      returnTo?: string,
    ) => {
      const safeReturnTo =
        returnTo && returnTo.startsWith('/') && !returnTo.startsWith('//') ? returnTo : '/'
      const url = new URL(safeReturnTo, window.location.origin)
      if (kind === 'github_connected') {
        url.searchParams.set('github_connected', 'true')
      } else {
        url.searchParams.set('github_error', code || 'github_error')
        if (msg) url.searchParams.set('github_error_msg', msg)
      }
      // Full reload to the in-app path: the destination route re-reads search
      // and the credential sync/toast listeners run on mount. Avoids cross-route
      // tanstack navigate() schema friction for arbitrary returnTo paths.
      window.location.assign(url.pathname + url.search + url.hash)
    }

    void run()
  }, [search])

  if (error) {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <BrandMark className="h-8 w-8" />
              <h2 className="text-xl font-semibold text-destructive">GitHub Connection Failed</h2>
            </div>
            <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
              <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
            </div>
            <button
              onClick={() => navigate({ to: '/', search: {} })}
              className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 transition-colors"
            >
              Back to App
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
            <h2 className="text-lg font-medium">Connecting GitHub...</h2>
            <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
          </div>
        </div>
      </div>
    </div>
  )
}
