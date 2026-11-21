import { useState } from 'react'
import { adoptDevToken } from '@/lib/devSignIn'
import { AuthError } from './AuthLayout'

/**
 * The sign-in screen's dev sign-in (lib/devSignIn.ts): paste a token from
 * control-plane's `auth mint-dev-token` and the browser is signed in as its
 * sub. Rendered only when devSignInEnabled(), which no production build is.
 */
export function DevSignInPanel() {
  const [token, setToken] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      await adoptDevToken(token, '/')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <form
      onSubmit={submit}
      data-testid="dev-sign-in"
      className="space-y-3 rounded-lg border border-dashed border-border p-4"
    >
      <div className="text-sm font-medium">Dev sign-in</div>
      <p className="text-xs text-muted-foreground">
        This dev build accepts a locally minted token. In control-plane, run{' '}
        <code className="font-mono">go run ./cmd/control-plane auth mint-dev-token --web-url {window.location.origin}</code>{' '}
        and open the link it prints, or paste the token here.
      </p>
      {error && <AuthError message={error} />}
      <textarea
        aria-label="Dev token"
        value={token}
        onChange={(e) => setToken(e.target.value)}
        disabled={busy}
        rows={3}
        className="block w-full px-3 py-2 border border-border rounded-lg bg-transparent font-mono text-xs focus:outline-none focus:ring-2 focus:ring-ring"
        placeholder="eyJhbGciOiJIUzI1NiIs…"
      />
      <button
        type="submit"
        disabled={busy || token.trim() === ''}
        className="w-full py-2 px-4 border border-border rounded-lg text-sm font-medium hover:bg-accent disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {busy ? 'Signing in…' : 'Sign in with dev token'}
      </button>
    </form>
  )
}
