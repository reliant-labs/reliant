// Account settings in forge mode: the login broker's account routes
// (forge/pkg/devidp AccountRoutes, forge#2663 — the API this is built against
// is that PR's; reliant's broker mounts the routes once it is rescaffolded on a
// forge that contains it).
//
// Every route is on reliant-api's broker (`reliantForgeBase()`), JSON, and acts
// on the account of the SESSION COOKIE only — a bearer is refused — so every
// call carries `credentials: "include"` and no Authorization header.
//
// ── Recent proof ────────────────────────────────────────────────────────
//
// What changes how the account is reached (its address, password, social links
// and passkeys) or signs other devices out needs a credential proved within the
// broker's reauth window (10 minutes by default). Past it the route answers 401
// `reauth_required` with `methods`: "password" (POST /auth/reauth, then retry)
// when the account has one, and "sign_in" (sign out and in again), which works
// for every account. `isReauthRequired` tells a caller to prompt.
//
// Every refusal is an AccountError carrying the broker's `code`; its message is
// the broker's own, already written for the person reading it.

import { reliantForgeBase } from "@/lib/auth/forge-api";
import type { PasswordRule } from "@/lib/auth/native-login";

/** A connected social account (GET /auth/account `links`). */
export interface AccountLink {
  /** The policy entry's type ("github"), or the issuer's provider id for one the policy no longer declares. */
  provider: string;
  label?: string;
  userName?: string;
}

/** A provider the login policy declares, and whether it is connected. */
export interface AccountSocial {
  type: string;
  label: string;
  connected: boolean;
}

/** One of the account's passkeys. */
export interface AccountPasskey {
  id: string;
  name: string;
}

/** One place the account is signed in. `handle` names it for revoking; it is not a credential. */
export interface AccountSession {
  handle: string;
  userAgent?: string;
  /** RFC 3339. */
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  current: boolean;
}

/** GET /auth/account. */
export interface Account {
  subject: string;
  email: string;
  emailVerified: boolean;
  hasPassword: boolean;
  links: AccountLink[];
  social: AccountSocial[];
  passkeys: AccountPasskey[];
  sessions: AccountSession[];
  /** RFC 3339; the zero time when the session never proved a credential. */
  provedAt: string;
  /** The reauth window is closed: the next "needs a recent proof" action will be refused. */
  reauthRequired: boolean;
}

/** How a person can prove a credential again. */
export type ReauthMethod = "password" | "sign_in";

/**
 * The broker refused an account request. `code` is its machine-readable reason
 * ("reauth_required", "wrong_password", "last_credential", "address_in_use",
 * "mail_unavailable", "sessions_not_revoked", "current_session", "cross_site",
 * "not_signed_in", "password_policy", …), "" when it gave none; `status` 0 is
 * the broker being unreachable.
 */
export class AccountError extends Error {
  readonly status: number;
  readonly code: string;
  /** reauth_required: the ways to prove a credential again. */
  readonly methods?: ReauthMethod[];
  /** sessions_not_revoked: the route that finishes signing the other devices out. */
  readonly retry?: string;
  /** sessions_not_revoked: the password DID change. */
  readonly changed?: boolean;
  /** A refused password: the policy rules it breaks, when the broker names them. */
  readonly unmet: PasswordRule[];

  constructor(
    message: string,
    status: number,
    code = "",
    extra: { methods?: ReauthMethod[]; retry?: string; changed?: boolean; unmet?: PasswordRule[] } = {},
  ) {
    super(message);
    this.name = "AccountError";
    this.status = status;
    this.code = code;
    this.methods = extra.methods;
    this.retry = extra.retry;
    this.changed = extra.changed;
    this.unmet = extra.unmet ?? [];
  }
}

/** True when the action needs a recent proof first: prompt, then retry it. */
export function isReauthRequired(err: unknown): err is AccountError & { methods: ReauthMethod[] } {
  return err instanceof AccountError && err.code === "reauth_required";
}

const UNREACHABLE = "Could not reach the account service.";

async function call<T>(method: "GET" | "POST" | "DELETE", path: string, body?: unknown): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`${reliantForgeBase()}${path}`, {
      method,
      credentials: "include",
      headers: {
        Accept: "application/json",
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
  } catch {
    throw new AccountError(UNREACHABLE, 0);
  }
  if (!response.ok) throw await rejection(response);
  try {
    return (await response.json()) as T;
  } catch {
    return {} as T;
  }
}

async function rejection(response: Response): Promise<AccountError> {
  let raw: Record<string, unknown> = {};
  try {
    const parsed: unknown = await response.json();
    if (parsed && typeof parsed === "object") raw = parsed as Record<string, unknown>;
  } catch {
    // A body that is not JSON: fall through to the status.
  }
  const code = typeof raw.code === "string" ? raw.code : "";
  const methods = Array.isArray(raw.methods)
    ? raw.methods.filter((m): m is ReauthMethod => m === "password" || m === "sign_in")
    : undefined;
  const message =
    typeof raw.error === "string" && raw.error !== ""
      ? raw.error
      : response.status >= 500
        ? "The account service is unavailable. Please try again."
        : "That request was not accepted.";
  return new AccountError(message, response.status, code, {
    methods: code === "reauth_required" ? (methods && methods.length > 0 ? methods : ["sign_in"]) : methods,
    retry: typeof raw.retry === "string" ? raw.retry : undefined,
    changed: typeof raw.changed === "boolean" ? raw.changed : undefined,
    unmet: Array.isArray(raw.unmet) ? (raw.unmet as PasswordRule[]) : undefined,
  });
}

