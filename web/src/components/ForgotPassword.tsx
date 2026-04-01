import { useState, useEffect } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Mail, KeyRound, CheckCircle } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'

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
              className="flex items-center gap-2 text-sm font-medium transition-colors mb-4"
              style={{ color: 'hsl(var(--muted-foreground))' }}
              onMouseEnter={(e) => {
                e.currentTarget.style.color = 'hsl(var(--primary))'
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.color = 'hsl(var(--muted-foreground))'
              }}
            >
              <ArrowLeft className="w-4 h-4" />
              Back to Sign In
            </button>

            <h2 className="text-2xl font-semibold" style={{ color: 'hsl(var(--foreground))' }}>
              Forgot Password?
            </h2>
            <p className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
              No worries! Enter your email and we'll send you a verification code.
            </p>
          </div>

          {/* Form */}
          <form onSubmit={handleSendCode} className="space-y-4">
            <div className="space-y-2">
              <label 
                htmlFor="email" 
                className="text-sm font-medium"
                style={{ color: 'hsl(var(--foreground))' }}
              >
                Email Address
              </label>
              <div className="relative">
                <Mail 
                  className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5" 
                  style={{ color: 'hsl(var(--muted-foreground))' }}
                />
                <input
                  id="email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                  disabled={loading}
                  className="w-full pl-10 pr-4 py-2 rounded-lg border-2 transition-all outline-none disabled:opacity-50"
                  style={{
                    backgroundColor: 'hsl(var(--background))',
                    borderColor: error ? 'hsl(var(--destructive))' : 'hsl(var(--border))',
                    color: 'hsl(var(--foreground))',
                  }}
                  onFocus={(e) => {
                    if (!error) {
                      e.currentTarget.style.borderColor = 'hsl(var(--primary))'
                    }
                  }}
                  onBlur={(e) => {
                    if (!error) {
                      e.currentTarget.style.borderColor = 'hsl(var(--border))'
                    }
                  }}
                />
              </div>
              {error && (
                <p className="text-sm" style={{ color: 'hsl(var(--destructive))' }}>
                  {error}
                </p>
              )}
            </div>

            <button
              type="submit"
              disabled={loading}
              className="w-full py-2 px-4 rounded-lg font-medium transition-all disabled:opacity-50"
              style={{
                backgroundColor: 'hsl(var(--primary))',
                color: 'hsl(var(--primary-foreground))',
              }}
              onMouseEnter={(e) => {
                if (!loading) {
                  e.currentTarget.style.opacity = '0.9'
                }
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.opacity = '1'
              }}
            >
              {loading ? 'Sending...' : 'Send Verification Code'}
            </button>
          </form>

          {/* Info Box */}
          <div 
            className="p-3 rounded-lg border text-sm"
            style={{
              backgroundColor: 'hsl(var(--muted) / 0.5)',
              borderColor: 'hsl(var(--border))',
              color: 'hsl(var(--muted-foreground))',
            }}
          >
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
            className="flex items-center gap-2 text-sm font-medium transition-colors mb-4"
            style={{ color: 'hsl(var(--muted-foreground))' }}
            onMouseEnter={(e) => {
              e.currentTarget.style.color = 'hsl(var(--primary))'
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.color = 'hsl(var(--muted-foreground))'
            }}
          >
            <ArrowLeft className="w-4 h-4" />
            Back
          </button>

          <div 
            className="w-16 h-16 mx-auto rounded-full flex items-center justify-center mb-4"
            style={{
              backgroundColor: 'hsl(var(--primary) / 0.15)',
            }}
          >
            <CheckCircle className="w-8 h-8" style={{ color: 'hsl(var(--primary))' }} />
          </div>

          <h2 className="text-2xl font-semibold text-center" style={{ color: 'hsl(var(--foreground))' }}>
            Check Your Email
          </h2>
          <p className="text-sm text-center" style={{ color: 'hsl(var(--muted-foreground))' }}>
            We've sent a 6-digit verification code to <strong>{email}</strong>
          </p>
        </div>

        {/* Form */}
        <form onSubmit={handleVerifyCode} className="space-y-4">
          <div className="space-y-2">
            <label 
              htmlFor="code" 
              className="text-sm font-medium"
              style={{ color: 'hsl(var(--foreground))' }}
            >
              Verification Code
            </label>
            <div className="relative">
              <KeyRound 
                className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5" 
                style={{ color: 'hsl(var(--muted-foreground))' }}
              />
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
                className="w-full pl-10 pr-4 py-2 rounded-lg border-2 transition-all outline-none disabled:opacity-50 text-center text-2xl tracking-widest"
                style={{
                  backgroundColor: 'hsl(var(--background))',
                  borderColor: error ? 'hsl(var(--destructive))' : 'hsl(var(--border))',
                  color: 'hsl(var(--foreground))',
                }}
                onFocus={(e) => {
                  if (!error) {
                    e.currentTarget.style.borderColor = 'hsl(var(--primary))'
                  }
                }}
                onBlur={(e) => {
                  if (!error) {
                    e.currentTarget.style.borderColor = 'hsl(var(--border))'
                  }
                }}
                maxLength={6}
                autoComplete="one-time-code"
              />
            </div>
            {error && (
              <p className="text-sm" style={{ color: 'hsl(var(--destructive))' }}>
                {error}
              </p>
            )}
          </div>

          <button
            type="submit"
            disabled={loading || code.length !== 6}
            className="w-full py-2 px-4 rounded-lg font-medium transition-all disabled:opacity-50"
            style={{
              backgroundColor: 'hsl(var(--primary))',
              color: 'hsl(var(--primary-foreground))',
            }}
            onMouseEnter={(e) => {
              if (!loading && code.length === 6) {
                e.currentTarget.style.opacity = '0.9'
              }
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.opacity = '1'
            }}
          >
            {loading ? 'Verifying...' : 'Verify Code'}
          </button>

          {/* Resend Code */}
          <div className="text-center">
            {resendCooldown > 0 ? (
              <p className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
                Resend code in {resendCooldown}s
              </p>
            ) : (
              <button
                type="button"
                onClick={handleResendCode}
                disabled={loading}
                className="text-sm font-medium transition-colors disabled:opacity-50"
                style={{ color: 'hsl(var(--primary))' }}
                onMouseEnter={(e) => {
                  if (!loading) {
                    e.currentTarget.style.opacity = '0.8'
                  }
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.opacity = '1'
                }}
              >
                Resend verification code
              </button>
            )}
          </div>
        </form>

        {/* Info Box */}
        <div 
          className="p-3 rounded-lg border text-sm"
          style={{
            backgroundColor: 'hsl(var(--muted) / 0.5)',
            borderColor: 'hsl(var(--border))',
            color: 'hsl(var(--muted-foreground))',
          }}
        >
          🔒 <strong>Security tip:</strong> The verification code expires in 10 minutes.
        </div>
      </div>
    </div>
  )
}
