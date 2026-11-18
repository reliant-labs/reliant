// Copyright (c) 2025 Reliant Labs

/**
 * An org's admin policy document (`controlplane.v1.OrgPolicyService`).
 * Contract: control-plane docs/design/org-policies.md §3.2 and §6.
 *
 *   GetOrgPolicy      any member: the document and its version (0 = none)
 *   PreviewOrgPolicy  admin/owner: what saving a document would do, changing
 *                     nothing (violations, field changes, warnings)
 *   UpdateOrgPolicy   admin/owner: replace the whole document, carrying the
 *                     version the editor read (optimistic concurrency)
 *   GetMyPolicyUsage  any member: their own spend against the caps (§6)
 *   ListOrgPolicyDenials  admin/owner: daily counts of what the policies
 *                     refused, and what audit-mode rules would have (§3.4)
 *
 * The history is the org's limit-change log (BillingService.ListOrgLimitChanges),
 * whose policy rows are the resources "policy.<section>.<field>".
 *
 * Every call names its org explicitly: a save must never land in whatever org
 * the transport's interceptor fills in after a switch that raced it.
 *
 * Editing is gated on the caller's ROLE (admin or owner), read fresh by the
 * server. What the page hides from a member is a courtesy; the server refuses.
 */

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import type { OrgPolicy, OrgPolicyMode, SpendAppliesTo, SpendCap } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import {
  OrgPolicyService,
  type OrgPolicyRecord,
  type PolicySpendUsage,
} from "@/gen/controlplane/services/org_policy/v1/org_policy_pb";
import { errorReason, hasErrorReason } from "@/lib/errorReasons";

import { getControlPlaneClient } from "./client";

/**
 * Query keys. The root is in activeOrgStore's ORG_SCOPED_QUERY_ROOTS, so an
 * org switch drops every one of them.
 */
export const orgPolicyKeys = {
  all: ["org-policy"] as const,
  policy: (orgId: string) => ["org-policy", orgId, "document"] as const,
  history: (orgId: string) => ["org-policy", orgId, "history"] as const,
  usage: (orgId: string) => ["org-policy", orgId, "usage"] as const,
  denials: (orgId: string, days: number) => ["org-policy", orgId, "denials", days] as const,
};

/** The org's policy as the page holds it. */
export interface PolicyRecord {
  orgId: string;
  /** 0 while the org has never had a policy. The expected_version of the next save. */
  version: number;
  policy: OrgPolicy | undefined;
  updatedAt: Date | null;
  /** "" for version 0, or when the editor's account is gone. */
  updatedByEmail: string;
}

function mapRecord(orgId: string, r: OrgPolicyRecord | undefined): PolicyRecord {
  return {
    orgId: r?.orgId || orgId,
    version: Number(r?.version ?? 0n),
    policy: r?.policy,
    updatedAt: r?.updatedAt ? timestampDate(r.updatedAt) : null,
    updatedByEmail: r?.updatedByEmail ?? "",
  };
}

/** GetOrgPolicy. No policy is an answer (version 0), never NotFound. */
export async function getOrgPolicy(orgId: string): Promise<PolicyRecord> {
  const res = await getControlPlaneClient(OrgPolicyService).getOrgPolicy({ orgId });
  return mapRecord(orgId, res.policy);
}

export interface PolicyViolation {
  /** The path in the document, e.g. "models.allowed". */
  field: string;
  message: string;
}

export interface PolicyFieldChange {
  /** "policy.<section>.<field>". */
  resource: string;
  field: string;
  /** Display JSON; "" when the field was not set before. */
  oldValue: string;
  /** Display JSON; "null" when the edit removes the field. */
  newValue: string;
}

export interface PolicyPreview {
  /** The stored version now: when it is not the version the editor read, a save would conflict. */
  currentVersion: number;
  valid: boolean;
  violations: PolicyViolation[];
  changes: PolicyFieldChange[];
  warnings: string[];
}

