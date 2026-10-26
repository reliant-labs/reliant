// Copyright (c) 2025 Reliant Labs

/**
 * Connecting GitHub (the control plane's `/auth/github/authorize` flow, which
 * writes `git_credentials`) from the web app or the desktop app. Every
 * "Connect GitHub" button goes through connectGitHub — nine call sites used to
 * each build the authorize URL and navigate, and the desktop one of those
 * finished the flow in a system-browser tab the app was never told about.
 *
 *   web      navigate this page to the authorize URL. The flow returns to
 *            `/auth/github/callback`, which exchanges the code and lands back.
 *   desktop  the same loopback relay Slack/Gmail use (see connection-oauth.ts):
 *            the main process listens on 127.0.0.1, the authorize URL carries
 *            that address as `loopback`, the web callback page relays the code
 *            and signed state to it without exchanging, and THIS renderer
 *            exchanges them — so the app that asked is the app that finishes.
 *
 * A GitHub App user token only lists repos of accounts the App is INSTALLED
 * on, so authorizing is not enough for a new user. The exchange reports
 * `needsInstall`; on desktop we open the install page right away.
 */
import { toast } from 'sonner'
import { gitService } from '@/services/controlPlane/git'
import { supabase } from '@/lib/supabase'
import { queryClient } from '@/lib/query-client'
import { logger } from '@/lib/logger'
import { connectsThroughSystemBrowser } from '@/lib/connection-oauth'
import { GITHUB_CREDENTIAL_QUERY_KEY } from '@/hooks/useGitHubCredential'

const GITHUB_REPOS_QUERY_KEY = ['onboarding', 'gitRepos'] as const

/** A token with less than this left is refreshed before it goes in a URL. */
const MIN_TOKEN_LIFETIME_MS = 60_000

type GitHubConnectBridge = {
  startConnectionOAuthReceiver?: () => Promise<{
    flowId: string
    redirectUri: string
  }>
  waitForProviderOAuth?: (flowId: string) => Promise<{ code: string; state: string }>
  cancelProviderOAuth?: (flowId: string) => Promise<unknown>
  openExternal?: (url: string) => Promise<unknown>
}

const bridge = (): GitHubConnectBridge | undefined =>
  typeof window === 'undefined'
    ? undefined
    : (window as unknown as { electronAPI?: GitHubConnectBridge }).electronAPI

export type GitHubConnectOutcome =
  /** Web: this page is navigating to GitHub and will come back. */
  | { kind: 'redirecting' }
  /** Desktop: the credential was saved while the caller waited. */
  | { kind: 'connected'; needsInstall: boolean }
  /** Desktop: the caller aborted, or the user dismissed the flow. */
  | { kind: 'cancelled' }
  | { kind: 'error'; message: string }

export interface GitHubConnectOptions {
  /** Where the web flow lands afterwards. Defaults to the current location. */
  returnTo?: string
  /** Releases the desktop loopback receiver at once. */
  signal?: AbortSignal
}

class Cancelled extends Error {}

/** A Supabase access token with at least MIN_TOKEN_LIFETIME_MS left. An
 *  expired one in `?token=` produced a raw 401 page on the control plane. */
async function freshAccessToken(): Promise<string> {
  const {
    data: { session },
  } = await supabase.auth.getSession()
  if (!session?.access_token) throw new Error('Not signed in')

  const expiresAtMs = session.expires_at ? session.expires_at * 1000 : undefined
  if (expiresAtMs === undefined || expiresAtMs - Date.now() >= MIN_TOKEN_LIFETIME_MS) {
    return session.access_token
  }
  const { data, error } = await supabase.auth.refreshSession()
  if (error || !data.session?.access_token) {
    throw new Error('Your session expired. Sign in again to connect GitHub.')
  }
  return data.session.access_token
}

function relayedErrorMessage(raw: string): string {
  if (raw.startsWith('access_denied')) return "Access wasn't granted on GitHub."
  return raw || 'GitHub sign-in did not complete.'
}

function currentLocation(): string {
  return `${window.location.pathname}${window.location.search}`
}

async function connectOnDesktop(
  api: Required<
    Pick<
      GitHubConnectBridge,
      'startConnectionOAuthReceiver' | 'waitForProviderOAuth' | 'openExternal'
    >
  > &
    GitHubConnectBridge,
  returnTo: string,
  signal?: AbortSignal,
): Promise<GitHubConnectOutcome> {
  const { flowId, redirectUri } = await api.startConnectionOAuthReceiver()
  const release = () => void api.cancelProviderOAuth?.(flowId)
  signal?.addEventListener('abort', release, { once: true })
  try {
    if (signal?.aborted) throw new Cancelled()
    const token = await freshAccessToken()
    const authorizeUrl = gitService.buildOAuthURL({
      token,
      returnTo,
      loopback: redirectUri,
    })
    if (!authorizeUrl) throw new Error('Control plane URL not configured')
    if (signal?.aborted) throw new Cancelled()
    await api.openExternal(authorizeUrl)

    let relayed: { code: string; state: string }
    try {
      relayed = await api.waitForProviderOAuth(flowId)
    } catch (err) {
      if (signal?.aborted) throw new Cancelled()
      throw new Error(relayedErrorMessage(err instanceof Error ? err.message.trim() : ''))
    }
    if (signal?.aborted) throw new Cancelled()

    const res = await gitService.exchangeGithubOAuthCode(relayed.code, relayed.state)
    if (!res.ok) throw new Error(`GitHub connection failed${res.error ? ` (${res.error})` : ''}`)

    await Promise.all([
      queryClient.invalidateQueries({ queryKey: GITHUB_CREDENTIAL_QUERY_KEY }),
      queryClient.invalidateQueries({ queryKey: GITHUB_REPOS_QUERY_KEY }),
    ])

    const needsInstall = res.needsInstall && !!res.installUrl
    if (needsInstall) {
      toast.success('GitHub connected', {
        description:
          'Install the Reliant GitHub App on the account that owns your repositories, then come back here.',
      })
      await api.openExternal(res.installUrl)
    } else {
      toast.success('GitHub connected successfully')
    }
    return { kind: 'connected', needsInstall }
  } catch (err) {
    release()
    throw err
  } finally {
    signal?.removeEventListener('abort', release)
  }
}

/**
 * Start (and, on the desktop, finish) the GitHub connect flow. Never throws:
 * every failure is an `error` outcome carrying a message to show.
 */
export async function connectGitHub(
  options: GitHubConnectOptions = {},
): Promise<GitHubConnectOutcome> {
  const returnTo = options.returnTo ?? currentLocation()
  try {
    const api = bridge()
    if (
      connectsThroughSystemBrowser() &&
      api?.startConnectionOAuthReceiver &&
      api.waitForProviderOAuth &&
      api.openExternal
    ) {
      return await connectOnDesktop(
        api as Parameters<typeof connectOnDesktop>[0],
        returnTo,
        options.signal,
      )
    }

    const token = await freshAccessToken()
    const authorizeUrl = gitService.buildOAuthURL({ token, returnTo })
    if (!authorizeUrl) throw new Error('Control plane URL not configured')
    window.location.assign(authorizeUrl)
    return { kind: 'redirecting' }
  } catch (err) {
    if (err instanceof Cancelled) return { kind: 'cancelled' }
    logger.warn('[connectGitHub] failed', err)
    return {
      kind: 'error',
      message: err instanceof Error ? err.message : 'Failed to connect GitHub',
    }
  }
}
