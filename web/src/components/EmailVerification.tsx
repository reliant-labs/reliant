import { useState, useEffect } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { KeyRound, CheckCircle, LogOut } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { BrandMark } from './icons/BrandMark'

interface EmailVerificationProps {
  autoSend?: boolean
  email?: string
}

export function EmailVerification({ autoSend = true, email }: EmailVerificationProps) {
  const navigate = useNavigate()
  const { user, sendEmailVerificationOTP, verifyEmailOTP, signOut } = useAuthStore()

  const emailToShow = email ?? user?.email

  const [code, setCode] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [resendCooldown, setResendCooldown] = useState(60)
  const [initialCodeSent, setInitialCodeSent] = useState(false)

  const getCooldownFromError = (message: string): number | null => {
    const match = message.match(/after\s+(\d+)\s+seconds?/i)
    if (!match) return null

    const seconds = Number(match[1])
    return Number.isFinite(seconds) ? seconds : null
  }

  const isCooldownError = (message: string): boolean => {
    const lower = message.toLowerCase()
    return lower.includes('rate limit') || lower.includes('security purposes') || /after\s+\d+\s+seconds?/.test(lower)
  }

  const showInlineError = !!error && !isCooldownError(error)

  // Send initial verification code on mount
  useEffect(() => {
    const sendInitialCode = async () => {
      if (!initialCodeSent && autoSend) {
        if (!emailToShow) {
          setError('Missing email for verification. Please go back and try again.')
          return
        }

        try {
          await sendEmailVerificationOTP(emailToShow)
          setInitialCodeSent(true)
        } catch (err) {
          console.error('Failed to send initial verification code:', err)
          if (err instanceof Error) {
            if (isCooldownError(err.message)) {
              const cooldownSeconds = getCooldownFromError(err.message)
              setResendCooldown(cooldownSeconds ?? 60)
              setInitialCodeSent(true)
              setError('')
              return
            }
            setError(err.message || 'Failed to send verification code')
          }
        }
      }
    }

    sendInitialCode()
  }, [sendEmailVerificationOTP, initialCodeSent, autoSend, emailToShow])

  // Resend cooldown timer
  useEffect(() => {
    if (resendCooldown > 0) {
      const timer = setTimeout(() => setResendCooldown(resendCooldown - 1), 1000)
      return () => clearTimeout(timer)
    }
  }, [resendCooldown])

  const handleVerifyCode = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (!code) {
      setError('Please enter the verification code')
      return
    }

    if (code.length !== 6) {
      setError('Verification code must be 6 digits')
      return
    }

    setLoading(true)

    if (!emailToShow) {
      setError('Missing email for verification. Please go back and try again.')
      setLoading(false)
      return
    }

    try {
      await verifyEmailOTP(code, emailToShow)
      // User state will be refreshed by verifyEmailOTP
      // AuthGuard will handle navigation to main app
      navigate({ to: '/', search: {} })
    } catch (err) {
      console.error('Verify OTP error:', err)

      if (err instanceof Error) {
        if (err.message.includes('expired')) {
          setError('Verification code has expired. Please request a new one.')
        } else if (err.message.includes('invalid')) {
          setError('Invalid verification code. Please check and try again.')
        } else if (err.message.includes('already verified')) {
          // Email already verified, just navigate
          navigate({ to: '/', search: {} })
        } else {
          setError(err.message || 'Failed to verify code. Please try again.')
        }
      } else {
        setError('Failed to verify code. Please try again.')
      }
    } finally {
      setLoading(false)
    }
  }

  const handleResendCode = async () => {
    if (resendCooldown > 0) return

    setError('')
    setLoading(true)

    if (!emailToShow) {
      setError('Missing email for verification. Please go back and try again.')
      setLoading(false)
      return
    }

    try {
      await sendEmailVerificationOTP(emailToShow)
      setInitialCodeSent(true)
      setResendCooldown(60)
    } catch (err) {
      console.error('Resend OTP error:', err)

      if (err instanceof Error) {
        if (isCooldownError(err.message)) {
          const cooldownSeconds = getCooldownFromError(err.message)
          setResendCooldown(cooldownSeconds ?? 60)
          setInitialCodeSent(true)
          setError('')
        } else if (err.message.includes('already verified')) {
          // Email already verified, just navigate
          navigate({ to: '/', search: {} })
        } else {
          setError(err.message || 'Failed to resend code. Please try again.')
        }
      } else {
        setError('Failed to resend code. Please try again.')
      }
    } finally {
      setLoading(false)
    }
  }

  const handleSignOut = async () => {
    try {
      await signOut()
      navigate({ to: '/auth', search: { redirect: undefined } })
    } catch (err) {
      console.error('Sign out error:', err)
    }
  }

  return (
    <div className="min-h-screen flex flex-col bg-background">
      <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl p-8">
          {/* Logo */}
          <div className="flex flex-col items-center gap-4 mb-8">
            <div className="flex items-center gap-3">
              <BrandMark className="h-8 w-8" />
              <h1 className="text-3xl font-bold">Reliant</h1>
            </div>
          </div>

          <div className="space-y-6">
            {/* Header */}
            <div className="space-y-2">
              <div className="w-16 h-16 mx-auto rounded-full flex items-center justify-center mb-4 bg-primary/15">
                <CheckCircle className="w-8 h-8 text-primary" />
              </div>

              <h2 className="text-2xl font-semibold text-center text-foreground">
                Verify Your Email
              </h2>
              <p className="text-sm text-center text-muted-foreground">
                We've sent a 6-digit verification code to <strong>{emailToShow}</strong>
              </p>
            </div>

            {/* Form */}
            <form onSubmit={handleVerifyCode} className="space-y-4">
              <div className="space-y-2">
                <label
                  htmlFor="code"
                  className="text-sm font-medium text-foreground"
                >
                  Verification Code
                </label>
                <div className="relative">
                  <KeyRound className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-muted-foreground" />
                  <input
                    id="code"
                    type="text"
                    value={code}
                    onChange={(e) => {
                      // Only allow numbers and limit to 6 digits
                      const value = e.target.value.replace(/\D/g, '').slice(0, 6)
                      setCode(value)
                      // Clear error when user starts typing
                      if (error) setError('')
                    }}
                    placeholder="000000"
                    disabled={loading}
                    className="w-full pl-10 pr-4 py-2 rounded-lg border-2 border-border bg-background text-foreground transition-all outline-none focus:border-primary disabled:opacity-50 text-center text-2xl tracking-widest"
                    maxLength={6}
                    autoComplete="one-time-code"
                    autoFocus
                  />
                </div>
                {showInlineError && (
                  <p className="text-sm text-destructive">
                    {error}
                  </p>
                )}
              </div>

              <button
                type="submit"
                disabled={loading || code.length !== 6}
                className="w-full py-2 px-4 rounded-lg bg-primary text-primary-foreground font-medium transition-colors hover:bg-primary/90 disabled:opacity-50"
              >
                {loading ? 'Verifying...' : 'Verify Email'}
              </button>

              {/* Resend Code */}
              <div className="text-center">
                {resendCooldown > 0 ? (
                  <p className="text-sm text-destructive">
                    For security purposes, you can only request this after {resendCooldown} seconds.
                  </p>
                ) : (
                  <button
                    type="button"
                    onClick={handleResendCode}
                    disabled={loading}
                    className="text-sm font-medium text-primary transition-colors hover:text-primary/80 disabled:opacity-50"
                  >
                    Resend verification code
                  </button>
                )}
              </div>
            </form>

            {/* Info Box */}
            <div className="p-3 rounded-lg border border-border bg-muted/50 text-sm text-muted-foreground">
              🔒 <strong>Security tip:</strong> The verification code expires in 10 minutes.
            </div>

            {/* Sign Out Option */}
            <div className="text-center pt-4 border-t border-border">
              <button
                type="button"
                onClick={handleSignOut}
                className="flex items-center justify-center gap-2 mx-auto text-sm font-medium text-muted-foreground transition-colors hover:text-destructive"
              >
                <LogOut className="w-4 h-4" />
                Sign out and use different email
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}