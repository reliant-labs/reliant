import type { ReactNode } from 'react'
import { GradientBackground } from './GradientBackground'
import { BrandMark } from './icons/BrandMark'

/**
 * The full-screen shell every unauthenticated/identity surface renders inside:
 * gradient backdrop, an Electron drag strip, and a centered card. Shared so the
 * sign-in screen and the account-upgrade screen cannot drift apart visually.
 */
export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-screen flex flex-col bg-background relative overflow-hidden">
      <GradientBackground />
      <div
        className="drag-region h-12 flex-shrink-0"
        style={{ WebkitAppRegion: 'drag' } as React.CSSProperties}
      />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl">
          {children}
        </div>
      </div>
    </div>
  )
}

/**
 * Brand lockup + title/description used at the top of an auth card.
 */
export function AuthHeader({
  title,
  description,
}: {
  title: string
  description?: string
}) {
  return (
    <div className="flex flex-col items-center gap-4">
      <div className="flex items-center gap-3">
        <BrandMark className="h-8 w-8" />
        <h1 className="text-3xl font-bold">Reliant</h1>
      </div>
      <div className="text-center space-y-1">
        <h2 className="text-xl font-semibold">{title}</h2>
        {description && (
          <p className="text-sm text-muted-foreground">{description}</p>
        )}
      </div>
    </div>
  )
}

/**
 * Inline failure message. Kept in one place so every auth surface reports
 * errors identically.
 */
export function AuthError({ message }: { message: string }) {
  return (
    <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-4">
      <p className="text-sm text-red-800 dark:text-red-200">{message}</p>
    </div>
  )
}

/**
 * Horizontal rule with centered label, used to separate the email form from
 * the OAuth providers.
 */
export function AuthDivider({ label }: { label: string }) {
  return (
    <div className="relative">
      <div className="absolute inset-0 flex items-center">
        <div className="w-full border-t border-border" />
      </div>
      <div className="relative flex justify-center text-sm">
        <span className="px-2 bg-background text-muted-foreground">{label}</span>
      </div>
    </div>
  )
}

export function AuthLegalLinks() {
  return (
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
  )
}
