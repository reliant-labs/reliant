// What the forge sign-in screens share that is not a component: the broker's
// options and password policy as hooks, the query values an emailed link
// carries, and where to land afterwards. Only used when AUTH_PROVIDER is
// "forge" (lib/auth/forge-api.ts).

import { useEffect, useState } from 'react'
import {
  fetchAuthOptions,
  fetchPasswordPolicy,
  LEGACY_AUTH_OPTIONS,
  type AuthOptions,
  type Identity,
  type PasswordPolicy,
} from '@/lib/auth/native-login'
import { useAuthStore } from '@/store/authStore'
import { isSafeReturnTo } from '@/lib/returnTo'
import { authPath, homePath } from '@/lib/paths'
import { landAt } from './forgeLanding'

/**
 * Which sign-in methods this deployment offers (GET /auth/options). Until it
 * answers — and on any failure — password only, invite-only: a hidden button
 * costs nothing, a shown one the broker answers with 404 is a broken screen.
 */
export function useAuthOptions(): { options: AuthOptions; loaded: boolean } {
  const [state, setState] = useState<{ options: AuthOptions; loaded: boolean }>({
    options: LEGACY_AUTH_OPTIONS,
    loaded: false,
  })
  useEffect(() => {
    let live = true
    void fetchAuthOptions().then((options) => {
      if (live) setState({ options, loaded: true })
    })
    return () => {
      live = false
    }
  }, [])
  return state
}

/** What a new password must contain, or null when the broker cannot say (a normal answer). */
export function usePasswordPolicy(): PasswordPolicy | null {
  const [policy, setPolicy] = useState<PasswordPolicy | null>(null)
  useEffect(() => {
    let live = true
    void fetchPasswordPolicy().then((p) => {
      if (live) setPolicy(p)
    })
    return () => {
      live = false
    }
  }, [])
  return policy
}

/**
 * One value of this page's query string. Read raw rather than through a
 * route search schema: the broker writes these links (internal/app/login_broker.go,
 * forge pkg/devidp), and a schema would refuse the bare page.
 */
export function queryParam(name: string): string | null {
  return new URLSearchParams(window.location.search).get(name)
}

/** `requested` when it is a page of this app (lib/returnTo), else home. */
export function safeReturnPath(requested: string | null | undefined): string {
  return isSafeReturnTo(requested) ? requested : homePath()
}

// ── Coming back from an email ───────────────────────────────────────────
//
// A sign-in link or an address confirmation opens in a NEW tab, which knows
// nothing of the `redirect` the sign-in screen had (a CLI login's consent
// page, an approval). The screen that sends the mail remembers it here, and
// the page the link opens takes it back. Same-origin paths only, checked
// again on the way out, and only for a while.

const RETURN_KEY = 'reliant.forge-auth.return-to'
const RETURN_TTL_MS = 30 * 60 * 1000

/** Remembers where to land after an emailed link (a no-op for home). */
export function rememberReturnTo(path: string): void {
  try {
    if (!isSafeReturnTo(path) || path === homePath()) {
      localStorage.removeItem(RETURN_KEY)
      return
    }
    localStorage.setItem(RETURN_KEY, JSON.stringify({ path, at: Date.now() }))
  } catch {
    // Storage is optional: the link still signs in, and lands home.
  }
}

/** The remembered landing page, if one is still fresh; `take` forgets it. */
export function rememberedReturnTo(take = false): string | null {
  try {
    const raw = localStorage.getItem(RETURN_KEY)
    if (take) localStorage.removeItem(RETURN_KEY)
    if (!raw) return null
    const { path, at } = JSON.parse(raw) as { path?: unknown; at?: unknown }
    if (typeof path !== 'string' || typeof at !== 'number') return null
    if (Date.now() - at > RETURN_TTL_MS) return null
    return isSafeReturnTo(path) ? path : null
  } catch {
    return null
  }
}

/** The sign-in screen, carrying a remembered landing page when there is one. */
export function signInHref(): string {
  const back = rememberedReturnTo()
  return back ? `${authPath()}?redirect=${encodeURIComponent(back)}` : authPath()
}

/**
 * Installs an identity the broker just answered with (sign-up, social, magic
 * link, passkey, reset) through the store's serialized transition, then lands
 * on `returnTo` (a same-origin path; anything else is home). Resolves false
 * when there was nothing to install: the broker set no session.
 */
export async function installAndLand(identity: Identity, returnTo: string | null | undefined): Promise<boolean> {
  const installed = await useAuthStore.getState().installForgeIdentity(identity)
  if (!installed) return false
  landAt(safeReturnPath(returnTo))
  return true
}
