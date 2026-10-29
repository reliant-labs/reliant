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
 * Branch on the reason or the Connect code, never on the message: an error
 * that carries no reason this web knows is not claimed by any reader, and the
 * surface shows it as the plain error it is. (Old servers were matched by
 * their prose until P9 R3w2 removed that fallback; 13-p9-frontend-plan.md
 * §7.2, owner decision D3.)
 */

import { ConnectError } from "@connectrpc/connect";
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
  /** The org's hourly observability query quota is spent; the QueryQuotaExhausted detail says when it resets. */
  "obs_query_quota",
  /** A background poll refused early, to keep the last of the quota for a person's own query. */
  "obs_query_quota_soft",
  // ── control-plane: git credentials (internal/gitcredential; reliant token_git.go) ──
  /** No git provider account is connected for the user. */
  "git_credential_not_connected",
  // ── control-plane: claimable deploys (internal/claimable; the /claim page) ──
  /** The deploy was already claimed: the first completion wins. */
  "claimable_claimed",
  /** Expired and purged, or past purge_after and no longer revivable. */
  "claimable_expired",
  /** Removed by an administrator; never claimable. */
  "claimable_taken_down",
  /** No such claim token: the same answer for "never existed" and "wrong". */
  "claimable_not_found",
  /** The anonymous session was already handed to a browser. */
  "claimable_session_released",
  /** CompleteClaim (PermissionDenied): the caller is still anonymous or has no verified email. Back to /upgrade. */
  "claimable_upgrade_required",
] as const;

export type ErrorReason = (typeof ERROR_REASONS)[number];

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
 * action fixes it (control-plane's daemon gate, billing and LLM billing gate).
 * These are the
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

function asConnectError(err: unknown): ConnectError | null {
  return err instanceof ConnectError ? err : null;
}

/** The runtime chain's reason first, then the header's. */
function reasonsOf(err: unknown): string[] {
  const ce = asConnectError(err);
  if (!ce) return [];
  const runtime = ce instanceof ConnectClientError ? ce.reason : null;
  return [runtime, ce.metadata.get(REASON_HEADER)].filter((r): r is string => !!r);
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

/** True when the error carries one of `reasons`. */
export function hasErrorReason(err: unknown, reasons: ErrorReason | readonly ErrorReason[]): boolean {
  const wanted: readonly string[] = typeof reasons === "string" ? [reasons] : reasons;
  return reasonsOf(err).some((r) => wanted.includes(r));
}

/**
 * The scopes a `scope_missing` refusal names, or [] when it names none (or
 * is not one). Scopes are `family:verb` from forge/pkg/accesstoken.AllScopes.
 */
export function errorScopes(err: unknown): string[] {
  const ce = asConnectError(err);
  const header = ce?.metadata.get(ERROR_SCOPES_HEADER) ?? "";
  return header
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}
