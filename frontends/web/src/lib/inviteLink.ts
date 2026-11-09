// Copyright (c) 2025 Reliant Labs

/**
 * The org invitation link, from the address bar to storage.
 *
 * An org admin's invitation is `https://<app host>/accept-invite#rinv_<token>`.
 * The token is in the URL FRAGMENT, as the claim link's is (lib/claimLink.ts),
 * so it is never sent to a server, a proxy log or a Referer. That holds only
 * while the fragment stays out of everything the BROWSER sends on: the
 * telemetry SDK stamps `location.href` on its spans and the router keeps it in
 * its own state. So inviteLinkBoot takes it at boot, before either exists,
 * moves it into storage and removes it from the address bar. The
 * /accept-invite page reads storage, never the URL.
 *
 * Storage, not memory, because a signed-out invitee leaves the page to sign in
 * (a provider redirect, or an emailed sign-in link that opens a new tab) and
 * must come back to the same invitation. localStorage is the only store both
 * can read; it holds the Supabase session, a stronger secret, already.
 *
 * The token is never logged, and never put in an error message.
 */

/** Where the accept page lives, and what sign-in returns to. */
export const ACCEPT_INVITE_PATH = "/accept-invite";

const STORAGE_KEY = "reliant:invite";

/** An invitation lives 7 days; a re-invite rotates the token. Nothing older is acceptable. */
const MAX_AGE_MS = 8 * 24 * 60 * 60 * 1000;

/** `rinv_` + base64url of 32 bytes. Lenient on length so a longer token never breaks the link. */
const INVITE_TOKEN = /^rinv_[A-Za-z0-9_-]{16,}$/;

export interface PendingInvite {
  token: string;
  savedAt: number;
}

/** The token in a URL fragment (`#rinv_…`), or null when it is not an invitation token. */
export function parseInviteToken(hash: string): string | null {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  return INVITE_TOKEN.test(raw) ? raw : null;
}

/** True on the accept page. */
export function isAcceptInvitePath(pathname: string): boolean {
  return pathname === ACCEPT_INVITE_PATH || pathname === `${ACCEPT_INVITE_PATH}/`;
}

// The fallback for a browser that refuses storage: the flow works within the
// page, it only cannot survive leaving it.
let memory: PendingInvite | null = null;

function load(): PendingInvite | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return memory;
    const parsed = JSON.parse(raw) as Partial<PendingInvite>;
    if (typeof parsed.token !== "string" || typeof parsed.savedAt !== "number") return null;
    return parsed as PendingInvite;
  } catch {
    return memory;
  }
}

function save(record: PendingInvite): void {
  memory = record;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(record));
  } catch {
    // Kept in memory above.
  }
}

/** The invitation this browser is part-way through, or null (none, malformed, or too old). */
export function readPendingInvite(now: number = Date.now()): PendingInvite | null {
  const record = load();
  if (!record || !INVITE_TOKEN.test(record.token)) return null;
  if (now - record.savedAt > MAX_AGE_MS) {
    clearPendingInvite();
    return null;
  }
  return record;
}

/** Forget it: accepted, or refused for good. */
export function clearPendingInvite(): void {
  memory = null;
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Nothing stored that could be cleared.
  }
}

/**
 * Move an invitation link's fragment into storage and out of the address bar.
 *
 * Runs at boot and on `hashchange`. On the accept path the fragment is ALWAYS
 * removed, even a malformed one: it was meant to be a secret. Returns the
 * token when this call stored a DIFFERENT invitation than the one pending
 * before (the page must start over), else null.
 */
export function captureInviteLinkFromUrl(): string | null {
  if (typeof window === "undefined") return null;
  const { pathname, search, hash } = window.location;
  if (!isAcceptInvitePath(pathname) || !hash) return null;

  window.history.replaceState(window.history.state, "", pathname + search);

  const token = parseInviteToken(hash);
  if (!token) return null;
  const changed = load()?.token !== token;
  save({ token, savedAt: Date.now() });
  return changed ? token : null;
}

/** Test seam: forget the in-memory fallback. */
export function resetInviteLinkMemoryForTests(): void {
  memory = null;
}
