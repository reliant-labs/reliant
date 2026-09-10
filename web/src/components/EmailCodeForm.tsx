import { useEffect, useState, type ReactNode } from 'react'
import { KeyRound } from 'lucide-react'
import { AuthError } from './AuthLayout'
import { CheckSpamNote } from './CheckSpamNote'
import {
  describeAuthError,
  isRateLimitError,
  retryAfterSeconds,
} from '@/lib/authErrors'

/**
 * The 6-digit code screen, once, for every flow that mails a code.
 *
 * There were four near-identical copies of this markup (sign-up verification,
 * password reset, anonymous-upgrade, billing email), and they had already
 * drifted: three hand-rolled their own "after N seconds" regex instead of
 * using `lib/authErrors`, two showed a rate-limit as a red error — which reads
 * as a broken account rather than "wait a moment" — and only some of them said
 * to check the spam folder, which is the single sentence that decides whether
 * a stuck user gives up.
 *
 * So this owns the parts that are the same everywhere and were wrong somewhere:
 * numeric-only input capped at six, `one-time-code` so the OS offers the code
 * from the notification, a resend cooldown seeded from GoTrue's own quoted
 * wait, rate limits rendered as NOTICE and never as failure, and the spam note
 * next to the address — this component always names an email, so the note is
 * unconditional rather than something each caller must remember.
 *
 * It deliberately owns no network call and no navigation. `onVerify` and
 * `onResend` differ by flow in ways that are not cosmetic — GoTrue resolves a
 * signup token, an email-change token and a magic-link token from different
 * columns, and picking the wrong one fails with "expired or invalid" on a code
 * that is neither. Keeping that choice at the call site is what stops this
 * component from growing a `type` prop that quietly gets it wrong.
 */
interface EmailCodeFormProps {
  /** The address the code went to. Shown to the user; never edited here. */
  email: string
  /** Rejects to report a bad code. Resolves once the caller has navigated. */
  onVerify: (code: string) => Promise<void>
  /** Sends another code. Omit to hide the resend affordance entirely. */
  onResend?: () => Promise<void>
  /**
   * Seconds to disable resend for on mount. The send that brought the user
   * here already started GoTrue's clock, so 0 would offer a button that can
   * only fail.
   */
  initialCooldown?: number
  submitLabel?: string
  /** Replaces the default "We sent a 6-digit code to <email>" sentence. */
  description?: ReactNode
  /** Extra actions under the form — "use a different email", "back", etc. */
  footer?: ReactNode
  idPrefix?: string
  autoFocus?: boolean
}

export function EmailCodeForm({
  email,
  onVerify,
  onResend,
  initialCooldown = 60,
  submitLabel = 'Continue',
  description,
  footer,
  idPrefix = 'email-code',
  autoFocus = true,
}: EmailCodeFormProps) {
  const [code, setCode] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [cooldown, setCooldown] = useState(initialCooldown)

  useEffect(() => {
    if (cooldown <= 0) return
    const timer = setTimeout(() => setCooldown((seconds) => seconds - 1), 1000)
    return () => clearTimeout(timer)
  }, [cooldown])

  const inputId = `${idPrefix}-code`

  const handleVerify = async (event: React.FormEvent) => {
    event.preventDefault()
    setError(null)
    setNotice(null)

    if (code.length !== 6) {
      setError('Enter the 6-digit code from your email.')
      return
    }

    setSubmitting(true)
    try {
      await onVerify(code)
    } catch (err) {
      setError(describeAuthError(err, 'That code didn’t work. Please try again.'))
    } finally {
      setSubmitting(false)
    }
  }

  const handleResend = async () => {
    if (!onResend || cooldown > 0 || submitting) return

    setError(null)
    setNotice(null)
    setSubmitting(true)
    try {
      await onResend()
      setNotice(`We sent another code to ${email}.`)
      setCooldown(60)
    } catch (err) {
      // A throttle is not the user's fault and not a failure of their account.
      // Say how long to wait, in the neutral notice box, and start the clock so
      // the button cannot be mashed into a longer ban.
      if (isRateLimitError(err)) {
        setNotice(describeAuthError(err, 'Please wait a moment and try again.'))
        setCooldown(retryAfterSeconds(describeAuthError(err, '')) ?? 60)
        return
      }
      setError(describeAuthError(err, 'We couldn’t send another code.'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form className="space-y-5" onSubmit={handleVerify}>
      {error && <AuthError message={error} />}

      {notice && (
        <div className="rounded-lg border border-border bg-muted p-4">
          <p className="text-sm text-muted-foreground">{notice}</p>
        </div>
      )}

      <div className="space-y-1">
        <p className="text-sm text-muted-foreground" data-testid="email-code-description">
          {description ?? (
            <>
              We sent a 6-digit code to{' '}
              <span className="font-medium text-foreground">{email}</span>. Enter
              it below, or click the link in the same email.
            </>
          )}
        </p>
        <CheckSpamNote className="text-xs text-muted-foreground" />
      </div>

      <div>
        <label htmlFor={inputId} className="block text-sm font-medium mb-1.5">
          Verification code
        </label>
        <div className="relative">
          <KeyRound className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-muted-foreground" />
          <input
            id={inputId}
            name="code"
            type="text"
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus={autoFocus}
            value={code}
            onChange={(event) => {
              setCode(event.target.value.replace(/\D/g, '').slice(0, 6))
              if (error) setError(null)
            }}
            disabled={submitting}
            maxLength={6}
            placeholder="000000"
            className="block w-full pl-10 pr-4 py-2.5 border border-border rounded-lg bg-transparent text-center text-2xl tracking-widest focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors disabled:opacity-50"
          />
        </div>
      </div>

      <button
        type="submit"
        disabled={submitting || code.length !== 6}
        className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {submitting ? 'Verifying...' : submitLabel}
      </button>

      {onResend && (
        <div className="text-center">
          {cooldown > 0 ? (
            <p className="text-sm text-muted-foreground">
              You can request another code in {cooldown}s.
            </p>
          ) : (
            <button
              type="button"
              onClick={() => void handleResend()}
              disabled={submitting}
              className="text-sm font-medium text-primary hover:text-primary/80 transition-colors disabled:opacity-50"
            >
              Send another code
            </button>
          )}
        </div>
      )}

      {footer}
    </form>
  )
}