function segment(value: string): string {
  return encodeURIComponent(value);
}

/** The signed-in person's account page. */
export async function fetchAccount(): Promise<Account> {
  const a = await call<Partial<Account>>("GET", "/auth/account");
  return {
    subject: a.subject ?? "",
    email: a.email ?? "",
    emailVerified: a.emailVerified === true,
    hasPassword: a.hasPassword === true,
    links: a.links ?? [],
    social: a.social ?? [],
    passkeys: a.passkeys ?? [],
    sessions: a.sessions ?? [],
    provedAt: a.provedAt ?? "",
    reauthRequired: a.reauthRequired === true,
  };
}

/** Proves the account's password again, opening the reauth window. Rejects with `wrong_password`. */
export async function reauthenticate(password: string): Promise<void> {
  await call("POST", "/auth/reauth", { password });
}

/**
 * Starts changing the address: the broker mails a code (and a link to
 * /auth/account/email/confirm?code=…) to the NEW address. From now until it is
 * confirmed the account's address IS the new one, unverified. Recent proof.
 */
export async function changeEmail(email: string): Promise<{ message: string }> {
  const body = await call<{ message?: string }>("POST", "/auth/account/email", { email });
  return { message: body.message ?? "Check your email: we sent a code to the new address." };
}

/** Proves the code mailed to the new address; resolves with the address, now verified. */
export async function confirmEmailChange(code: string): Promise<{ email: string; emailVerified: boolean }> {
  const body = await call<{ email?: string; emailVerified?: boolean }>("POST", "/auth/account/email/confirm", { code });
  return { email: body.email ?? "", emailVerified: body.emailVerified === true };
}

/** What a password change answered. */
export interface PasswordChange {
  changed: boolean;
  otherSessionsRevoked: boolean;
  otherSessionsEnded: number;
}

/**
 * Sets or changes the password, then signs every other session out.
 * `currentPassword`, when given, is the proof; without it the session needs a
 * recent one. A 502 `sessions_not_revoked` AccountError has `changed: true`:
 * the password changed but other devices may still be signed in, and
 * `revokeOtherSessions` finishes it.
 */
export async function changePassword(input: { currentPassword?: string; newPassword: string }): Promise<PasswordChange> {
  const body = await call<Partial<PasswordChange>>("POST", "/auth/account/password", {
    ...(input.currentPassword ? { currentPassword: input.currentPassword } : {}),
    newPassword: input.newPassword,
  });
  return {
    changed: body.changed === true,
    otherSessionsRevoked: body.otherSessionsRevoked === true,
    otherSessionsEnded: typeof body.otherSessionsEnded === "number" ? body.otherSessionsEnded : 0,
  };
}

/**
 * Starts connecting a social account; resolves with the provider URL to
 * navigate to. The provider returns to the SPA's social-callback page, which
 * completes it as a link to THIS session. Recent proof.
 */
export async function startLink(provider: string, returnTo?: string): Promise<{ redirect: string }> {
  const body = await call<{ redirect?: string }>("POST", "/auth/account/links/start", {
    provider,
    ...(returnTo ? { returnTo } : {}),
  });
  if (!body.redirect) throw new AccountError("The account service did not say where to go.", 502);
  return { redirect: body.redirect };
}

/** Disconnects a social account. Recent proof; 409 `last_credential` when it is the only way in. */
export async function unlinkSocial(provider: string): Promise<void> {
  await call("DELETE", `/auth/account/links/${segment(provider)}`);
}

/** Removes a passkey. Recent proof; 409 `last_credential` when it is the only way in. */
export async function removePasskey(id: string): Promise<void> {
  await call("DELETE", `/auth/account/passkeys/${segment(id)}`);
}

/** Where the account is signed in, the current session flagged. */
export async function listSessions(): Promise<AccountSession[]> {
  const body = await call<{ sessions?: AccountSession[] }>("GET", "/auth/account/sessions");
  return body.sessions ?? [];
}

/** Signs one OTHER session out (409 `current_session` for this one: sign out instead). */
export async function revokeSession(handle: string): Promise<void> {
  await call("DELETE", `/auth/account/sessions/${segment(handle)}`);
}

/** Signs every other session out; resolves with how many ended. Recent proof. */
export async function revokeOtherSessions(): Promise<number> {
  const body = await call<{ revoked?: number }>("POST", "/auth/account/sessions/revoke-others", {});
  return typeof body.revoked === "number" ? body.revoked : 0;
}
