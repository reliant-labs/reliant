export type PasswordStrength = 'weak' | 'medium' | 'strong'

export interface PasswordRequirement {
  label: string
  test: (password: string) => boolean
}

export const passwordRequirements: PasswordRequirement[] = [
  { label: 'At least 8 characters', test: (p) => p.length >= 8 },
  { label: 'Contains uppercase letter', test: (p) => /[A-Z]/.test(p) },
  { label: 'Contains lowercase letter', test: (p) => /[a-z]/.test(p) },
  { label: 'Contains number', test: (p) => /[0-9]/.test(p) },
  { label: 'Contains special character', test: (p) => /[^A-Za-z0-9]/.test(p) },
]

/**
 * Calculates password strength based on how many requirements are met
 */
export function calculatePasswordStrength(password: string): { strength: PasswordStrength; score: number } {
  if (!password) return { strength: 'weak', score: 0 }

  const score = passwordRequirements.filter(req => req.test(password)).length

  if (score <= 2) return { strength: 'weak', score }
  if (score <= 3) return { strength: 'medium', score }
  return { strength: 'strong', score }
}

/**
 * Validates that a password meets all requirements
 */
export function validatePassword(password: string): { valid: boolean; unmetRequirements: PasswordRequirement[] } {
  const unmetRequirements = passwordRequirements.filter(req => !req.test(password))
  return {
    valid: unmetRequirements.length === 0,
    unmetRequirements,
  }
}

/**
 * Gets color class for password strength
 */
export function getStrengthColor(strength: PasswordStrength): string {
  switch (strength) {
    case 'weak': return 'bg-red-500'
    case 'medium': return 'bg-yellow-500'
    case 'strong': return 'bg-green-500'
  }
}

/**
 * Gets text label for password strength
 */
export function getStrengthText(strength: PasswordStrength): string {
  switch (strength) {
    case 'weak': return 'Weak'
    case 'medium': return 'Medium'
    case 'strong': return 'Strong'
  }
}

