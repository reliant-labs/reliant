// /auth/magic?code=… with forge sign-in: the page an emailed sign-in link
// opens (forge pkg/devidp magicTemplate).
//
// Opening the link does NOT sign anyone in. Mail scanners open every link in
// an email; a page that signed in on load would let one spend the single-use
// code before the person clicked. Nothing happens until the button is
// pressed. The link also works only in the browser that asked for it (the
// broker bound the request to a cookie there); opened anywhere else, the page
// says so and asks for the code to be typed into the waiting tab.

import { useState } from 'react'
import { AuthError as NativeAuthError, consumeMagicLink } from '@/lib/auth/native-login'
import { AuthError } from '../../AuthLayout'
import { ForgeAuthCard, ForgeField, ForgeLink, ForgeNotice, ForgePrimaryButton, forgeInputClass } from './ForgeAuthParts'
import { installAndLand, queryParam, rememberedReturnTo, signInHref } from './forgeAuthLib'

export function ForgeMagicLink({ code: linked }: { code: string | null }) {
  const [code, setCode] = useState(linked ?? '')
  const [error, setError] = useState<string | null>(null)
  const [otherBrowser, setOtherBrowser] = useState(false)
  const [busy, setBusy] = useState(false)

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const identity = await consumeMagicLink(code.trim())
      if (await installAndLand(identity, rememberedReturnTo(true))) return
      setError('That sign-in didn’t complete. Please try again.')
    } catch (err) {
      const authErr = err instanceof NativeAuthError ? err : null
      setOtherBrowser(authErr?.code === 'magic_other_browser')
      setError(authErr ? authErr.message : 'That sign-in didn’t complete. Please try again.')
    }
    setBusy(false)
  }

  if (otherBrowser) {
    return (
      <ForgeAuthCard title="Open this link where you asked for it">
        <ForgeNotice tone="info">
          <p>This link only works in the browser where you asked for it.</p>
          <p>Go back to that browser and type the code from the email there, or ask for a new link here.</p>
        </ForgeNotice>
        <div className="text-center">
          <ForgeLink href={signInHref()}>Back to sign in</ForgeLink>
        </div>
      </ForgeAuthCard>
    )
  }

  return (
    <ForgeAuthCard
      title="Sign in"
      description={
        linked
          ? 'Press the button to finish signing in.'
          : 'Type the code from the email. Never share it: Reliant will never ask you for it.'
      }
    >
      <form className="space-y-5" onSubmit={onSubmit} aria-busy={busy}>
        {error && <AuthError message={error} />}
        {!linked && (
          <ForgeField id="magic-code" label="Code from the email">
            <input
              id="magic-code"
              name="code"
              inputMode="numeric"
              autoComplete="one-time-code"
              autoFocus
              required
              value={code}
              onChange={(e) => setCode(e.target.value)}
              disabled={busy}
              className={forgeInputClass}
            />
          </ForgeField>
        )}
        <ForgePrimaryButton busy={busy}>{busy ? 'Signing in…' : 'Sign in'}</ForgePrimaryButton>
        <div className="text-center">
          <ForgeLink href={signInHref()}>Back to sign in</ForgeLink>
        </div>
      </form>
    </ForgeAuthCard>
  )
}

/** The /auth/magic page: the link's code from the query string. */
export function ForgeMagicLinkPage() {
  return <ForgeMagicLink code={queryParam('code')} />
}
