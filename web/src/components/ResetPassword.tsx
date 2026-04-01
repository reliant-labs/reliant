import { useState, useMemo } from 'react'
import { Eye, EyeOff, Check, X, CheckCircle, AlertCircle } from 'lucide-react'

interface ResetPasswordProps {
  onSubmit: (newPassword: string) => Promise<void>
  onSuccess: () => void
}

type PasswordStrength = 'weak' | 'medium' | 'strong'

interface PasswordRequirement {
  label: string
  test: (password: string) => boolean
}

const passwordRequirements: PasswordRequirement[] = [
  { label: 'At least 8 characters', test: (p) => p.length >= 8 },
  { label: 'Contains uppercase letter', test: (p) => /[A-Z]/.test(p) },
  { label: 'Contains lowercase letter', test: (p) => /[a-z]/.test(p) },
  { label: 'Contains number', test: (p) => /[0-9]/.test(p) },
]

export function ResetPassword({ onSubmit, onSuccess }: ResetPasswordProps) {
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [showConfirmPassword, setShowConfirmPassword] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)

  // Calculate password strength
  const passwordStrength = useMemo((): { strength: PasswordStrength; score: number } => {
    if (!password) return { strength: 'weak', score: 0 }

    const metRequirements = passwordRequirements.filter(req => req.test(password)).length
    const score = (metRequirements / passwordRequirements.length) * 100

    if (score >= 75) return { strength: 'strong', score }
    if (score >= 50) return { strength: 'medium', score }
    return { strength: 'weak', score }
  }, [password])

  const getStrengthColor = (strength: PasswordStrength) => {
    switch (strength) {
      case 'strong': return 'hsl(var(--success))'
      case 'medium': return '#f59e0b'
      case 'weak': return 'hsl(var(--destructive))'
    }
  }

  const getStrengthText = (strength: PasswordStrength) => {
    switch (strength) {
      case 'strong': return 'Strong'
      case 'medium': return 'Medium'
      case 'weak': return 'Weak'
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    // Validate password requirements
    const unmetRequirements = passwordRequirements.filter(req => !req.test(password))
    if (unmetRequirements.length > 0) {
      setError(`Password must meet all requirements`)
      return
    }

    // Validate passwords match
    if (password !== confirmPassword) {
      setError('Passwords do not match')
      return
    }

    setLoading(true)

    try {
      await onSubmit(password)
      setSuccess(true)
      // Wait a moment then redirect to sign in
      setTimeout(() => {
        onSuccess()
      }, 2000)
    } catch (err) {
      console.error('Password reset error:', err)
      
      if (err instanceof Error) {
        if (err.message.includes('token') || err.message.includes('expired')) {
          setError('This reset link has expired. Please request a new password reset.')
        } else {
          setError(err.message || 'Failed to reset password. Please try again.')
        }
      } else {
        setError('Failed to reset password. Please try again.')
      }
    } finally {
      setLoading(false)
    }
  }

  if (success) {
    return (
      <div className="w-full max-w-md mx-auto p-8">
        <div className="text-center space-y-6">
          <div 
            className="w-16 h-16 mx-auto rounded-full flex items-center justify-center"
            style={{
              backgroundColor: 'hsl(var(--primary) / 0.15)',
            }}
          >
            <CheckCircle className="w-8 h-8" style={{ color: 'hsl(var(--primary))' }} />
          </div>
          
          <div className="space-y-2">
            <h2 className="text-2xl font-semibold" style={{ color: 'hsl(var(--foreground))' }}>
              Password Reset Complete!
            </h2>
            <p className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
              Your password has been successfully updated. Redirecting to sign in...
            </p>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="w-full max-w-md mx-auto p-8">
      <div className="space-y-6">
        {/* Header */}
        <div className="space-y-2 text-center">
          <h2 className="text-2xl font-semibold" style={{ color: 'hsl(var(--foreground))' }}>
            Set New Password
          </h2>
          <p className="text-sm" style={{ color: 'hsl(var(--muted-foreground))' }}>
            Choose a strong password for your account
          </p>
        </div>

        {/* Error Message */}
        {error && (
          <div 
            className="p-4 rounded-lg border flex items-start gap-3"
            style={{
              backgroundColor: 'hsl(var(--destructive) / 0.1)',
              borderColor: 'hsl(var(--destructive) / 0.3)',
            }}
          >
            <AlertCircle className="w-5 h-5 flex-shrink-0 mt-0.5" style={{ color: 'hsl(var(--destructive))' }} />
            <p className="text-sm" style={{ color: 'hsl(var(--destructive))' }}>
              {error}
            </p>
          </div>
        )}

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-4">
          {/* New Password */}
          <div className="space-y-2">
            <label 
              htmlFor="password" 
              className="text-sm font-medium"
              style={{ color: 'hsl(var(--foreground))' }}
            >
              New Password
            </label>
            <div className="relative">
              <input
                id="password"
                type={showPassword ? 'text' : 'password'}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="••••••••"
                disabled={loading}
                className="w-full px-4 pr-10 py-2 rounded-lg border-2 transition-all outline-none disabled:opacity-50"
                style={{
                  backgroundColor: 'hsl(var(--background))',
                  borderColor: 'hsl(var(--border))',
                  color: 'hsl(var(--foreground))',
                }}
                onFocus={(e) => {
                  e.currentTarget.style.borderColor = 'hsl(var(--primary))'
                }}
                onBlur={(e) => {
                  e.currentTarget.style.borderColor = 'hsl(var(--border))'
                }}
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-3 top-1/2 -translate-y-1/2"
                style={{ color: 'hsl(var(--muted-foreground))' }}
              >
                {showPassword ? <EyeOff className="w-5 h-5" /> : <Eye className="w-5 h-5" />}
              </button>
            </div>

            {/* Password Strength Indicator */}
            {password && (
              <div className="space-y-2">
                <div className="flex items-center justify-between text-xs">
                  <span style={{ color: 'hsl(var(--muted-foreground))' }}>Password strength:</span>
                  <span style={{ color: getStrengthColor(passwordStrength.strength), fontWeight: 600 }}>
                    {getStrengthText(passwordStrength.strength)}
                  </span>
                </div>
                <div className="h-2 bg-muted rounded-full overflow-hidden">
                  <div
                    className="h-full transition-all duration-300 rounded-full"
                    style={{
                      width: `${passwordStrength.score}%`,
                      backgroundColor: getStrengthColor(passwordStrength.strength),
                    }}
                  />
                </div>
              </div>
            )}
          </div>

          {/* Confirm Password */}
          <div className="space-y-2">
            <label 
              htmlFor="confirmPassword" 
              className="text-sm font-medium"
              style={{ color: 'hsl(var(--foreground))' }}
            >
              Confirm Password
            </label>
            <div className="relative">
              <input
                id="confirmPassword"
                type={showConfirmPassword ? 'text' : 'password'}
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                placeholder="••••••••"
                disabled={loading}
                className="w-full px-4 pr-10 py-2 rounded-lg border-2 transition-all outline-none disabled:opacity-50"
                style={{
                  backgroundColor: 'hsl(var(--background))',
                  borderColor: 'hsl(var(--border))',
                  color: 'hsl(var(--foreground))',
                }}
                onFocus={(e) => {
                  e.currentTarget.style.borderColor = 'hsl(var(--primary))'
                }}
                onBlur={(e) => {
                  e.currentTarget.style.borderColor = 'hsl(var(--border))'
                }}
              />
              <button
                type="button"
                onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                className="absolute right-3 top-1/2 -translate-y-1/2"
                style={{ color: 'hsl(var(--muted-foreground))' }}
              >
                {showConfirmPassword ? <EyeOff className="w-5 h-5" /> : <Eye className="w-5 h-5" />}
              </button>
            </div>
            {confirmPassword && password !== confirmPassword && (
              <p className="text-xs" style={{ color: 'hsl(var(--destructive))' }}>
                Passwords do not match
              </p>
            )}
          </div>

          {/* Password Requirements */}
          <div 
            className="p-3 rounded-lg border space-y-2"
            style={{
              backgroundColor: 'hsl(var(--muted) / 0.3)',
              borderColor: 'hsl(var(--border))',
            }}
          >
            <p className="text-xs font-medium" style={{ color: 'hsl(var(--foreground))' }}>
              Password must contain:
            </p>
            {passwordRequirements.map((req, index) => {
              const isMet = password ? req.test(password) : false
              return (
                <div key={index} className="flex items-center gap-2">
                  {isMet ? (
                    <Check className="w-3.5 h-3.5" style={{ color: 'hsl(var(--success))' }} />
                  ) : (
                    <X className="w-3.5 h-3.5" style={{ color: 'hsl(var(--muted-foreground))' }} />
                  )}
                  <span 
                    className="text-xs"
                    style={{ 
                      color: isMet ? 'hsl(var(--success))' : 'hsl(var(--muted-foreground))'
                    }}
                  >
                    {req.label}
                  </span>
                </div>
              )
            })}
          </div>

          {/* Submit Button */}
          <button
            type="submit"
            disabled={loading || !password || !confirmPassword || password !== confirmPassword}
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
            {loading ? 'Resetting Password...' : 'Reset Password'}
          </button>
        </form>
      </div>
    </div>
  )
}
