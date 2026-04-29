import { useState, useEffect } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Mail, KeyRound, CheckCircle } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { cn } from '../lib/utils'

interface ForgotPasswordProps {
  onBackToSignIn: () => void
}

type Step = 'email' | 'code'

export function ForgotPassword({ onBackToSignIn }: ForgotPasswordProps) {
  const navigate = useNavigate()
  const { sendPasswordResetOTP, verifyPasswordResetOTP } = useAuthStore()
  
  const [step, setStep] = useState<Step>('email')
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [resendCooldown, setResendCooldown] = useState(0)

  // Resend cooldown timer
  useEffect(() => {
    if (resendCooldown > 0) {
      const timer = setTimeout(() => setResendCooldown(resendCooldown - 1), 1000)
      return () => clearTimeout(timer)
    }
  }, [resendCooldown])

  const validateEmail = (email: string) => {
    const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
    return emailRegex.test(email)
  }

  const handleSendCode = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    
    if (!email) {
      setError('Please enter your email address')
      return
    }
    
    if (!validateEmail(email)) {
      setError('Please enter a valid email address')
      return
    }

    setLoading(true)
    
    try {
      await sendPasswordResetOTP(email)
      setStep('code')
      setResendCooldown(60)
    } catch (err) {
      console.error('Send OTP error:', err)
      
      if (err instanceof Error) {
        if (err.message.includes('rate limit')) {
          setError('Too many requests. Please wait a moment and try again.')
        } else if (err.message.includes('User not found') || err.message.includes('not found')) {
          // For security, we don't reveal if email exists - proceed to code step
          setStep('code')
          setResendCooldown(60)
        } else {
          setError(err.message || 'Failed to send verification code. Please try again.')
        }
      } else {
        setError('Failed to send verification code. Please try again.')
      }
    } finally {
      setLoading(false)
    }
  }

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
    
    try {
      await verifyPasswordResetOTP(email, code)
      // User is now authenticated, navigate to reset password page
      navigate({ to: '/reset-password' })
    } catch (err) {
      console.error('Verify OTP error:', err)
      
      if (err instanceof Error) {
        if (err.message.includes('expired')) {
          setError('Verification code has expired. Please request a new one.')
        } else if (err.message.includes('invalid')) {
          setError('Invalid verification code. Please check and try again.')
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
    
    try {
      await sendPasswordResetOTP(email)
      setResendCooldown(60)
    } catch (err) {
      console.error('Resend OTP error:', err)
      
      if (err instanceof Error) {
        if (err.message.includes('rate limit')) {
          setError('Too many requests. Please wait a moment and try again.')
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

  const handleBackToEmail = () => {
    setStep('email')
    setCode('')
    setError('')
  }

  // Step 1: Email Entry
  if (step === 'email') {
    return (
      <div className="w-full max-w-md mx-auto p-8">
        <div className="space-y-6">
          {/* Header */}
          <div className="space-y-2">
            <button
              onClick={onBackToSignIn}
              className="flex items-center gap-2 text-sm font-medium text-muted-foreground transition-colors hover:text-primary mb-4"
            >
              <ArrowLeft className="w-4 h-4" />
              Back to Sign In
            </button>

            <h2 className="text-2xl font-semibold text-foreground">
              Forgot Password?
            </h2>
            <p className="text-sm text-muted-foreground">
              No worries! Enter your email and we'll send you a verification code.
            </p>
          </div>

          {/* Form */}
          <form onSubmit={handleSendCode} className="space-y-4">
            <div className="space-y-2">
              <label 
                htmlFor="email" 
                className="text-sm font-medium text-foreground"
              >
                Email Address
              </label>
              <div className="relative">
                <Mail className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-muted-foreground" />
                <input
                  id="email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                  disabled={loading}
                  className={cn(
                    'w-full pl-10 pr-4 py-2 rounded-lg border-2 bg-background text-foreground transition-all outline-none focus:border-primary disabled:opacity-50',
                    error ? 'border-destructive' : 'border-border'
                  )}
                />
              </div>
              {error && (
                <p className="text-sm text-destructive">
                  {error}
                </p>
              )}
            </div>

            <button
              type="submit"
              disabled={loading}
              className="w-full py-2 px-4 rounded-lg bg-primary text-primary-foreground font-medium transition-colors hover:bg-primary/90 disabled:opacity-50"
            >
              {loading ? 'Sending...' : 'Send Verification Code'}
            </button>
          </form>

          {/* Info Box */}
          <div className="p-3 rounded-lg border border-border bg-muted/50 text-sm text-muted-foreground">
            💡 <strong>Tip:</strong> Check your spam folder if you don't see the email within a few minutes.
          </div>
        </div>
      </div>
    )
  }

  // Step 2: Code Entry
  return (
    <div className="w-full max-w-md mx-auto p-8">
      <div className="space-y-6">
        {/* Header */}
        <div className="space-y-2">
          <button
            onClick={handleBackToEmail}
            className="flex items-center gap-2 text-sm font-medium text-muted-foreground transition-colors hover:text-primary mb-4"
          >
            <ArrowLeft className="w-4 h-4" />
            Back
          </button>

          <div className="w-16 h-16 mx-auto rounded-full flex items-center justify-center mb-4 bg-primary/15">
            <CheckCircle className="w-8 h-8 text-primary" />
          </div>

          <h2 className="text-2xl font-semibold text-center text-foreground">
            Check Your Email
          </h2>
          <p className="text-sm text-center text-muted-foreground">
            We've sent a 6-digit verification code to <strong>{email}</strong>
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
                }}
                placeholder="000000"
                disabled={loading}
                className={cn(
                  'w-full pl-10 pr-4 py-2 rounded-lg border-2 bg-background text-foreground transition-all outline-none focus:border-primary disabled:opacity-50 text-center text-2xl tracking-widest',
                  error ? 'border-destructive' : 'border-border'
                )}
                maxLength={6}
                autoComplete="one-time-code"
              />
            </div>
            {error && (
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
            {loading ? 'Verifying...' : 'Verify Code'}
          </button>

          {/* Resend Code */}
          <div className="text-center">
            {resendCooldown > 0 ? (
              <p className="text-sm text-muted-foreground">
                Resend code in {resendCooldown}s
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
      </div>
    </div>
  )
}