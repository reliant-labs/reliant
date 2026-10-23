// Copyright (c) 2025 Reliant Labs

/**
 * WHY AN RPC FAILED, IN WORDS THE SERVER PROMISES NOT TO CHANGE.
 *
 * Both servers name the failures the UI branches on with a stable reason code
 * in the `x-forge-error-reason` error metadata (forge's svcerr.ReasonHeader,
 * set by `svcerr.WithReason`). This module is the ONE place the web reads it:
 * every surface asks `hasErrorReason(err, "<reason>")` (or `errorReason(err)`)
 * instead of matching the server's prose, which is display copy and gets
 * reworded.
 *
 * An error that went through forge-web-runtime's chain arrives as a
 * `ConnectClientError` (a ConnectError subclass since runtime 0.8) whose
 * `.reason` is that header; a plain ConnectError carries it in `metadata`.
 * Both are read here, so a caller never cares which transport it was on.
 *
 * ── EXPAND / CONTRACT (13-p9-frontend-plan.md §7.2, owner decision D3) ────
 *
 * The servers do not all stamp reasons yet. Until they do (R3s-rel, R3s-cp),
 * two older signals are still read:
 *
 *   - control-plane's `x-reliant-reason` header, read as if it were the new one;
 *   - the message markers each surface used to match, kept in
 *     LEGACY_MARKERS below, keyed by the reason that replaces them. They are
 *     consulted only when the error carries no reason from ERROR_REASONS.
 *
 * So a server that has switched is judged by its reason alone, and a reason
 * this web has no branch for (forge's CRUD vocabulary) cannot hide a marker
 * the surface still depends on. R3w2 deletes LEGACY_REASON_HEADER,
 * LEGACY_MARKERS, legacyView and the two lines marked EXPAND that read them —
 * nothing outside this file changes.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { ConnectClientError } from "@reliantlabs/forge-web-runtime";

/**
 * Every reason the web branches on: a closed vocabulary, so a typo is a type
 * error rather than a branch that silently never runs. Each names its
 * producer; the servers' constants are the source of truth for the spelling.
 */
export const ERROR_REASONS = [
  // ── reliant: the user's machine (internal/toolexec, internal/machinewake) ──
  /** A daemon record exists but has not registered yet (ErrDaemonPending). */
  "daemon_starting",
  /** The machine is asleep (ErrDaemonSuspended); a message wakes it. */
  "daemon_suspended",
  /** The server woke the machine for this request (machinewake). */
  "daemon_waking",
  /** The machine's daemon is not on NATS: closed or restarting (errDaemonNotConnected). */
  "daemon_not_connected",
  /** No machine exists for the request at all (ErrNoDaemon). Nothing to wait for. */
  "no_daemon",
  // ── reliant: handlers ──
  /** SendMessage on a chat whose root run has not started (handlers/chat). */
  "chat_not_started",
  /** A save raced a newer revision of the same definition (handlers/workflow). */
  "stale_revision",
  /** Killing a process that already exited (handlers/package_commands, background). */
  "process_not_running",
  /** The operator turned the forge rpc surface off (internal/grpc/forge_api_disabled.go). */
  "forge_api_disabled",
  /** A protected environment wants a fresh step-up (control-plane secret_store). */
  "step_up_required",
  // ── control-plane: daemon lifecycle and entitlement (internal/svcdaemon) ──
  /** Resume of a machine that is not suspended — it is already up or coming up. */
  "daemon_not_suspended",
  /** Included compute minutes and coupon time are spent. */
  "compute_budget_exhausted",
  /** No compute plan and no coupon grant. */
  "no_compute_subscription",
  /** The plan does not include the requested machine size. */
  "daemon_size_denied",
  /** The plan does not allow the requested disk size. */
  "daemon_storage_denied",
  /** The session has no reachable identity (internal/accountgate). */
  "account_required",
  // ── control-plane: billing (internal/billing, internal/llmproxy) ──
  /** A billing action needs an email on the org first. */
  "billing_email_missing",
  /** The org's Reliant credit is at or below zero. */
  "reliant_credit_exhausted",
  // ── access tokens and deploy (control-plane handlers/access_token, reliant handlers/token) ──
  /** Authenticated, but the token lacks a scope; the scopes ride ERROR_SCOPES_HEADER. */
  "scope_missing",
  /** forge on the machine holds no usable control-plane credential. */
  "no_control_plane_credential",
  // ── control-plane: observability and org membership ──
  /** This control plane runs no alerting backend. */
  "observability_not_configured",
  /** The caller is not a member of the organization asked about. */
  "not_org_member",
  // ── control-plane: git credentials (internal/gitcredential; reliant token_git.go) ──
  /** No git provider account is connected for the user. */
  "git_credential_not_connected",
] as const;

