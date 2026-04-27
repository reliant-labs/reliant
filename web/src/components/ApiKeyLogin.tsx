import { useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import { GradientBackground } from './GradientBackground'
import Logo from '../assets/logo.svg'

export function ApiKeyLogin() {
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const { setApiKeySession } = useAuthStore()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setLoading(true)

    try {
      setApiKeySession(apiKey)
    } catch {
      setError('Failed to set API key')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="relative min-h-screen">
      <GradientBackground />
      <div className="relative z-10 flex min-h-screen items-center justify-center px-4">
        <div className="w-full max-w-md space-y-8 rounded-xl border border-border bg-card p-8 shadow-lg">
          <div className="text-center">
            <img src={Logo} alt="Reliant" className="mx-auto h-12 w-12" />
            <h2 className="mt-4 text-2xl font-bold text-foreground">Sign in to Reliant</h2>
            <p className="mt-2 text-sm text-muted-foreground">Enter your API key to continue</p>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label htmlFor="api-key" className="block text-sm font-medium text-foreground">
                API Key
              </label>
              <input
                id="api-key"
                type="password"
                value={apiKey}
                onChange={e => setApiKey(e.target.value)}
                placeholder="Enter your API key..."
                className="mt-1 w-full rounded-md border border-input bg-background px-3 py-2 text-foreground shadow-sm placeholder:text-muted-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
                required
              />
            </div>

            {error && (
              <p className="text-sm text-destructive">{error}</p>
            )}

            <button
              type="submit"
              disabled={loading || !apiKey}
              className="w-full rounded-md bg-primary px-4 py-2 text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
            >
              {loading ? 'Verifying...' : 'Sign In'}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
