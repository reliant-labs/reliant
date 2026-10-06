// Copyright (c) 2025 Reliant Labs

import { useEffect, useRef, useState } from 'react'
import { useSearch } from '@tanstack/react-router'

import { connectionErrorMessage, connectionGrpc } from '@/api/connection-grpc'
import { relayedErrorMessage } from '@/lib/connection-oauth'
import { GradientBackground } from './GradientBackground'
import { BrandMark } from './icons/BrandMark'

/**
 * `/connections/oauth/callback`: where the API relays a web app's integration
 * OAuth flow (lib/connection-oauth.ts explains why the API cannot finish it
 * itself). This page finishes it with CompleteOAuth, as the signed-in user —
 * which is also what proves the flow is theirs — then returns to the page the
 * user connected from with `?connection=<id>`.
 *
 * The code in this URL is single-use and only redeemable by the user who
 * started the flow, and leaving with `location.replace` keeps it out of the
 * back stack.
 */
export function ConnectionOAuthCallback() {
  const search = useSearch({ from: '/connections/oauth/callback' })
  const [error, setError] = useState<string | null>(null)
  const ran = useRef(false)
  const back = safeRelativePath(search.redirect_after) ?? '/'

  useEffect(() => {
    if (ran.current) return
    ran.current = true

    if (search.error) {
      setError(relayedErrorMessage(search.error))
      return
    }
    if (!search.code || !search.state) {
      setError('This sign-in link is incomplete.')
      return
    }
    connectionGrpc
      .completeOAuth({ state: search.state, code: search.code })
      .then(({ connection, redirectAfter }) => {
        const landing = new URL(safeRelativePath(redirectAfter) ?? back, window.location.origin)
        landing.searchParams.set('connection', connection.id)
        window.location.replace(landing.pathname + landing.search + landing.hash)
      })
      .catch((err) => setError(connectionErrorMessage(err)))
  }, [search, back])

  return (
    <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
      <GradientBackground />
      <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-card border border-border rounded-lg shadow-xl p-8 space-y-6">
          <div className="flex flex-col items-center gap-4 text-center">
            <BrandMark className="h-8 w-8" />
            {error ? (
              <>
                <h2 className="text-lg font-semibold text-foreground">Connection not completed</h2>
                <p role="alert" className="text-sm text-destructive-ink">{error}</p>
              </>
            ) : (
              <>
                <h2 className="text-lg font-medium text-foreground">Finishing the connection…</h2>
                <div role="status" aria-label="Connecting" className="animate-spin motion-reduce:animate-none h-6 w-6 border-2 border-primary border-t-transparent rounded-full" />
              </>
            )}
          </div>
          {error && (
            <button
              type="button"
              onClick={() => window.location.replace(back)}
              className="w-full flex justify-center py-2.5 px-4 rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 transition-colors"
            >
              Back to Reliant
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

/** A same-origin relative path, or undefined: the open-redirect guard. */
function safeRelativePath(raw: string | undefined): string | undefined {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return undefined
  try {
    return new URL(raw, window.location.origin).origin === window.location.origin ? raw : undefined
  } catch {
    return undefined
  }
}