export type ErrorReason = (typeof ERROR_REASONS)[number];

const KNOWN_REASONS: ReadonlySet<string> = new Set(ERROR_REASONS);

/**
 * The machine is on its way: starting, asleep, waking, or reconnecting. A
 * surface waits and retries on any of these (daemon-wait.ts, daemon-retry.ts).
 */
export const DAEMON_CONNECTING_REASONS = [
  "daemon_starting",
  "daemon_suspended",
  "daemon_waking",
  "daemon_not_connected",
] as const satisfies readonly ErrorReason[];

/**
 * The account is not entitled to what it asked for, and a billing or identity
 * action fixes it (control-plane's daemon gate, billing and LLM billing gate):
 * every value control-plane has sent as `x-reliant-reason`. These are the
 * reasons the upgrade / identity / billing-email modals render — never "any
 * reason at all", which once the servers stamp reasons everywhere would also
 * catch a coupon's `coupon_exhausted`.
 */
export const ENTITLEMENT_REASONS = [
  "account_required",
  "billing_email_missing",
  "compute_budget_exhausted",
  "daemon_size_denied",
  "daemon_storage_denied",
  "no_compute_subscription",
  "reliant_credit_exhausted",
] as const satisfies readonly ErrorReason[];

/** forge's svcerr.ReasonHeader. */
const REASON_HEADER = "x-forge-error-reason";

/**
 * The scopes a `scope_missing` refusal names, comma-separated. Structured, so
 * the deploy panel can say which grant to ask for without parsing a sentence.
 */
const ERROR_SCOPES_HEADER = "x-forge-error-scopes";

/** EXPAND (R3w2 deletes): control-plane's pre-svcerr reason header. */
const LEGACY_REASON_HEADER = "x-reliant-reason";

function asConnectError(err: unknown): ConnectError | null {
  return err instanceof ConnectError ? err : null;
}

/** forge's reason first, then (EXPAND) the legacy header's. */
function reasonsOf(err: unknown): string[] {
  const ce = asConnectError(err);
  if (!ce) return [];
  const runtime = ce instanceof ConnectClientError ? ce.reason : null;
  return [runtime, ce.metadata.get(REASON_HEADER), ce.metadata.get(LEGACY_REASON_HEADER)].filter(
    (r): r is string => !!r,
  );
}

/**
 * The reason a failed RPC carries, or null when it carries none (a transport
 * failure, a server that has not been taught one, a non-RPC error). For
 * surfaces that forward the reason (the upgrade modal's copy table); a
 * surface that BRANCHES asks hasErrorReason.
 */
export function errorReason(err: unknown): string | null {
  return reasonsOf(err)[0] ?? null;
}

/**
 * True when the error carries one of `reasons`.
 *
 * A reason from this vocabulary is the whole answer. Only an error with no
 * reason this web knows — none at all, or one it has no branch for (forge's
 * generated CRUD stamps its own) — falls back to the legacy marker of each
 * asked-for reason; see the module comment.
 */
export function hasErrorReason(err: unknown, reasons: ErrorReason | readonly ErrorReason[]): boolean {
  const wanted: readonly string[] = typeof reasons === "string" ? [reasons] : reasons;
  const carried = reasonsOf(err);
  if (carried.some((r) => wanted.includes(r))) return true;
  if (carried.some((r) => KNOWN_REASONS.has(r))) return false;
  // EXPAND (R3w2 deletes this line): no reason yet, so the old marker answers.
  return wanted.some((r) => LEGACY_MARKERS[r as ErrorReason]?.(legacyView(err)) ?? false);
}

