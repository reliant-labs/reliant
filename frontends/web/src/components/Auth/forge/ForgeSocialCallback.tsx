// /auth/social/callback?id=…&token=… with forge sign-in: where the identity
// provider sends the browser back after Google or GitHub (forge pkg/devidp
// HandleSocialStart). It receives no authorization code, only a handle the
// broker checks against the sign-in THIS browser started (a binding cookie),
// so the page is public and posts the pair straight back
// (POST /auth/social/callback).
//
// Not reliant's /auth/callback, which is Supabase's OAuth return and keeps
// working in Supabase mode.

import { useEffect, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { AuthError as NativeAuthError, isVerifySent, socialComplete } from '@/lib/auth/native-login'
import { AuthError } from '../../AuthLayout'
import { CheckSpamNote } from '../../CheckSpamNote'
import { ForgeAuthCard, ForgeLink, ForgeNotice } from './ForgeAuthParts'
import { installAndLand, queryParam, safeReturnPath, signInHref } from './forgeAuthLib'
import { landAt } from './forgeLanding'

const INCOMPLETE = 'That sign-in didn’t complete. Please try again.'

/**
 * A provider LINKED to the signed-in account from settings (forge#2663,
 * POST /auth/account/links/start) comes back to this same callback, and the
 * broker answers that it linked rather than signed in. The session is the
 * one already in place: nothing to install, only a page to go back to.
 */
interface LinkedResult {
  linked: true
  provider?: string
  returnTo?: string
}

function isLinked(value: unknown): value is LinkedResult {
  return typeof value === 'object' && value !== null && (value as { linked?: unknown }).linked === true
}

/** The broker's reasons a provider round trip failed, as a person reads them. */
function describeFailure(err: unknown): string {
  if (!(err instanceof NativeAuthError)) return INCOMPLETE
  switch (err.code) {
    case 'social_taken':
      return 'That account is already connected to a different Reliant account. Sign in with it there, or use another account.'
    case 'social_failed':
      return 'The provider didn’t finish connecting. Please try again.'
    default:
      return err.message
  }
}

export function ForgeSocialCallback({ id, token }: { id: string | null; token: string | null }) {
  const incomplete = !id || !token
  const [error, setError] = useState<string | null>(incomplete ? INCOMPLETE : null)
  const [verifySent, setVerifySent] = useState<string | null>(null)
  const started = useRef(false)

  useEffect(() => {
    // Once: React's development double-invoke must not post the token twice.
    if (started.current || !id || !token) return
    started.current = true
    void socialComplete(id, token)
      .then(async (result) => {
        if (isVerifySent(result)) {
          setVerifySent(result.message)
          return
        }
        if (isLinked(result)) {
          landAt(safeReturnPath(result.returnTo))
          return
        }
        // The broker checked returnTo; it is checked again here, as any path
        // this page is handed (lib/returnTo).
        if (!(await installAndLand(result.identity, result.returnTo))) setError(INCOMPLETE)
      })
      .catch((err: unknown) => {
        setError(describeFailure(err))
      })
  }, [id, token])

  if (verifySent !== null) {
    return (
      <ForgeAuthCard title="Check your email">
        <ForgeNotice tone="success">
          <p>{verifySent}</p>
        </ForgeNotice>
        <CheckSpamNote />
        <div className="text-center">
          <ForgeLink href={signInHref()}>Back to sign in</ForgeLink>
        </div>
      </ForgeAuthCard>
    )
  }

  if (error !== null) {
    return (
      <ForgeAuthCard title="Sign in">
        <AuthError message={error} />
        <div className="text-center">
          <ForgeLink href={signInHref()}>Back to sign in</ForgeLink>
        </div>
      </ForgeAuthCard>
    )
  }

  return (
    <ForgeAuthCard title="Signing you in">
      <p role="status" className="flex items-center justify-center gap-3 text-sm text-muted-foreground">
        <Loader2 aria-hidden="true" className="h-4 w-4 flex-shrink-0 motion-safe:animate-spin" />
        One moment…
      </p>
    </ForgeAuthCard>
  )
}

/** The /auth/social/callback page: the provider round trip's id and token. */
export function ForgeSocialCallbackPage() {
  return <ForgeSocialCallback id={queryParam('id')} token={queryParam('token')} />
}
