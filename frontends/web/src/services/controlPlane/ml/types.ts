// Copyright (c) 2026 Reliant Labs

/**
 * The plain types around the ML tab's reads (components/Forge/Ml): the
 * outcome of a read and the org's entitlement. The reads' own answers are
 * control-plane's `controlplane.v1.MlEarlyAccessService` messages as
 * generated (proto-vendor/controlplane/services/ml_early_access/v1, whose
 * header is the behaviour spec): each view's model file (components/Forge/Ml/
 * <area>/*Model.ts) derives what it draws from them, so there is no second
 * copy of a 30-rpc contract to keep in step.
 *
 * EARLY ACCESS, BY REQUEST. Every read answers one of three ways (MlOutcome):
 * the data; "not entitled" (the org has no "ml" entitlement, or lost it); or
 * "no data yet" (entitled, and this control plane does not hold that data
 * yet). The second and third are states the pages draw, never errors, and
 * neither is ever filled in with invented rows.
 */

/** Per-call options the client forwards to Connect (a query's abort signal). */
export interface CallOptions {
  signal?: AbortSignal;
}

/** One ML read's answer: the data, or why there is none. Every other failure throws. */
export type MlOutcome<T> =
  | { kind: "ok"; data: T }
  /** PermissionDenied, reason FEATURE_NOT_ENTITLED: the org has no "ml" entitlement. */
  | { kind: "not-entitled" }
  /** Unimplemented: the org is entitled, and this control plane has no such data yet. */
  | { kind: "no-data" };

// ── Entitlements (OrganizationService) ──────────────────────────────────────

/** The one feature granted by request today. */
export const ML_FEATURE = "ml";

/** What a member may know of one feature granted by request. */
export interface OrgEntitlement {
  feature: string;
  enabled: boolean;
  /** When someone in the org last asked for it; null when nobody has. */
  requestedAtMs: number | null;
  /** When it was granted; null unless enabled. */
  grantedAtMs: number | null;
}

/** RequestOrgEntitlement's answer: the feature's state after the call. */
export interface EntitlementRequestResult {
  entitlement: OrgEntitlement;
  /** It had been asked for already; nothing was recorded again. */
  alreadyRequested: boolean;
  /** The org has it already; nothing was recorded. */
  alreadyEnabled: boolean;
}
