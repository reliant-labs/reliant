import * as Sentry from '@sentry/react'
import { settingsGrpc } from '@/api/settings-grpc'
import { PollCopilotDeviceAuthResponse_Status } from '@/gen/reliant/v1/settings_pb'

/**
 * GitHub Copilot uses the OAuth 2.0 device-authorization flow rather than the
 * browser-redirect PKCE flow used by Codex/Claude. The user is shown a short
 * `userCode`, opens `verificationUri` (github.com/login/device) in a browser,
 * enters the code, and this module polls the backend until GitHub reports the
 * device as authorized (or the flow expires / is denied).
 */

type CopilotOAuthErrorCode =
  | 'start_failed'
  | 'timeout'
  | 'cancelled'
  | 'denied'
  | 'poll_error'

export type CopilotOAuthResult =
  | {
      ok: true
      message: string
    }
  | {
      ok: false
      errorCode: CopilotOAuthErrorCode
      message: string
    }

/** Device-authorization details surfaced to the UI once the flow has started. */
export interface CopilotDeviceCodeInfo {
  userCode: string
  verificationUri: string
  intervalSeconds: number
  expiresInSeconds: number
}

export interface CopilotOAuthOptions {
  signal?: AbortSignal
  /**
   * Invoked once the device code has been issued, so the UI can display the
   * `userCode` and open the verification URI. Polling begins immediately after.
   */
  onDeviceCode?: (info: CopilotDeviceCodeInfo) => void
  /** Invoked when the polling loop begins (after the device code is shown). */
  onPolling?: () => void
}

const errorResult = (
  errorCode: CopilotOAuthErrorCode,
  message: string,
): CopilotOAuthResult => ({
  ok: false,
  errorCode,
  message,
})

class AbortError extends Error {
  constructor() {
    super('cancelled')
    this.name = 'AbortError'
  }
}

/** Sleep that resolves early (rejecting) if the abort signal fires. */
function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new AbortError())
      return
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(new AbortError())
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * Run the Copilot device-authorization flow end to end: start, display the
 * user code (via `onDeviceCode`), then poll every `intervalSeconds` until the
 * status leaves PENDING or `expiresInSeconds` elapses.
 *
 * Resolves with `ok: true` on AUTHORIZED; resolves with `ok: false` and a
 * user-facing message on EXPIRED / DENIED / ERROR / cancellation.
 */
export async function runCopilotDeviceFlow(
  options: CopilotOAuthOptions = {},
): Promise<CopilotOAuthResult> {
  const { signal, onDeviceCode, onPolling } = options

  let start: Awaited<ReturnType<typeof settingsGrpc.startCopilotDeviceAuth>>
  try {
    start = await settingsGrpc.startCopilotDeviceAuth()
  } catch (error: any) {
    if (signal?.aborted) {
      return errorResult('cancelled', 'Sign-in cancelled.')
    }
    Sentry.captureException(error, {
      tags: { component: 'oauth', provider: 'copilot' },
      level: 'warning',
    })
    return errorResult(
      'start_failed',
      error?.message || 'Could not start GitHub Copilot sign-in. Please try again.',
    )
  }

  if (signal?.aborted) {
    return errorResult('cancelled', 'Sign-in cancelled.')
  }

  onDeviceCode?.({
    userCode: start.userCode,
    verificationUri: start.verificationUri,
    intervalSeconds: start.intervalSeconds,
    expiresInSeconds: start.expiresInSeconds,
  })

  // Honor the server-provided interval; clamp to a sane floor to avoid
  // hammering GitHub if the backend ever returns 0.
  const intervalMs = Math.max(start.intervalSeconds, 1) * 1000
  const deadline = Date.now() + Math.max(start.expiresInSeconds, 1) * 1000

  onPolling?.()

  try {
    for (;;) {
      if (Date.now() >= deadline) {
        return errorResult(
          'timeout',
          'The sign-in code expired before it was authorized. Please try again.',
        )
      }

      await sleep(intervalMs, signal)

      const { status, errorMessage } = await settingsGrpc.pollCopilotDeviceAuth(
        start.deviceCode,
      )

      switch (status) {
        case PollCopilotDeviceAuthResponse_Status.AUTHORIZED:
          return { ok: true, message: 'Connected to GitHub Copilot successfully!' }
        case PollCopilotDeviceAuthResponse_Status.PENDING:
        case PollCopilotDeviceAuthResponse_Status.UNSPECIFIED:
          continue
        case PollCopilotDeviceAuthResponse_Status.EXPIRED:
          return errorResult(
            'timeout',
            errorMessage || 'The sign-in code expired before it was authorized. Please try again.',
          )
        case PollCopilotDeviceAuthResponse_Status.DENIED:
          return errorResult(
            'denied',
            errorMessage || 'Authorization was denied. Please try again and approve the request.',
          )
        case PollCopilotDeviceAuthResponse_Status.ERROR:
        default:
          Sentry.captureMessage('Copilot device auth poll returned error', {
            tags: { component: 'oauth', provider: 'copilot' },
            level: 'warning',
          })
          return errorResult(
            'poll_error',
            errorMessage || 'GitHub Copilot sign-in failed. Please try again.',
          )
      }
    }
  } catch (error: any) {
    if (error instanceof AbortError || signal?.aborted) {
      return errorResult('cancelled', 'Sign-in cancelled.')
    }
    Sentry.captureException(error, {
      tags: { component: 'oauth', provider: 'copilot' },
      level: 'warning',
    })
    return errorResult(
      'poll_error',
      error?.message || 'GitHub Copilot sign-in failed. Please try again.',
    )
  }
}