/** PreviewOrgPolicy: what saving `policy` would do. A refused document is an answer here. */
export async function previewOrgPolicy(orgId: string, policy: OrgPolicy): Promise<PolicyPreview> {
  const res = await getControlPlaneClient(OrgPolicyService).previewOrgPolicy({ orgId, policy });
  return {
    currentVersion: Number(res.currentVersion),
    valid: res.valid,
    violations: res.violations.map((v) => ({ field: v.field, message: v.message })),
    changes: res.changes.map((c) => ({ resource: c.resource, field: c.field, oldValue: c.oldValue, newValue: c.newValue })),
    warnings: [...res.warnings],
  };
}

/**
 * UpdateOrgPolicy: replace the whole document. A stale `expectedVersion`
 * fails `policy_version_conflict` and changes nothing.
 */
export async function updateOrgPolicy(
  orgId: string,
  policy: OrgPolicy,
  expectedVersion: number,
): Promise<{ record: PolicyRecord; changed: boolean }> {
  const res = await getControlPlaneClient(OrgPolicyService).updateOrgPolicy({
    orgId,
    policy,
    expectedVersion: BigInt(expectedVersion),
  });
  return { record: mapRecord(orgId, res.policy), changed: res.changed };
}

/** Spend against one cap in its current UTC period. */
export interface SpendUsage {
  /** The cap that applies; null when none does (no cap, or an unlimited override). */
  cap: SpendCap | null;
  /** The caller's override exempts them from the member cap (member usage only). */
  unlimited: boolean;
  /** What the cap counts this period: its own period, or the calendar month without one. */
  spentUsdNanos: bigint;
  periodStart: Date | null;
  /** Exclusive: the moment the count starts again. */
  periodEnd: Date | null;
}

/** The caller's own spend in an org against the caps that bind them (GetMyPolicyUsage). */
export interface MyPolicyUsage {
  /** 0: the org has no policy. */
  policyVersion: number;
  mode: OrgPolicyMode;
  /** The caps refuse calls today; false while they are only trialled (audit). */
  enforced: boolean;
  member: SpendUsage | null;
  org: SpendUsage | null;
  appliesTo: SpendAppliesTo;
}

function mapSpendUsage(u: PolicySpendUsage | undefined): SpendUsage | null {
  if (!u) return null;
  return {
    cap: u.cap ?? null,
    unlimited: u.unlimited,
    spentUsdNanos: u.spentUsdNanos,
    periodStart: u.periodStart ? timestampDate(u.periodStart) : null,
    periodEnd: u.periodEnd ? timestampDate(u.periodEnd) : null,
  };
}

/**
 * GetMyPolicyUsage: any member's own spend against the org's caps. A control
 * plane older than org-policies G2 answers Unimplemented; callers show that
 * as "not available yet" (isUsageUnavailable).
 */
export async function getMyPolicyUsage(orgId: string): Promise<MyPolicyUsage> {
  const res = await getControlPlaneClient(OrgPolicyService).getMyPolicyUsage({ orgId });
  return {
    policyVersion: Number(res.policyVersion),
    mode: res.mode,
    enforced: res.enforced,
    member: mapSpendUsage(res.member),
    org: mapSpendUsage(res.org),
    appliesTo: res.appliesTo,
  };
}

/** The control plane does not have this rpc yet: an older deployment, not a fault. */
export function isRpcNotDeployed(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.Unimplemented;
}

/** The control plane does not have GetMyPolicyUsage yet. */
export const isUsageUnavailable = isRpcNotDeployed;

/** One day's count of one policy's refusals for one reason, member and outcome. */
export interface PolicyDenialCount {
  /** The UTC day, YYYY-MM-DD. */
  day: string;
  /** The section: "models", "spend", ... */
  policy: string;
  /** The refusal reason, e.g. "model_not_allowed". */
  reason: string;
  /** The member the call acted as; "" for a hosted app's environment key. */
  userId: string;
  /** Refused by an enforced rule, or counted by a rule in audit (would have been refused). */
  outcome: "refused" | "audited";
  count: number;
}

