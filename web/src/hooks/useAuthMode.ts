import { useState, useEffect } from 'react'
import { getGRPCBaseURL } from '@/api/grpc-client'

export type AuthMode = 'dev' | 'apikey' | 'supabase'

let cachedAuthMode: AuthMode | null = null

export function getAuthMode(): AuthMode | null {
  return cachedAuthMode
}

export function useAuthMode(): { authMode: AuthMode; loading: boolean } {
  const [authMode, setAuthMode] = useState<AuthMode>(cachedAuthMode ?? 'supabase')
  const [loading, setLoading] = useState(cachedAuthMode === null)

  useEffect(() => {
    if (cachedAuthMode !== null) return

    const baseUrl = getGRPCBaseURLPublic()
    if (!baseUrl) {
      // Config not yet available (e.g. Electron loading), default to supabase
      cachedAuthMode = 'supabase'
      setAuthMode('supabase')
      setLoading(false)
      return
    }

    fetch(`${baseUrl}/health`)
      .then(r => r.json())
      .then(data => {
        const mode = (data.auth_mode || 'supabase') as AuthMode
        cachedAuthMode = mode
        setAuthMode(mode)
      })
      .catch(() => {
        cachedAuthMode = 'supabase'
        setAuthMode('supabase')
      })
      .finally(() => setLoading(false))
  }, [])

  return { authMode, loading }
}