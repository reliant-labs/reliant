// The pieces every forge sign-in screen is built from, in the sign-in
// screen's own look (AuthScreen.tsx, AuthLayout.tsx): the same card, header,
// inputs and buttons, so a person cannot tell which provider signs them in.

import type { ReactNode } from 'react'
import { Check, CheckCircle2, Info, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  activeRules,
  describeRule,
  unmetRules,
  type PasswordPolicy,
  type PasswordRule,
} from '@/lib/auth/native-login'
import { AuthHeader, AuthLayout } from '../../AuthLayout'

/** A whole auth page: the shared layout, header and body. */
export function ForgeAuthCard({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children?: ReactNode
}) {
  return (
    <AuthLayout>
      <ForgeAuthPanel title={title} description={description}>
        {children}
      </ForgeAuthPanel>
    </AuthLayout>
  )
}

/** The inside of a card, for a screen that renders inside a layout someone else drew. */
export function ForgeAuthPanel({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children?: ReactNode
}) {
  return (
    <div className="p-8 space-y-6">
      <AuthHeader title={title} description={description} />
      {children}
    </div>
  )
}

/** "Check your email", "confirmed": an icon carries the tone, never a fill. */
export function ForgeNotice({ tone, children }: { tone: 'success' | 'info'; children: ReactNode }) {
  const Icon = tone === 'success' ? CheckCircle2 : Info
  return (
    <div role="status" className="flex gap-3 rounded-xl border border-border/60 bg-background p-4">
      <Icon
        aria-hidden="true"
        className={cn('h-4 w-4 flex-shrink-0 mt-0.5', tone === 'success' ? 'text-success-ink' : 'text-primary-ink')}
      />
      <div className="space-y-2 text-sm text-foreground">{children}</div>
    </div>
  )
}

export const forgeInputClass =
  'block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent focus:outline-none focus:ring-2 focus:ring-ring focus:border-primary transition-colors'

export function ForgeField({
  id,
  label,
  children,
}: {
  id: string
  label: string
  children: ReactNode
}) {
  return (
    <div>
      <label htmlFor={id} className="block text-sm font-medium mb-1.5">
        {label}
      </label>
      {children}
    </div>
  )
}

/** The one filled button of a screen. */
export function ForgePrimaryButton({
  children,
  busy = false,
  disabled = false,
  type = 'submit',
  onClick,
}: {
  children: ReactNode
  busy?: boolean
  disabled?: boolean
  type?: 'submit' | 'button'
  onClick?: () => void
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={busy || disabled}
      aria-busy={busy}
      className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
    >
      {children}
    </button>
  )
}

/** A secondary, outlined action (a passkey, an emailed link). */
export function ForgeOutlineButton({
  children,
  busy = false,
  disabled = false,
  onClick,
}: {
  children: ReactNode
  busy?: boolean
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy || disabled}
      aria-busy={busy}
      className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-foreground bg-transparent hover:bg-foreground/5 focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
    >
      {children}
    </button>
  )
}

/** A quiet in-place toggle ("Use a different email"). */
export function ForgeTextButton({ children, onClick, disabled }: { children: ReactNode; onClick: () => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="text-sm text-muted-foreground hover:text-foreground transition-colors"
    >
      {children}
    </button>
  )
}

/** A link-styled action that moves somewhere ("Forgot password?", "Back to sign in"). */
export function ForgeLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} className="text-sm text-primary-ink hover:underline">
      {children}
    </a>
  )
}

/**
 * The broker's password policy as a checklist. Guidance only: the identity
 * provider checks the password regardless. A rule the server reported unmet,
 * or one still unmet after a submit, reads as a failure; the rest stay quiet
 * until met.
 */
export function ForgePasswordRequirements({
  id,
  policy,
  password,
  serverUnmet,
  attempted,
}: {
  id: string
  policy: PasswordPolicy | null
  password: string
  serverUnmet: PasswordRule[]
  attempted: boolean
}) {
  if (!policy) return null
  const rules = activeRules(policy)
  if (rules.length === 0) return null
  const unmet = new Set<PasswordRule>([...unmetRules(policy, password), ...serverUnmet])
  return (
    <ul id={id} className="space-y-1 mt-2" aria-label="Password requirements">
      {rules.map((rule) => {
        const met = !unmet.has(rule)
        const failing = !met && (attempted || serverUnmet.includes(rule))
        return (
          <li key={rule} className="flex items-center gap-2 text-xs">
            {met ? (
              <Check aria-hidden="true" className="h-3 w-3 flex-shrink-0 text-success-ink" />
            ) : (
              <X
                aria-hidden="true"
                className={cn('h-3 w-3 flex-shrink-0', failing ? 'text-destructive-ink' : 'text-muted-foreground')}
              />
            )}
            <span className={failing ? 'text-destructive-ink' : 'text-muted-foreground'}>
              {describeRule(policy, rule)}
              <span className="sr-only">{met ? ' (met)' : ' (not met)'}</span>
            </span>
          </li>
        )
      })}
    </ul>
  )
}
