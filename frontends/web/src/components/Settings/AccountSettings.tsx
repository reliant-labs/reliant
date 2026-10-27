import { useState, useEffect, useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '../../store/authStore'
import { LogOut, User, CheckCircle, Trash2 } from 'lucide-react'
import Button from '@/components/forge-ui/button';
import { LinkedAccounts } from '../LinkedAccounts'
import { DeleteAccountDialog } from './DeleteAccountDialog'
import { SettingsPageHeader } from "./SettingsPageHeader";

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
  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false)

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

  // After deletion the user's data is gone but their JWT is still valid, so
  // the app would keep rendering against an account that no longer has
  // anything in it. Sign out and route to /auth to end the session.
  //
  // Sign-out failure is swallowed deliberately: the deletion already
  // succeeded, and stranding the user in a half-torn-down UI to report a
  // secondary error helps nobody. The navigation happens either way.
  const handleAccountDeleted = async () => {
    setIsDeleteDialogOpen(false)
    try {
      await signOut()
    } catch (error) {
      console.error('Sign out after account deletion failed:', error)
    }
    navigate({ to: '/auth', search: { redirect: undefined } })
  }

  return (
    <div className="space-y-6">
      <SettingsPageHeader
        title="Account"
        description="Your sign-in details and linked login methods. Come here to link another way of signing in, sign out, or delete your account."
      />

      <div className="rounded-lg border border-border bg-card p-6 space-y-4">
        {linkSuccess && (
          <div className="rounded-lg border border-success/20 bg-success/10 p-3 flex items-center gap-2">
            <CheckCircle className="w-4 h-4 text-success-ink" />
            <p className="text-sm text-success-ink">{linkSuccess}</p>
          </div>
        )}

        {accountError && (
          <div className="rounded-lg border border-destructive/20 bg-destructive/10 p-3">
            <p className="text-sm text-destructive-ink">{accountError}</p>
          </div>
        )}

        <div className="flex items-start justify-between">
          <div className="flex items-start gap-3">
            <div className="p-2 rounded-lg border border-border/60 bg-background">
              <User className="w-5 h-5 text-muted-foreground" />
            </div>
            <div>
              <h3 className="text-sm font-semibold mb-1">Current account</h3>
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
            variant="danger"
            size="xs"
            onClick={handleSignOut}
            disabled={isSigningOut}
            leftIcon={<LogOut className="w-3 h-3" />}
          >
            {isSigningOut ? 'Signing out...' : 'Sign Out'}
          </Button>
        </div>
      </div>

      <div className="rounded-lg border border-destructive/30 bg-card p-6 space-y-4">
        <div>
          <h3 className="text-sm font-semibold mb-1">Delete account</h3>
          <p className="text-sm text-muted-foreground">
            Permanently delete your projects, chats and connected provider
            accounts. This cannot be undone.
          </p>
        </div>
        <Button
          variant="danger"
          size="xs"
          onClick={() => setIsDeleteDialogOpen(true)}
          leftIcon={<Trash2 className="w-3 h-3" />}
        >
          Delete account
        </Button>
      </div>

      <DeleteAccountDialog
        isOpen={isDeleteDialogOpen}
        onClose={() => setIsDeleteDialogOpen(false)}
        onDeleted={handleAccountDeleted}
      />
    </div>
  )
}