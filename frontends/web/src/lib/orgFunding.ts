// Copyright (c) 2025 Reliant Labs

/**
 * A person's choice, per org, of whose AI accounts that org's chats use first
 * and what happens when the org's accounts cannot serve a chat
 * (control-plane docs/design/org-provider-accounts.md §4.4, §10.4). It is the
 * user setting `provider.org_funding`, a JSON object keyed by org id:
 *
 *   {"<org_id>": {"order": "org_first", "on_org_limit": "ask"}}
 *
 * The server reads it (internal/orgfunding) and is the authority: a missing
 * entry, a missing field and an unreadable value all mean the defaults
 * (org_first, ask), so a bad value can only ask or wait, never spend the
 * person's money. This module is the same reading, for Settings to show and
 * write. Only a person's session may write it (the settings handler refuses
 * an agent's or a script's credential).
 *
 * Pure: no React, no RPCs.
 */

export const ORG_FUNDING_KEY = "provider.org_funding";

/** Whose accounts an org's chat uses first. */
export type FundingOrder = "org_first" | "personal_first" | "org_only";
/** What happens when the org's accounts cannot serve and the person's own could. */
export type OnOrgLimit = "ask" | "use_personal" | "wait";

export interface OrgFundingPreference {
  order: FundingOrder;
  onOrgLimit: OnOrgLimit;
}

export const DEFAULT_ORG_FUNDING: OrgFundingPreference = { order: "org_first", onOrgLimit: "ask" };

/** One org's stored entry, as the server writes it (omitempty fields). */
interface StoredPreference {
  order?: string;
  on_org_limit?: string;
}

/** The parsed setting, by org id. */
export type OrgFundingSettings = Record<string, StoredPreference>;

const ORDERS: readonly FundingOrder[] = ["org_first", "personal_first", "org_only"];
const ON_LIMITS: readonly OnOrgLimit[] = ["ask", "use_personal", "wait"];

function parseOrder(v: unknown): FundingOrder | undefined {
  return typeof v === "string" && (ORDERS as readonly string[]).includes(v.trim()) ? (v.trim() as FundingOrder) : undefined;
}

function parseOnOrgLimit(v: unknown): OnOrgLimit | undefined {
  return typeof v === "string" && (ON_LIMITS as readonly string[]).includes(v.trim()) ? (v.trim() as OnOrgLimit) : undefined;
}

/** Reads the setting's value. Anything unreadable is no entries: every org has the defaults. */
export function parseOrgFunding(value: string | undefined): OrgFundingSettings {
  if (!value?.trim()) return {};
  try {
    const raw: unknown = JSON.parse(value);
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
    const out: OrgFundingSettings = {};
    for (const [orgId, entry] of Object.entries(raw as Record<string, unknown>)) {
      if (!entry || typeof entry !== "object") continue;
      const e = entry as Record<string, unknown>;
      out[orgId] = {
        ...(typeof e.order === "string" ? { order: e.order } : {}),
        ...(typeof e.on_org_limit === "string" ? { on_org_limit: e.on_org_limit } : {}),
      };
    }
    return out;
  } catch {
    return {};
  }
}

/** `orgId`'s preference, the defaults filling anything unset or unknown. */
export function orgFundingFor(settings: OrgFundingSettings, orgId: string): OrgFundingPreference {
  const e = settings[orgId];
  return {
    order: parseOrder(e?.order) ?? DEFAULT_ORG_FUNDING.order,
    onOrgLimit: parseOnOrgLimit(e?.on_org_limit) ?? DEFAULT_ORG_FUNDING.onOrgLimit,
  };
}

/** Whether `orgId` has anything other than the defaults (what "Reset" undoes). */
export function isDefaultOrgFunding(settings: OrgFundingSettings, orgId: string): boolean {
  const p = orgFundingFor(settings, orgId);
  return p.order === DEFAULT_ORG_FUNDING.order && p.onOrgLimit === DEFAULT_ORG_FUNDING.onOrgLimit;
}

/**
 * The setting with `orgId`'s entry changed by `patch` (fields left out keep the
 * entry's value), and the entries of orgs `keep` rejects dropped: leaving an
 * org makes its entry inert, and it goes the next time the setting is written
 * (§10.4). `keep` undefined keeps every entry.
 */
export function withOrgFunding(
  settings: OrgFundingSettings,
  orgId: string,
  patch: Partial<OrgFundingPreference>,
  keep?: (orgId: string) => boolean,
): OrgFundingSettings {
  const out = pruned(settings, orgId, keep);
  const next: StoredPreference = { ...settings[orgId] };
  if (patch.order) next.order = patch.order;
  if (patch.onOrgLimit) next.on_org_limit = patch.onOrgLimit;
  out[orgId] = next;
  return out;
}

/** The setting with `orgId` back on the defaults (its entry removed). */
export function resetOrgFunding(
  settings: OrgFundingSettings,
  orgId: string,
  keep?: (orgId: string) => boolean,
): OrgFundingSettings {
  const out = pruned(settings, orgId, keep);
  delete out[orgId];
  return out;
}

function pruned(settings: OrgFundingSettings, orgId: string, keep?: (orgId: string) => boolean): OrgFundingSettings {
  const out: OrgFundingSettings = {};
  for (const [id, entry] of Object.entries(settings)) {
    if (id === orgId || !keep || keep(id)) out[id] = { ...entry };
  }
  return out;
}

/** The order, in the words of the Providers row ("In Acme, use: …"). */
export function orderLabel(order: FundingOrder, orgName: string): string {
  switch (order) {
    case "org_first":
      return `${orgName}'s accounts first`;
    case "personal_first":
      return "My accounts first";
    case "org_only":
      return `Only ${orgName}'s accounts`;
  }
}

/** The on-limit choice, in the words of the Providers row ("When Acme's accounts can't serve a chat: …"). */
export function onOrgLimitLabel(onOrgLimit: OnOrgLimit): string {
  switch (onOrgLimit) {
    case "ask":
      return "Ask me";
    case "use_personal":
      return "Use my accounts";
    case "wait":
      return "Wait";
  }
}
