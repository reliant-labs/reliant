import { useState, useEffect, useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '../../store/authStore'
import { LogOut, User, CheckCircle } from 'lucide-react'
import { Button } from '../ui/Button'
import { LinkedAccounts } from '../LinkedAccounts'

export function AccountSettings() {
  const {
    user,
    signOut,
    authError,
    clearAuthError,
  } = useAuthStore()
  const navigate = useNavigate()

  const [isSigningOut, setIsSigningOut] = useState(false)
  const [linkSuccess, setLinkSuccess] = useState<string | null>(null)
  const [accountError, setAccountError] = useState<string | null>(null)

  // Track previous providers to detect newly linked identities
  const initializedRef = useRef(false)
  const prevProvidersRef = useRef<string[]>([])

  const realIdentities = (user?.identities || []).filter(
    (identity) => identity.provider !== 'anonymous'
  )
  const linkedProviders = [...new Set(realIdentities.map((i) => i.provider))]
  const email = user?.email

  useEffect(() => {
    const currentProviders = realIdentities.map((i) => i.provider)

    if (!initializedRef.current) {
      initializedRef.current = true
      prevProvidersRef.current = currentProviders
      return
    }

    if (currentProviders.length > prevProvidersRef.current.length) {
      const newProvider = currentProviders.find(
        (p) => !prevProvidersRef.current.includes(p)
      )
      if (newProvider) {
        setLinkSuccess(`Successfully linked ${newProvider}!`)
        setTimeout(() => setLinkSuccess(null), 5000)
      }
    }

    prevProvidersRef.current = currentProviders
  }, [realIdentities])

  useEffect(() => {
    if (authError) {
      setAccountError(authError)
      clearAuthError()
    }
  }, [authError, clearAuthError])

  const handleSignOut = async () => {
    if (!confirm('Are you sure you want to sign out?')) return
    setIsSigningOut(true)
    try {
      await signOut()
      // Explicitly route to the auth screen. AuthGuard would eventually do
      // this once its loading=true window closes, but there's a render gap
      // where the (now project-less) main app shell can flash up — driving
      // the navigation here closes that gap, and also clears any onboarding
      // plan still sitting in the URL.
      navigate({ to: '/auth', search: { redirect: undefined } })
    } catch (error) {
      console.error('Sign out failed:', error)
      alert('Failed to sign out. Please try again.')
    } finally {
      setIsSigningOut(false)
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-semibold mb-2">Account</h2>
        <p className="text-sm text-muted-foreground">
          Manage your account settings and authentication
        </p>
      </div>

      <div className="border border-border/40 rounded-lg p-6 space-y-4 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        {linkSuccess && (
          <div className="rounded-lg bg-green-50 dark:bg-green-950/20 border border-green-200 dark:border-green-800 p-3 flex items-center gap-2">
            <CheckCircle className="w-4 h-4 text-green-600 dark:text-green-400" />
            <p className="text-sm text-green-800 dark:text-green-200">{linkSuccess}</p>
          </div>
        )}

        {accountError && (
          <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
            <p className="text-sm text-red-800 dark:text-red-200">{accountError}</p>
          </div>
        )}

        <div className="flex items-start justify-between">
          <div className="flex items-start gap-3">
            <div className="p-2 bg-muted rounded-lg">
              <User className="w-5 h-5 text-muted-foreground" />
            </div>
            <div>
              <h3 className="font-medium mb-1">Current Account</h3>
              <div className="space-y-1">
                <p className="text-sm">{email || linkedProviders.join(', ') || 'Connected'}</p>
                <p className="text-xs text-muted-foreground">
                  {linkedProviders.length > 0
                    ? `Connected via ${linkedProviders.join(', ')}`
                    : 'Signed in'}
                </p>
              </div>
            </div>
          </div>
        </div>

        <div className="border-t border-border/40 pt-4">
          <LinkedAccounts />
        </div>

        <div className="border-t border-border/40 pt-4">
          <Button
            variant="destructive"
            size="xs"
            onClick={handleSignOut}
            disabled={isSigningOut}
            leftIcon={<LogOut className="w-3 h-3" />}
          >
            {isSigningOut ? 'Signing out...' : 'Sign Out'}
          </Button>
        </div>
      </div>
    </div>
  )
}