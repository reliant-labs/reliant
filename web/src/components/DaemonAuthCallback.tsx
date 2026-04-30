import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { supabase } from '@/lib/supabase'
import { logger } from '@/lib/logger'
import { GradientBackground } from './GradientBackground'
import Logo from '../assets/logo.svg'

/**
 * DaemonAuthCallback handles the browser-based auth handoff for `reliant daemon start`.
 *
 * Flow:
 * 1. Daemon opens browser to /daemon/auth?callback_port=<port>&state=<nonce>
 * 2. This component checks for a valid Supabase session
 * 3. If no session → redirect to /auth with redirect back here
 * 4. If session → POST access token to http://localhost:<port>/callback
 * 5. Show success / error message
 */
export function DaemonAuthCallback() {
  const navigate = useNavigate()
  const { callback_port, state } = useSearch({ from: '/daemon/auth' })
  const [status, setStatus] = useState<'loading' | 'success' | 'error'>('loading')
  const [error, setError] = useState<string | null>(null)
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true

    const handleAuth = async () => {
      if (!callback_port || !state) {
        setError('Missing required parameters (callback_port, state).')
        setStatus('error')
        return
      }

      // Check for existing session.
      const { data: { session }, error: sessionError } = await supabase.auth.getSession()

      if (sessionError || !session) {
        // No session — redirect to login, then back here.
        logger.info('[DaemonAuth] No session, redirecting to login')
        const returnPath = `/daemon/auth?callback_port=${callback_port}&state=${encodeURIComponent(state)}`
        navigate({
          to: '/auth',
          search: { redirect: returnPath },
        })
        return
      }

      // Session exists — POST the access token to the daemon's callback server.
      const callbackURL = `http://localhost:${callback_port}/callback`

      try {
        logger.info('[DaemonAuth] Posting token to daemon callback', { port: callback_port })
        const response = await fetch(callbackURL, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            token: session.access_token,
            state: state,
          }),
        })

        if (!response.ok) {
          const text = await response.text()
          logger.error('[DaemonAuth] Callback failed', { status: response.status, body: text })
          setError(`Authentication failed: ${text}`)
          setStatus('error')
          return
        }

        logger.info('[DaemonAuth] Token delivered successfully')
        setStatus('success')
      } catch (err) {
        logger.error('[DaemonAuth] Failed to reach daemon callback server', err)
        setError(
          'Could not reach the daemon callback server. Make sure `reliant daemon start` is still running and try again.'
        )
        setStatus('error')
      }
    }

    void handleAuth()
  }, [callback_port, state, navigate])

  if (status === 'success') {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <img src={Logo} alt="Reliant Logo" className="h-8 w-auto" />
              <h2 className="text-xl font-semibold text-green-600 dark:text-green-400">Daemon Authenticated</h2>
            </div>
            <p className="text-center text-sm text-muted-foreground">
              Your daemon has been authenticated. You can close this tab and return to your terminal.
            </p>
          </div>
        </div>
      </div>
    )
  }

  if (status === 'error') {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <img src={Logo} alt="Reliant Logo" className="h-8 w-auto" />
              <h2 className="text-xl font-semibold text-destructive">Daemon Authentication Failed</h2>
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

  // Loading state
  return (
    <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
      <GradientBackground />
      <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8">
          <div className="flex flex-col items-center gap-4">
            <img src={Logo} alt="Reliant Logo" className="h-8 w-auto" />
            <h2 className="text-lg font-medium">Authenticating daemon...</h2>
            <div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
          </div>
        </div>
      </div>
    </div>
  )
}
