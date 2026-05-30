import { useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import { gitService } from '@/services/controlPlane/git'
import { supabase } from '@/lib/supabase'
import { SocialProviderIcon, type SocialProvider } from './icons/SocialProviderIcon'

type Provider = SocialProvider

export function LinkedAccounts() {
  const { linkGoogleAccount, linkAppleAccount, unlinkIdentity, user } = useAuthStore()
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
        case 'github': {
          // GitHub uses the control-plane custom OAuth flow rather than
          // Supabase identity linking. The Supabase GitHub provider is
          // sign-in only (0 scopes); the long-lived repo-scoped token comes
          // from /auth/github/authorize, which writes to git_credentials.
          const oauthURL = gitService.getOAuthURL()
          if (!oauthURL) throw new Error('Control plane URL not configured')
          const { data: { session } } = await supabase.auth.getSession()
          if (!session?.access_token) throw new Error('Not signed in')
          const returnTo = `${window.location.pathname}${window.location.search}`
          const params = new URLSearchParams({ token: session.access_token, returnTo })
          window.location.href = `${oauthURL}?${params.toString()}`
          return
        }
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
                  <SocialProviderIcon provider={provider} />
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