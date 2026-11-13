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

import type { OrgPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { OrgPolicyService, type OrgPolicyRecord } from "@/gen/controlplane/services/org_policy/v1/org_policy_pb";
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
