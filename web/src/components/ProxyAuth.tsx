import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { supabase } from '@/lib/supabase'
import { getAdminURL } from '@/lib/constants'
import { logger } from '@/lib/logger'
import { GradientBackground } from './GradientBackground'
import Logo from '../assets/logo.svg'

/**
 * ProxyAuth handles the proxy session authentication flow.
 *
 * Flow:
 * 1. Proxy redirects browser to /auth/proxy?return={originalURL}
 * 2. This component checks for a valid Supabase session
 * 3. If no session → redirect to /auth with return back here
 * 4. If session → POST to admin-server /api/proxy-session to mint a proxy token
 * 5. Redirect to {return_url_origin}/__proxy/callback?token={token}
 */
export function ProxyAuth() {
  const navigate = useNavigate()
  const { return: returnURL } = useSearch({ from: '/auth/proxy' })
  const [error, setError] = useState<string | null>(null)
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true

    const handleProxyAuth = async () => {
      if (!returnURL) {
        setError('Missing return URL parameter.')
        return
      }

      // Validate the return URL is a valid URL.
      let parsedReturn: URL
      try {
        parsedReturn = new URL(returnURL)
      } catch {
        setError('Invalid return URL.')
        return
      }

      // Check for existing Supabase session.
      const { data: { session }, error: sessionError } = await supabase.auth.getSession()

      if (sessionError || !session) {
        // No session — redirect to login, then back here.
        logger.info('[ProxyAuth] No session, redirecting to login')
        navigate({
          to: '/auth',
          search: { redirect: `/auth/proxy?return=${encodeURIComponent(returnURL)}` },
        })
        return
      }

      // Session exists — mint a proxy session token via the admin-server.
      const adminURL = getAdminURL()
      if (!adminURL) {
        setError('Admin API URL not configured.')
        return
      }

      try {
        logger.info('[ProxyAuth] Minting proxy session token')
        const response = await fetch(`${adminURL}/api/proxy-session`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${session.access_token}`,
          },
          body: JSON.stringify({ return_url: returnURL }),
        })

        if (!response.ok) {
          const text = await response.text()
          logger.error('[ProxyAuth] Token mint failed', { status: response.status, body: text })
          setError(`Failed to authenticate: ${text}`)
          return
        }

        const { token } = await response.json()

        // Redirect to the proxy callback on the workspace domain.
        const callbackURL = `${parsedReturn.origin}/__proxy/callback?token=${encodeURIComponent(token)}`
        logger.info('[ProxyAuth] Redirecting to proxy callback', { origin: parsedReturn.origin })
        window.location.href = callbackURL
      } catch (err) {
        logger.error('[ProxyAuth] Unexpected error', err)
        setError(err instanceof Error ? err.message : 'Authentication failed')
      }
    }

    void handleProxyAuth()
  }, [navigate, returnURL])

  if (error) {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <img src={Logo} alt="Reliant Logo" className="h-8 w-auto" />
              <h2 className="text-xl font-semibold text-destructive">Workspace Authentication Failed</h2>
            </div>
            <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
              <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
            </div>
            <button
              onClick={() => navigate({ to: '/' })}
              className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 transition-colors"
            >
              Go to Dashboard
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
            <img src={Logo} alt="Reliant Logo" className="h-8 w-auto" />
            <h2 className="text-lg font-medium">Authenticating workspace access...</h2>
            <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
          </div>
        </div>
      </div>
    </div>
  )
}
