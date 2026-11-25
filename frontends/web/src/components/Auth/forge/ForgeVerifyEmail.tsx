// /auth/verify?userID=…&code=… with forge sign-in: the page a "confirm your
// address" email opens (forge pkg/devidp, the broker's verify template).
//
// Inert until the button is pressed, so a mail scanner opening the link
// spends nothing. Confirming issues NO session by itself: the person then
// signs in as usual, or — an account made without a password, where passkeys
// are offered — adds a passkey here and is signed in with it. When the
// password chosen at sign-up could not be saved, the broker hands back a
// reset code and the person chooses another right here.

import { useState } from 'react'
import {
  AuthError as NativeAuthError,
  passkeySignIn,
  registerPasskey,
  verifyEmail,
} from '@/lib/auth/native-login'
import { forgeForgotPasswordPath } from '@/lib/paths'
import { AuthError, AuthLayout } from '../../AuthLayout'
import { ForgeAuthCard, ForgeLink, ForgeNotice, ForgeOutlineButton, ForgePrimaryButton } from './ForgeAuthParts'
import { ForgeResetPasswordForm } from './ForgeResetPassword'
import { installAndLand, queryParam, rememberedReturnTo, signInHref } from './forgeAuthLib'
import { landAt } from './forgeLanding'

// "password": the sign-up password was not saved and a reset code came back;
// "noPassword": it was not saved and none did.
type VerifyState = 'ready' | 'verified' | 'enroll' | 'password' | 'noPassword'

export function ForgeVerifyEmail({ userID, code }: { userID: string | null; code: string | null }) {
  const [state, setState] = useState<VerifyState>('ready')
  const [reset, setReset] = useState<{ userID: string; code: string } | null>(null)
  // Methods set before the address was confirmed were removed: nothing proved
  // they were the owner's.
  const [removed, setRemoved] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (!userID || !code) {
    return (
      <ForgeAuthCard
        title="Confirm your email"
        description="This link is incomplete. Open the link from the email again, or sign in to ask for a new one."
      >
        <div className="text-center">
          <ForgeLink href={signInHref()}>Go to sign in</ForgeLink>
        </div>
      </ForgeAuthCard>
    )
  }

  const act = async (action: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (err) {
      setError(err instanceof NativeAuthError ? err.message : 'That didn’t work. Please try again.')
    } finally {
      setBusy(false)
    }
  }

  if (state === 'password' && reset) {
    return (
      <AuthLayout>
        <ForgeResetPasswordForm
          userID={reset.userID}
          code={reset.code}
          intro={
            <ForgeNotice tone="info">
              <p>Your email is confirmed, but your password couldn’t be saved. Choose a password to finish.</p>
            </ForgeNotice>
          }
        />
      </AuthLayout>
    )
  }

  return (
    <ForgeAuthCard
      title="Confirm your email"
      description={state === 'ready' ? 'Press the button to confirm this address belongs to you.' : undefined}
    >
      {error && <AuthError message={error} />}

      {state === 'ready' && (
        <ForgePrimaryButton
          type="button"
          busy={busy}
          onClick={() =>
            void act(async () => {
              const result = await verifyEmail(userID, code)
              setRemoved(result.credentialsReset === true)
              if (result.password === 'not_set') {
                setReset(result.reset ?? null)
                setState(result.reset ? 'password' : 'noPassword')
                return
              }
              setState(result.enroll ? 'enroll' : 'verified')
            })
          }
        >
          {busy ? 'Confirming…' : 'Confirm my email'}
        </ForgePrimaryButton>
      )}

      {(state === 'verified' || state === 'enroll') && !removed && (
        <ForgeNotice tone="success">
          <p>
            {state === 'enroll'
              ? 'Your email is confirmed. Create a passkey to sign in with this device.'
              : 'Your email is confirmed. You can sign in now.'}
          </p>
        </ForgeNotice>
      )}

      {(state === 'verified' || state === 'enroll') && removed && (
        <ForgeNotice tone="info">
          <p>
            Your email is confirmed. For your security, sign-in methods set before it was confirmed have been
            removed.
          </p>
          <p>
            <ForgeLink href={forgeForgotPasswordPath()}>Choose a password</ForgeLink>
            {state === 'enroll' ? ', or create a passkey below.' : '.'}
          </p>
        </ForgeNotice>
      )}

      {state === 'noPassword' && (
        <ForgeNotice tone="info">
          <p>
            Your email is confirmed, but your password couldn’t be saved. Use “Forgot password?” on the sign-in
            screen to choose one.
          </p>
        </ForgeNotice>
      )}

      {state === 'verified' && (
        <ForgePrimaryButton type="button" onClick={() => landAt(signInHref())}>
          Sign in
        </ForgePrimaryButton>
      )}

      {state === 'enroll' && (
        <ForgeOutlineButton
          busy={busy}
          onClick={() =>
            void act(async () => {
              await registerPasskey()
              const identity = await passkeySignIn()
              if (!(await installAndLand(identity, rememberedReturnTo(true)))) landAt(signInHref())
            })
          }
        >
          {busy ? 'Waiting for your passkey…' : 'Create a passkey'}
        </ForgeOutlineButton>
      )}

      {state !== 'verified' && (
        <div className="text-center">
          <ForgeLink href={signInHref()}>Go to sign in</ForgeLink>
        </div>
      )}
    </ForgeAuthCard>
  )
}

/** The /auth/verify page: the link's userID and code from the query string. */
export function ForgeVerifyEmailPage() {
  return <ForgeVerifyEmail userID={queryParam('userID')} code={queryParam('code')} />
}
