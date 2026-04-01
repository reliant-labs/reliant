import { useState } from 'react'
import { useAuthStore } from '@/store/authStore'

type Provider = 'github' | 'google' | 'apple'

export function LinkedAccounts() {
  const { linkGithubAccount, linkGoogleAccount, linkAppleAccount, unlinkIdentity, user } = useAuthStore()
  const [linkingProvider, setLinkingProvider] = useState<Provider | null>(null)
  const [unlinkingId, setUnlinkingId] = useState<string | null>(null)

  // Use identities directly from the cached user object - no API call needed
  const identities = (user?.identities || []).map((identity) => ({
    id: identity.id,
    provider: identity.provider,
    email: identity.identity_data?.email as string | undefined,
    created_at: identity.created_at || '',
  }))

  const handleLinkAccount = async (provider: Provider) => {
    setLinkingProvider(provider)
    try {
      switch (provider) {
        case 'github':
          await linkGithubAccount()
          break
        case 'google':
          await linkGoogleAccount()
          break
        case 'apple':
          await linkAppleAccount()
          break
      }
      // The OAuth flow will handle the rest, and the user object will be updated after callback
    } catch (error) {
      console.error('Failed to link account:', error)
      alert(error instanceof Error ? error.message : 'Failed to link account')
    } finally {
      setLinkingProvider(null)
    }
  }

  const handleUnlinkAccount = async (identityId: string, provider: string) => {
    // Prevent unlinking if it's the only real authentication method (exclude anonymous)
    const realIdentities = identities.filter(i => i.provider !== 'anonymous')
    if (realIdentities.length <= 1) {
      alert('Cannot unlink your only authentication method. Please add another login method first.')
      return
    }

    if (!confirm(`Are you sure you want to unlink your ${provider} account?`)) {
      return
    }

    setUnlinkingId(identityId)
    try {
      await unlinkIdentity(identityId)
      // The store already updates the user object after unlinking, so identities will update automatically
    } catch (error) {
      console.error('Failed to unlink account:', error)
      alert(error instanceof Error ? error.message : 'Failed to unlink account')
    } finally {
      setUnlinkingId(null)
    }
  }

  const isProviderLinked = (provider: Provider) => {
    return identities.some(identity => identity.provider === provider)
  }

  const getProviderIdentity = (provider: Provider) => {
    return identities.find(identity => identity.provider === provider)
  }

  const availableProviders: Provider[] = ['github', 'google']

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-lg font-medium mb-2" style={{ color: 'hsl(var(--foreground))' }}>
          Linked Accounts
        </h3>
        <p className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
          Connect multiple accounts to sign in with your preferred method.
        </p>
      </div>

      <div className="space-y-3">
        {availableProviders.map((provider) => {
          const identity = getProviderIdentity(provider)
          const isLinked = isProviderLinked(provider)

          return (
            <div key={provider} className="flex items-center justify-between p-4 rounded-lg border transition-colors" style={{
              backgroundColor: 'hsl(var(--card))',
              borderColor: 'hsl(var(--border))'
            }}>
              <div className="flex items-center gap-3 flex-1">
                <div className="w-10 h-10 rounded-full flex items-center justify-center border" style={{
                  backgroundColor: 'hsl(var(--background))',
                  borderColor: 'hsl(var(--border))'
                }}>
                  {provider === 'github' && (
                    <svg className="w-5 h-5" viewBox="0 0 24 24" fill="currentColor">
                      <path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z"/>
                    </svg>
                  )}
                  {provider === 'google' && (
                    <svg className="w-5 h-5" viewBox="0 0 24 24">
                      <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z"/>
                      <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
                      <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"/>
                      <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"/>
                    </svg>
                  )}
                  {provider === 'apple' && (
                    <svg className="w-5 h-5" viewBox="0 0 24 24" fill="currentColor">
                      <path d="M17.05 20.28c-.98.95-2.05.88-3.08.4-1.09-.5-2.08-.48-3.24 0-1.44.62-2.2.44-3.06-.4C2.79 15.25 3.51 7.59 9.05 7.31c1.35.07 2.29.74 3.08.8 1.18-.24 2.31-.93 3.57-.84 1.51.12 2.65.72 3.4 1.8-3.12 1.87-2.38 5.98.48 7.13-.57 1.5-1.31 2.99-2.54 4.09l.01-.01zM12.03 7.25c-.15-2.23 1.66-4.07 3.74-4.25.29 2.58-2.34 4.5-3.74 4.25z"/>
                    </svg>
                  )}
                </div>
                <div className="flex-1">
                  <div className="font-medium capitalize" style={{ color: 'hsl(var(--foreground))' }}>
                    {provider}
                  </div>
                  {isLinked && identity?.email && (
                    <div className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
                      {identity.email}
                    </div>
                  )}
                  {!isLinked && (
                    <div className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
                      Not connected
                    </div>
                  )}
                </div>
              </div>

              <div className="ml-4">
                {isLinked ? (
                  <button
                    onClick={() => handleUnlinkAccount(identity!.id, provider)}
                    disabled={unlinkingId === identity!.id}
                    className="px-4 py-2 text-sm font-medium rounded-md transition-all disabled:opacity-50 border-2 border-transparent hover:bg-accent/50 hover:border-border"
                    style={{
                      color: 'hsl(var(--destructive))'
                    }}
                  >
                    {unlinkingId === identity!.id ? 'Unlinking...' : 'Unlink'}
                  </button>
                ) : (
                  <button
                    onClick={() => handleLinkAccount(provider)}
                    disabled={linkingProvider !== null}
                    className="px-4 py-2 text-sm font-medium rounded-md transition-all disabled:opacity-50 border-2 border-transparent hover:bg-accent/50 hover:border-border"
                    style={{
                      color: 'hsl(var(--primary))'
                    }}
                  >
                    {linkingProvider === provider ? 'Connecting...' : 'Link'}
                  </button>
                )}
              </div>
            </div>
          )
        })}
      </div>

      {identities.length > 0 && (
        <div
          className="mt-4 p-3 rounded-md border"
          style={{
            backgroundColor: 'hsl(var(--primary) / 0.05)',
            borderColor: 'hsl(var(--primary) / 0.2)'
          }}
        >
          <p className="text-sm" style={{ color: 'hsl(var(--primary))' }}>
            💡 You can sign in with any of your linked accounts. At least one account must remain linked.
          </p>
        </div>
      )}
    </div>
  )
}
