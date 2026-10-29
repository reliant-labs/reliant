// Copyright (c) 2025 Reliant Labs

/**
 * The claim link, from the address bar to storage.
 *
 * An agent that deployed without an account hands its human
 * `https://<app host>/claim#rcl_<token>`. The token is in the URL FRAGMENT so
 * that it is never sent to a server, a proxy log or a Referer. That guarantee
 * only holds while the fragment stays out of everything the BROWSER sends on:
 * the telemetry SDK stamps `location.href` on its spans and the router keeps
 * it in its own location state.
 *
 * So the fragment is taken at BOOT (claimLinkBoot, imported by main.tsx ahead
 * of the router and the SDKs), moved into storage, and removed from the
 * address bar before anything else has seen it. The /claim page then reads the
 * storage record, never the URL.
 *
 * Storage rather than memory, because the flow leaves the page: attaching a
 * provider identity redirects the window to the provider and back, and an
 * emailed confirmation link opens in a NEW tab. localStorage (not
 * sessionStorage) is the only store both of those can read. That is no wider a
 * door than the Supabase session, which lives in the same place and is the
 * stronger secret.
 *
 * The record also remembers how far the flow got (`anonUserId`: the anonymous
 * session this browser now holds). The server releases that session AT MOST
 * ONCE, so a reload mid-flow must not ask for it again.
 */

/** Where the claim page lives, and where the upgrade flow returns to. */
export const CLAIM_PATH = "/claim";
export const CLAIM_RETURN_TO = CLAIM_PATH;

const STORAGE_KEY = "reliant:claim";

/** A deploy lives 72h and is kept 7 days more: nothing is claimable past this. */
const MAX_AGE_MS = 10 * 24 * 60 * 60 * 1000;

/** `rcl_` + base64url of 256 bits. Lenient on length so a longer token never breaks the link. */
const CLAIM_TOKEN = /^rcl_[A-Za-z0-9_-]{16,}$/;

export interface PendingClaim {
  token: string;
  savedAt: number;
  /** The anonymous user whose session this browser holds, once it was released. */
  anonUserId?: string;
  /** The deploy's project, learned from the preview: which app /apps highlights. */
  project?: string;
  /** The link-opened event went out for this token (a reload must not count twice). */
  opened?: boolean;
  /** CompleteClaim sent this browser back to /upgrade once; a second refusal must not loop. */
  upgradeSent?: boolean;
}

/** The token in a URL fragment (`#rcl_…`), or null when it is not a claim token. */
export function parseClaimToken(hash: string): string | null {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  return CLAIM_TOKEN.test(raw) ? raw : null;
}

/** True for the claim page's own `returnTo`. */
export function isClaimReturnTo(returnTo: string | undefined | null): boolean {
  return returnTo === CLAIM_RETURN_TO;
}

/** True on the claim page, which owns the whole screen while a claim is in flight. */
export function isClaimPath(pathname: string): boolean {
  return pathname === CLAIM_PATH || pathname === `${CLAIM_PATH}/`;
}

// The fallback for a browser that refuses storage (private mode, a policy):
// the flow still works within the page, it only cannot survive a redirect.
let memory: PendingClaim | null = null;

function load(): PendingClaim | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return memory;
    const parsed = JSON.parse(raw) as Partial<PendingClaim>;
    if (typeof parsed.token !== "string" || typeof parsed.savedAt !== "number") return null;
    return parsed as PendingClaim;
  } catch {
    return memory;
  }
}

function save(record: PendingClaim): void {
  memory = record;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(record));
  } catch {
    // Kept in memory above.
  }
}

/** The claim this browser is part-way through, or null (none, malformed, or too old to be claimable). */
export function readPendingClaim(now: number = Date.now()): PendingClaim | null {
  const record = load();
  if (!record || !CLAIM_TOKEN.test(record.token)) return null;
  if (now - record.savedAt > MAX_AGE_MS) {
    clearPendingClaim();
    return null;
  }
  return record;
}

/** Record progress on the pending claim. A no-op when there is none. */
export function updatePendingClaim(patch: Partial<Omit<PendingClaim, "token" | "savedAt">>): void {
  const record = load();
  if (record) save({ ...record, ...patch });
}

export function clearPendingClaim(): void {
  memory = null;
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Nothing stored that could be cleared.
  }
}

/**
 * Move a claim link's fragment into storage and out of the address bar.
 *
 * Runs at boot and again on `hashchange` (pasting a second link into a tab
 * already on /claim changes only the fragment, so nothing reloads). On the
 * claim path the fragment is ALWAYS removed, even a malformed one: it was
 * meant to be a secret, and a mistyped secret is still one.
 *
 * Opening the SAME link again keeps the progress already made on it: the
 * session it released is the one this browser holds.
 *
 * Returns the token when this call stored a DIFFERENT claim than the one
 * pending before it (the caller must restart whatever was working on the old
 * one), else null.
 */
export function captureClaimLinkFromUrl(): string | null {
  if (typeof window === "undefined") return null;
  const { pathname, search, hash } = window.location;
  if (!isClaimPath(pathname) || !hash) return null;

  window.history.replaceState(window.history.state, "", pathname + search);

  const token = parseClaimToken(hash);
  if (!token) return null;
  const existing = load();
  if (existing?.token === token) {
    save({ ...existing, savedAt: Date.now() });
    return null;
  }
  save({ token, savedAt: Date.now() });
  return token;
}

/** Test seam: forget the in-memory fallback. */
export function resetClaimLinkMemoryForTests(): void {
  memory = null;
}
