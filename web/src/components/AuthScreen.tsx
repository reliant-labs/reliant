import { useState, useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useNavigate } from '@tanstack/react-router'
import { OAuthButton } from './OAuthButton'
import { ForgotPassword } from './ForgotPassword'
import { EmailVerification } from './EmailVerification'
import { PasswordInput, ConfirmPasswordInput } from './PasswordInput'
import { validatePassword } from '../utils/passwordValidation'
import { GradientBackground } from './GradientBackground'
import Logo from '../assets/logo.svg'

export function AuthScreen() {
  const [mode, setMode] = useState<'login' | 'signup' | 'forgot-password'>('login')
  const [verificationSent, setVerificationSent] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [signupEmailForVerification, setSignupEmailForVerification] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const { signIn, signUp, signInAnonymously, signInWithGithub, signInWithGoogle, signInWithApple } = useAuthStore()
  const navigate = useNavigate()

  // Track auth screen view for pre-auth funnel
  useEffect(() => {
    if (!window.electronAPI?.analyticsTrack) return

    void window.electronAPI.analyticsTrack({
      eventName: 'auth_screen_viewed',
      metadata: {
        view: mode,
      },
    })
  }, [mode])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)

    // Validation for signup mode
    if (mode === 'signup') {
      // Check all password requirements using shared validation
      const passwordValidation = validatePassword(password)
      if (!passwordValidation.valid) {
        setError(`Password must meet all requirements: ${passwordValidation.unmetRequirements.map(r => r.label.toLowerCase()).join(', ')}`)
        return
      }

      if (password !== confirmPassword) {
        setError('Passwords do not match')
        return
      }
    }

    setLoading(true)

    try {
      if (mode === 'login') {
        await signIn(email, password)
        // Only navigate on success
        navigate({ to: '/' })
      } else {
        const { session } = await signUp(email, password)
        
        // If we get a user but no session, email verification is required
        if (!session) {
          setSignupEmailForVerification(email)
          setVerificationSent(true)
          setLoading(false)
          return
        }

        // Only navigate on success
        navigate({ to: '/' })
      }
    } catch (err: unknown) {
      // Extract the error message from Supabase error format
      // Supabase returns errors in different formats, handle them all
      let errorMessage = 'Authentication failed'

      // Check error code first for specific Supabase errors
      if (typeof err === 'object' && err !== null && 'code' in err) {
        if (err.code === 'email_not_confirmed') {
          errorMessage = 'Please check your email and confirm your account before signing in.'
        } else if (err.code === 'email_address_invalid') {
          errorMessage = 'Invalid email address format. Please enter a valid email.'
        }
      }

      if (typeof err === 'string') {
        errorMessage = err
      } else if (typeof err === 'object' && err !== null) {
        if ('message' in err && typeof err.message === 'string') {
          errorMessage = err.message
        } else if ('error_description' in err && typeof err.error_description === 'string') {
          errorMessage = err.error_description
        } else if ('msg' in err && typeof err.msg === 'string') {
          errorMessage = err.msg
        }
      }

      // Make error messages more user-friendly based on content
      if (errorMessage.includes('email_address_invalid')) {
        errorMessage = 'Invalid email address format. Please enter a valid email.'
      } else if (errorMessage.includes('Invalid login credentials')) {
        errorMessage = 'Invalid email or password. Please try again.'
      } else if (errorMessage.includes('Email not confirmed') || errorMessage.includes('email_not_confirmed')) {
        errorMessage = 'Please check your email and confirm your account before signing in.'
      }

      setLoading(false)
      setError(errorMessage)
      return
    }

    setLoading(false)
  }

  const handleOAuthSignIn = async (provider: 'google' | 'github' | 'apple') => {
    setLoading(true)
    setError(null)

    try {
      if (provider === 'google') {
        await signInWithGoogle()
        // For web, OAuth will redirect. For Electron, window will open.
        // Don't navigate here - let the OAuth callback handle it
      } else if (provider === 'github') {
        await signInWithGithub()
        // Same as above
      } else if (provider === 'apple') {
        await signInWithApple()
        // Same as above
      }
    } catch (err: unknown) {
      let errorMessage = `Failed to sign in with ${provider}`

      if (err instanceof Error) {
        errorMessage = err.message
      } else if (typeof err === 'object' && err !== null) {
        if ('error_description' in err && typeof err.error_description === 'string') {
          errorMessage = err.error_description
        } else if ('message' in err && typeof err.message === 'string') {
          errorMessage = err.message
        }
      }

      // Handle specific OAuth errors
      if (errorMessage.includes('Multiple accounts') || errorMessage.includes('same email')) {
        errorMessage = `An account with this email already exists. Please sign in with your existing method first, then link ${provider} in settings.`
      }

      setError(errorMessage)
      setLoading(false)
    }
  }

  // Render forgot password view
  if (mode === 'forgot-password') {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl">
            <ForgotPassword
              onBackToSignIn={() => setMode('login')}
            />
          </div>
        </div>
      </div>
    )
  }

  if (verificationSent) {
    return (
      <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
        <GradientBackground />
        <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl">
             <EmailVerification autoSend={true} email={signupEmailForVerification} />
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
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl">
          <div className="p-8 space-y-6">
            <div className="flex flex-col items-center gap-4">
              <div className="flex items-center gap-3">
                <img src={Logo} alt="Reliant Logo" className="h-8 w-auto" />
                <h1 className="text-3xl font-bold">Reliant</h1>
              </div>
              <div className="text-center">
                <h2 className="text-xl font-semibold">
                  {mode === 'login' ? 'Sign in' : 'Sign up'}
                </h2>
              </div>
            </div>

        <form className="space-y-5" onSubmit={handleSubmit} autoComplete="on" id="auth-form" method="post" action="#">
          {error && (
            <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
              <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
            </div>
          )}

          <div className="space-y-4">
            <div>
              <label htmlFor="email" className="block text-sm font-medium mb-1.5">
                Email address
              </label>
              <input
                id="email"
                name="email"
                type="email"
                autoComplete="username email"
                autoFocus
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="block w-full px-3 py-2.5 border border-border rounded-lg focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
                style={{ backgroundColor: 'transparent' }}
                placeholder="you@example.com"
              />
            </div>

            <div>
              <PasswordInput
                id="password"
                name="password"
                label="Password"
                value={password}
                onChange={setPassword}
                autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                required
                disabled={loading}
                showStrengthIndicator={mode === 'signup'}
                showRequirements={mode === 'signup'}
              />

              {/* Forgot Password Link - Only show in login mode */}
              {mode === 'login' && (
                <div className="flex justify-end mt-1">
                  <button
                    type="button"
                    onClick={() => setMode('forgot-password')}
                    className="text-sm text-primary hover:underline"
                  >
                    Forgot password?
                  </button>
                </div>
              )}
            </div>

            {mode === 'signup' && (
              <ConfirmPasswordInput
                id="confirmPassword"
                name="confirmPassword"
                label="Confirm Password"
                value={confirmPassword}
                password={password}
                onChange={setConfirmPassword}
                autoComplete="new-password"
                required
                disabled={loading}
              />
            )}
          </div>

          {/* Primary Submit Button */}
          <div className="space-y-3 pt-1">
            <button
              type="submit"
              disabled={loading}
              className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {loading ? 'Processing...' : mode === 'login' ? 'Sign In' : 'Sign Up'}
            </button>

            <div className="text-center text-sm text-muted-foreground">
              {mode === 'login' ? "Don't have an account? " : 'Already have an account? '}
              <button
                type="button"
                onClick={() => {
                  setMode(mode === 'login' ? 'signup' : 'login')
                  setError(null)
                  setConfirmPassword('')
                }}
                className="font-medium text-primary hover:text-primary/80 transition-colors"
              >
                {mode === 'login' ? 'Sign up' : 'Sign in'}
              </button>
            </div>
          </div>

          {/* OAuth Providers */}
          <div className="relative">
            <div className="absolute inset-0 flex items-center">
              <div className="w-full border-t border-border" />
            </div>
            <div className="relative flex justify-center text-sm">
              <span className="px-2 bg-background text-muted-foreground">
                Or continue with
              </span>
            </div>
          </div>

          <div className="space-y-3">
            <OAuthButton
              provider="github"
              onClick={() => handleOAuthSignIn('github')}
              loading={loading}
            />
            <OAuthButton
              provider="google"
              onClick={() => handleOAuthSignIn('google')}
              loading={loading}
            />
          </div>

          {/* Skip for now */}
          <div className="text-center">
            <button
              type="button"
              disabled={loading}
              onClick={async () => {
                setLoading(true)
                setError(null)
                try {
                  await signInAnonymously()
                  navigate({ to: '/' })
                } catch (err: unknown) {
                  let errorMessage = 'Failed to continue as guest'
                  if (err instanceof Error) {
                    errorMessage = err.message
                  }
                  setError(errorMessage)
                  setLoading(false)
                }
              }}
              className="text-sm text-muted-foreground hover:text-foreground transition-colors"
            >
              Skip for now
            </button>
          </div>

          {/* Legal Links */}
          <div className="text-center text-xs text-muted-foreground pt-2">
            By continuing, you agree to our{' '}
            <a
              href="https://reliantlabs.io/terms"
              target="_blank"
              rel="noopener noreferrer"
              className="text-primary hover:underline"
            >
              Terms of Service
            </a>{' '}
            and{' '}
            <a
              href="https://reliantlabs.io/privacy"
              target="_blank"
              rel="noopener noreferrer"
              className="text-primary hover:underline"
            >
              Privacy Policy
            </a>
          </div>
        </form>
        </div>
        </div>
      </div>
    </div>
  )
}