export interface PolicyDenials {
  counts: PolicyDenialCount[];
  refusedTotal: number;
  auditedTotal: number;
}

/**
 * ListOrgPolicyDenials (admin/owner): what the org's policies refused, and
 * what rules in audit would have refused, per UTC day over the last `days`
 * days (today included). A control plane older than org-policies A1 answers
 * Unimplemented (isRpcNotDeployed).
 */
export async function listOrgPolicyDenials(orgId: string, days: number): Promise<PolicyDenials> {
  const res = await getControlPlaneClient(OrgPolicyService).listOrgPolicyDenials({ orgId, days });
  return {
    counts: res.counts
      .filter((c) => c.outcome === "refused" || c.outcome === "audited")
      .map((c) => ({
        day: c.day,
        policy: c.policy,
        reason: c.reason,
        userId: c.userId,
        outcome: c.outcome as "refused" | "audited",
        count: Number(c.count),
      })),
    refusedTotal: Number(res.refusedTotal),
    auditedTotal: Number(res.auditedTotal),
  };
}

/** One edit to one field of the policy, from the org's limit-change log. */
export interface PolicyHistoryEntry {
  id: string;
  at: Date | null;
  /** "" when it was not a person. */
  actorEmail: string;
  /** The section: "models", "spend", ... */
  section: string;
  /** The field within it; a member override is "member_overrides.<user_id>". */
  field: string;
  oldValue: string;
  newValue: string;
}

export interface PolicyHistoryPage {
  entries: PolicyHistoryEntry[];
  nextPageToken: string;
}

const POLICY_RESOURCE = /^policy\.([a-z_]+)\./;

/**
 * The policy's edits, newest first: the org's limit-change log narrowed to
 * its "policy.*" rows. The log's filter is one exact resource, so the
 * narrowing is here; a page may hold none and still have older ones.
 */
export async function listPolicyHistory(orgId: string, pageToken = ""): Promise<PolicyHistoryPage> {
  const res = await getControlPlaneClient(BillingService).listOrgLimitChanges({ orgId, pageSize: 100, pageToken });
  const entries: PolicyHistoryEntry[] = [];
  for (const c of res.changes) {
    const m = POLICY_RESOURCE.exec(c.resource);
    if (!m) continue;
    entries.push({
      id: c.id,
      at: c.at ? timestampDate(c.at) : null,
      actorEmail: c.actorEmail,
      section: m[1],
      field: c.field,
      oldValue: c.oldValue,
      newValue: c.newValue,
    });
  }
  return { entries, nextPageToken: res.nextPageToken };
}

/** Whether the caller's role lets them edit the org's policy. A courtesy: the server decides. */
export function canEditPolicy(role: string | undefined): boolean {
  return role === "admin" || role === "owner";
}

/** A save lost the race to someone else's: reload, and reapply. */
export function isPolicyVersionConflict(err: unknown): boolean {
  return hasErrorReason(err, "policy_version_conflict");
}

const REASON_COPY: Record<string, string> = {
  admin_required: "Only an admin or owner of this organization can change its policies.",
  human_only: "Sign in as yourself to do this: a machine token cannot read or change policies.",
  not_org_member: "You're not a member of this organization any more.",
  org_unclaimed: "This organization belongs to a site nobody has claimed yet, so it has no policies.",
  policy_invalid: "These policies can't be saved as they are. Review them to see what to fix.",
  policy_version_conflict: "Someone saved a newer version of these policies while you were editing.",
};

/**
 * Human copy for a failed policy call: the reason's words when the server
 * named one, else `fallback`. Never the server's prose, which is not copy.
 */
export function policyErrorMessage(err: unknown, fallback: string): string {
  const reason = errorReason(err);
  return (reason && REASON_COPY[reason]) || fallback;
}
