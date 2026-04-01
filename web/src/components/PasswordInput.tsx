import { useState, useMemo } from 'react'
import { Eye, EyeOff, Check, X } from 'lucide-react'
import {
  passwordRequirements,
  calculatePasswordStrength,
  getStrengthColor,
  getStrengthText,
} from '../utils/passwordValidation'

interface PasswordInputProps {
  id?: string
  name?: string
  label?: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
  autoComplete?: string
  required?: boolean
  disabled?: boolean
  showStrengthIndicator?: boolean
  showRequirements?: boolean
  minLength?: number
  className?: string
}

export function PasswordInput({
  id = 'password',
  name = 'password',
  label = 'Password',
  value,
  onChange,
  placeholder = '••••••••',
  autoComplete = 'new-password',
  required = false,
  disabled = false,
  showStrengthIndicator = true,
  showRequirements = true,
  minLength = 6,
  className = '',
}: PasswordInputProps) {
  const [showPassword, setShowPassword] = useState(false)

  const passwordStrength = useMemo(() => calculatePasswordStrength(value), [value])

  return (
    <div className={className}>
      <label htmlFor={id} className="block text-sm font-medium mb-1.5">
        {label}
      </label>
      <div className="relative">
        <input
          id={id}
          name={name}
          type={showPassword ? 'text' : 'password'}
          autoComplete={autoComplete}
          required={required}
          minLength={minLength}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="block w-full px-3 py-2.5 pr-10 border border-border rounded-lg focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
          style={{ backgroundColor: 'transparent' }}
          placeholder={placeholder}
          disabled={disabled}
        />
        <button
          type="button"
          onClick={() => setShowPassword(!showPassword)}
          className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
          tabIndex={-1}
          disabled={disabled}
        >
          {showPassword ? (
            <EyeOff className="h-4 w-4" />
          ) : (
            <Eye className="h-4 w-4" />
          )}
        </button>
      </div>

      {/* Password Strength Indicator */}
      {showStrengthIndicator && value && (
        <div className="space-y-2 mt-2">
          <div className="flex items-center justify-between text-xs">
            <span className="text-muted-foreground">Password strength:</span>
            <span className={`font-medium ${
              passwordStrength.strength === 'weak' ? 'text-red-500' :
              passwordStrength.strength === 'medium' ? 'text-yellow-500' :
              'text-green-500'
            }`}>
              {getStrengthText(passwordStrength.strength)}
            </span>
          </div>
          <div className="flex gap-1">
            {[1, 2, 3, 4, 5].map((bar) => (
              <div
                key={bar}
                className={`h-1 flex-1 rounded-full transition-colors ${
                  bar <= passwordStrength.score
                    ? getStrengthColor(passwordStrength.strength)
                    : 'bg-border'
                }`}
              />
            ))}
          </div>
        </div>
      )}

      {/* Password Requirements Checklist */}
      {showRequirements && (
        <div className="space-y-1 mt-2">
          {passwordRequirements.map((requirement, index) => {
            const met = requirement.test(value)
            return (
              <div key={index} className="flex items-center gap-2 text-xs">
                {met ? (
                  <Check className="h-3 w-3 text-green-500 flex-shrink-0" />
                ) : (
                  <X className="h-3 w-3 text-red-500 flex-shrink-0" />
                )}
                <span className={met ? 'text-green-600 dark:text-green-400' : 'text-muted-foreground'}>
                  {requirement.label}
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

interface ConfirmPasswordInputProps {
  id?: string
  name?: string
  label?: string
  value: string
  password: string
  onChange: (value: string) => void
  placeholder?: string
  autoComplete?: string
  required?: boolean
  disabled?: boolean
  className?: string
}

export function ConfirmPasswordInput({
  id = 'confirmPassword',
  name = 'confirmPassword',
  label = 'Confirm Password',
  value,
  password,
  onChange,
  placeholder = '••••••••',
  autoComplete = 'new-password',
  required = false,
  disabled = false,
  className = '',
}: ConfirmPasswordInputProps) {
  const [showPassword, setShowPassword] = useState(false)
  const passwordsMatch = value === password

  return (
    <div className={className}>
      <label htmlFor={id} className="block text-sm font-medium mb-1.5">
        {label}
      </label>
      <div className="relative">
        <input
          id={id}
          name={name}
          type={showPassword ? 'text' : 'password'}
          autoComplete={autoComplete}
          required={required}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className={`block w-full px-3 py-2.5 pr-10 border rounded-lg focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors ${
            value && !passwordsMatch ? 'border-red-500' : 'border-border'
          }`}
          style={{ backgroundColor: 'transparent' }}
          placeholder={placeholder}
          disabled={disabled}
        />
        <button
          type="button"
          onClick={() => setShowPassword(!showPassword)}
          className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
          tabIndex={-1}
          disabled={disabled}
        >
          {showPassword ? (
            <EyeOff className="h-4 w-4" />
          ) : (
            <Eye className="h-4 w-4" />
          )}
        </button>
      </div>
      {value && !passwordsMatch && (
        <p className="text-xs text-red-500 mt-1">Passwords do not match</p>
      )}
    </div>
  )
}