/**
 * The scopes a `scope_missing` refusal names, or [] when it names none (or
 * is not one). Scopes are `family:verb` from forge/pkg/accesstoken.AllScopes.
 */
export function errorScopes(err: unknown): string[] {
  const ce = asConnectError(err);
  const header = ce?.metadata.get(ERROR_SCOPES_HEADER) ?? "";
  const scopes = header
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  if (scopes.length > 0) return scopes;
  // EXPAND (R3w2 deletes): the scope named in the refusal's prose.
  const match = legacyView(err).text.match(
    /\b(deploy|secret|domain|cluster|token|llm|proxy|mcp|daemon|reliant):[a-z]+\b/,
  );
  return match ? [match[0]] : [];
}

// ── EXPAND: everything below is deleted by R3w2 ─────────────────────────────

/** What a legacy marker sees: the Connect code when there is one, and the text. */
interface LegacyView {
  code: Code | null;
  /** The server's message, lower-cased; `[code] ` framing included when present. */
  text: string;
}

function legacyView(err: unknown): LegacyView {
  if (err instanceof ConnectError) {
    // `message` carries connect's "[code] " framing and `rawMessage` the
    // server's text; markers have matched both, so both are searched.
    return { code: err.code, text: `${err.message} ${err.rawMessage}`.toLowerCase() };
  }
  if (err instanceof Error) return { code: null, text: err.message.toLowerCase() };
  if (typeof err === "string") return { code: null, text: err.toLowerCase() };
  return { code: null, text: "" };
}

/** daemonoffline.ErrorSubstring: in every "the machine is not serving" message. */
const NO_DAEMON_CONNECTED = "no daemon connected";

function daemonStarting({ text }: LegacyView): boolean {
  return text.includes(NO_DAEMON_CONNECTED) && text.includes("still starting");
}
function daemonSuspended({ text }: LegacyView): boolean {
  return text.includes(NO_DAEMON_CONNECTED) && text.includes("suspended");
}
function daemonWaking({ text }: LegacyView): boolean {
  return text.includes(NO_DAEMON_CONNECTED) && text.includes("waking");
}

/**
 * The message (and code) each surface matched before its reason existed,
 * keyed by that reason. A reason with no entry was only ever signalled by
 * LEGACY_REASON_HEADER, which reasonsOf already reads — including the resume
 * quota's old "resource_exhausted"/"compute limit" text arm, which existed only
 * because callers kept the message and dropped the error; they keep the error
 * now, and control-plane has always sent its reason.
 */
const LEGACY_MARKERS: Partial<Record<ErrorReason, (view: LegacyView) => boolean>> = {
  daemon_starting: daemonStarting,
  daemon_suspended: daemonSuspended,
  daemon_waking: daemonWaking,
  // The rest of "no daemon connected": the generic "…for user" a closed or
  // restarting machine answers with. Together the four cover the marker.
  daemon_not_connected: (v) =>
    v.text.includes(NO_DAEMON_CONNECTED) && !daemonStarting(v) && !daemonSuspended(v) && !daemonWaking(v),
  no_daemon: ({ text }) => text.includes("no daemon available"),
  chat_not_started: ({ code, text }) => code === Code.FailedPrecondition && text.includes("chat has not started"),
  stale_revision: ({ text }) => text.includes("workflow was modified since"),
  process_not_running: ({ text }) => text.includes("not running") || text.includes("already stopped"),
  daemon_not_suspended: ({ code, text }) => code === Code.FailedPrecondition && text.includes("not suspended"),
  // The deploy panel's refusals reach it as a plain Error from the plan call,
  // so they match on text alone (services/forge/deployAuthz.ts).
  scope_missing: ({ text }) =>
    text.includes("permission_denied") ||
    text.includes("permission denied") ||
    text.includes("permissions do not include"),
  no_control_plane_credential: ({ text }) =>
    text.includes("no control-plane credential") ||
    text.includes("forge login") ||
    text.includes("unauthenticated"),
  observability_not_configured: ({ code, text }) => code === Code.Unavailable && text.includes("not configured"),
  not_org_member: ({ code, text }) => code === Code.PermissionDenied && text.includes("not a member"),
  git_credential_not_connected: ({ text }) => text.includes("no git credential found"),
};
