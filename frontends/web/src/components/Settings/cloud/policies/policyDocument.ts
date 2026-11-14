// Copyright (c) 2025 Reliant Labs

/**
 * The policy document as the Policies page edits it: section keys and their
 * words, the draft operations (clone, compare, rebase onto a newer version),
 * money in nanos, and the display of the history's field values. Pure: no
 * React, no requests, so every rule here is tested without rendering.
 *
 * The document is the OrgPolicy proto (control-plane
 * proto/controlplane/v1/org_policy.proto). Every section is optional; an
 * absent one is no rule. Lists are kept sorted, as the server stores them, so
 * ticking a box off and on again is no change.
 */

import { clone, create, equals } from "@bufbuild/protobuf";

import {
  AutomationApprovalFloor,
  ChatModeFloor,
  OrgPolicyMode,
  OrgPolicySchema,
  ProviderSource,
  SpendPeriod,
  type OrgPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";

/** The document's sections, in the order the page shows them. */
export const SECTION_KEYS = [
  "models",
  "providers",
  "automations",
  "spend",
  "integrations",
  "mcp",
  "approvals",
  "machines",
] as const;
export type SectionKey = (typeof SECTION_KEYS)[number];

export const SECTION_TITLES: Record<SectionKey, string> = {
  models: "Models",
  providers: "AI providers",
  automations: "Automations",
  spend: "Spend limits",
  integrations: "Integrations",
  mcp: "Tools (MCP)",
  approvals: "Approvals",
  machines: "Machine credentials",
};

/**
 * Where each rule is applied (§7 of the design): models and spend at the
 * Reliant gateway, which binds every app version; the rest on Reliant's
 * servers, rolling out section by section, each in audit first.
 */
export const SECTION_ENFORCED_BY: Record<SectionKey, string> = {
  models: "Applied by the Reliant gateway, for every version of the app.",
  providers: "Applied by Reliant's servers when a chat picks an account.",
  automations: "Applied by Reliant's servers when an automation picks an account.",
  spend: "Applied by the Reliant gateway, for every version of the app.",
  integrations: "Applied by Reliant's servers each time a run uses a connection.",
  mcp: "Applied by Reliant's servers to what agents can see and call.",
  approvals: "Applied by Reliant's servers when a run starts.",
  machines: "Applied by Reliant's servers to every command a run sends to a machine.",
};

export function emptyPolicy(): OrgPolicy {
  return create(OrgPolicySchema);
}

/** A deep copy the page may edit: never the cached server answer itself. */
export function copyPolicy(p: OrgPolicy | undefined): OrgPolicy {
  return p ? clone(OrgPolicySchema, p) : emptyPolicy();
}

/** `p` with only section `key` (a whole document, so `equals` can compare it). */
function only(p: OrgPolicy | undefined, key: SectionKey): OrgPolicy {
  const out = emptyPolicy();
  const section = p?.[key];
  if (section) (out as unknown as Record<SectionKey, unknown>)[key] = section;
  return out;
}

export function sectionEqual(a: OrgPolicy | undefined, b: OrgPolicy | undefined, key: SectionKey): boolean {
  return equals(OrgPolicySchema, only(a, key), only(b, key));
}

/** The sections `draft` changes relative to `base`, in page order. */
export function changedSections(base: OrgPolicy | undefined, draft: OrgPolicy | undefined): SectionKey[] {
  return SECTION_KEYS.filter((k) => !sectionEqual(base, draft, k));
}

/** The sections that are set: the rules the org has. */
export function activeSections(p: OrgPolicy | undefined): SectionKey[] {
  return SECTION_KEYS.filter((k) => p?.[k] !== undefined);
}

/**
 * Someone saved `latest` while the editor was changing `base` into `draft`.
 * The editor's changed sections are reapplied onto `latest`; every other
 * section is theirs. `overlapping` are the sections both changed, where the
 * editor's version replaces theirs: the page says so before the save.
 */
export function rebasePolicy(
  base: OrgPolicy | undefined,
  draft: OrgPolicy,
  latest: OrgPolicy | undefined,
): { policy: OrgPolicy; overlapping: SectionKey[] } {
  const out = copyPolicy(latest);
  const mine = changedSections(base, draft);
  const overlapping = mine.filter((k) => !sectionEqual(base, latest, k));
  const target = out as unknown as Record<SectionKey, unknown>;
  const source = copyPolicy(draft) as unknown as Record<SectionKey, unknown>;
  for (const k of mine) target[k] = source[k];
  return { policy: out, overlapping };
}

/** Insert or remove `value` in a sorted, duplicate-free list. */
export function toggled<T extends string | number>(list: readonly T[], value: T, on: boolean): T[] {
  const set = new Set(list);
  if (on) set.add(value);
  else set.delete(value);
  return [...set].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

// ── Modes ───────────────────────────────────────────────────────────────────

/** UNSPECIFIED is read as AUDIT, the mode that refuses nothing (the proto's rule). */
export function isEnforced(mode: OrgPolicyMode | undefined): boolean {
  return mode === OrgPolicyMode.ENFORCE;
}

// ── Providers and sources ───────────────────────────────────────────────────

/**
 * The driver ids a providers section may name (control-plane
 * internal/orgpolicy `drivers`, from reliant's models.ProviderPriority), and
 * the kinds of account each can be.
 */
export const PROVIDER_DRIVERS: ReadonlyArray<{ id: string; label: string; sources: readonly ProviderSource[] }> = [
  { id: "reliant", label: "Reliant", sources: [ProviderSource.RELIANT] },
  { id: "claude", label: "Claude subscription", sources: [ProviderSource.ORG_SEATS, ProviderSource.PERSONAL] },
  { id: "codex", label: "ChatGPT (Codex) subscription", sources: [ProviderSource.ORG_SEATS, ProviderSource.PERSONAL] },
  { id: "copilot", label: "GitHub Copilot", sources: [ProviderSource.ORG_SEATS, ProviderSource.PERSONAL] },
  { id: "antigravity", label: "Antigravity", sources: [ProviderSource.ORG_SEATS, ProviderSource.PERSONAL] },
  { id: "anthropic", label: "Anthropic API", sources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.PERSONAL] },
  { id: "openai", label: "OpenAI API", sources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.PERSONAL] },
  { id: "openrouter", label: "OpenRouter", sources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.PERSONAL] },
  { id: "gemini", label: "Gemini API", sources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.PERSONAL] },
  { id: "vertexai", label: "Vertex AI", sources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.PERSONAL] },
  { id: "local", label: "Local models", sources: [ProviderSource.LOCAL] },
];

export function driverLabel(id: string): string {
  return PROVIDER_DRIVERS.find((d) => d.id === id)?.label ?? id;
}

export const PROVIDER_SOURCES: readonly ProviderSource[] = [
  ProviderSource.ORG_ACCOUNTS,
  ProviderSource.ORG_SEATS,
  ProviderSource.RELIANT,
  ProviderSource.PERSONAL,
  ProviderSource.LOCAL,
];

/** A kind of account, by who pays for it, in the org's own words. */
export function sourceLabel(source: ProviderSource, orgName: string): string {
  switch (source) {
    case ProviderSource.ORG_ACCOUNTS:
      return `${orgName}'s accounts`;
    case ProviderSource.ORG_SEATS:
      return `Seats ${orgName} pays for`;
    case ProviderSource.RELIANT:
      return "Reliant credits";
    case ProviderSource.PERSONAL:
      return "Members' own accounts";
    case ProviderSource.LOCAL:
      return "Local models";
    default:
      return "Unknown source";
  }
}

// ── Money ───────────────────────────────────────────────────────────────────

const NANOS_PER_USD = 1_000_000_000n;

/** "$50", "$12.50". */
export function formatUsdNanos(nanos: bigint): string {
  const cents = Number((nanos * 100n) / NANOS_PER_USD);
  const dollars = cents / 100;
  return dollars.toLocaleString("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: cents % 100 === 0 ? 0 : 2,
    maximumFractionDigits: 2,
  });
}

/** A typed dollar amount ("50", "12.5", "$1,000") as nanos; null when it is not one. */
export function parseUsd(text: string): bigint | null {
  const cleaned = text.replace(/[$,\s]/g, "");
  if (!/^\d+(\.\d{0,2})?$/.test(cleaned)) return null;
  const [whole, frac = ""] = cleaned.split(".");
  const cents = BigInt(whole) * 100n + BigInt((frac + "00").slice(0, 2));
  return cents * (NANOS_PER_USD / 100n);
}

/** The amount as an editor shows it: "50", "12.50". */
export function usdInputValue(nanos: bigint): string {
  const cents = (nanos * 100n) / NANOS_PER_USD;
  const whole = cents / 100n;
  const frac = cents % 100n;
  return frac === 0n ? `${whole}` : `${whole}.${frac.toString().padStart(2, "0")}`;
}

export const PERIOD_LABELS: Record<SpendPeriod, string> = {
  [SpendPeriod.UNSPECIFIED]: "period",
  [SpendPeriod.DAY]: "day",
  [SpendPeriod.MONTH]: "month",
};

// ── Approvals ───────────────────────────────────────────────────────────────

export const AUTOMATION_FLOOR_LABELS: Record<AutomationApprovalFloor, string> = {
  [AutomationApprovalFloor.UNSPECIFIED]: "No floor: each person's own setting",
  [AutomationApprovalFloor.ON_CAPABILITY_CHANGE]: "When a change widens what an automation can do",
  [AutomationApprovalFloor.ALWAYS]: "Every change an agent makes to an automation",
};

export const CHAT_MODE_FLOOR_LABELS: Record<ChatModeFloor, string> = {
  [ChatModeFloor.UNSPECIFIED]: "Any mode",
  [ChatModeFloor.ANY]: "Any mode",
  [ChatModeFloor.NO_AUTO]: "No auto mode: every tool call asks first",
};

// ── History values ──────────────────────────────────────────────────────────

const FIELD_LABELS: Record<string, string> = {
  mode: "mode",
  allow_all: "allow everything",
  allowed: "allowed",
  allowed_sources: "allowed sources",
  allowed_servers: "allowed servers",
  org_cap: "organization limit",
  member_default_cap: "limit per member",
  member_overrides: "limit for",
  applies_to: "counts",
  automation_floor: "automation approvals",
  chat_mode_floor: "chat modes",
  force_credentials_none: "run commands without credentials",
};

/** "Spend limits › limit per member", "Spend limits › limit for Ada". */
export function describeHistoryField(section: string, field: string, nameOf: (userId: string) => string): string {
  const title = SECTION_TITLES[section as SectionKey] ?? section;
  const [base, key] = field.split(/\.(.+)/);
  const label = FIELD_LABELS[base] ?? base.replace(/_/g, " ");
  return key ? `${title} › ${label} ${nameOf(key)}` : `${title} › ${label}`;
}

const ENUM_WORDS: Record<string, string> = {
  ORG_POLICY_MODE_ENFORCE: "enforce",
  ORG_POLICY_MODE_AUDIT: "audit",
  ORG_POLICY_MODE_UNSPECIFIED: "unset",
  PROVIDER_SOURCE_ORG_ACCOUNTS: "the org's accounts",
  PROVIDER_SOURCE_ORG_SEATS: "seats the org pays for",
  PROVIDER_SOURCE_RELIANT: "Reliant credits",
  PROVIDER_SOURCE_PERSONAL: "members' own accounts",
  PROVIDER_SOURCE_LOCAL: "local models",
  SPEND_APPLIES_TO_MEMBERS: "members only",
  SPEND_APPLIES_TO_MEMBERS_AND_APPS: "members and hosted apps",
  SPEND_APPLIES_TO_UNSPECIFIED: "members only",
  AUTOMATION_APPROVAL_FLOOR_UNSPECIFIED: "no floor",
  AUTOMATION_APPROVAL_FLOOR_ALWAYS: "always",
  AUTOMATION_APPROVAL_FLOOR_ON_CAPABILITY_CHANGE: "on capability change",
  CHAT_MODE_FLOOR_UNSPECIFIED: "any mode",
  CHAT_MODE_FLOOR_ANY: "any mode",
  CHAT_MODE_FLOOR_NO_AUTO: "no auto mode",
};

const PERIOD_WORDS: Record<string, string> = { SPEND_PERIOD_DAY: "day", SPEND_PERIOD_MONTH: "month" };

function capWords(cap: Record<string, unknown>): string | null {
  if (!("usd_nanos" in cap) && !("period" in cap)) return null;
  const nanos = BigInt(String(cap.usd_nanos ?? "0"));
  const period = PERIOD_WORDS[String(cap.period)] ?? "period";
  return `${formatUsdNanos(nanos)} a ${period}`;
}

function valueWords(v: unknown): string {
  if (v === null || v === undefined) return "none";
  if (typeof v === "boolean") return v ? "on" : "off";
  if (typeof v === "string") return ENUM_WORDS[v] ?? (driverLabelOrSelf(v));
  if (typeof v === "number") return String(v);
  if (Array.isArray(v)) return v.length === 0 ? "none" : v.map(valueWords).join(", ");
  const obj = v as Record<string, unknown>;
  const cap = capWords(obj);
  if (cap) return cap;
  if ("limit" in obj && obj.limit && typeof obj.limit === "object") return capWords(obj.limit as Record<string, unknown>) ?? "a limit";
  if (obj.unlimited === true) return "no limit";
  return JSON.stringify(v);
}

function driverLabelOrSelf(v: string): string {
  return PROVIDER_DRIVERS.some((d) => d.id === v) ? driverLabel(v) : v;
}

/**
 * A history value (display JSON from the server) in words: "" and "null"
 * are "not set", a cap is "$50 a month", an enum is its plain name. Anything
 * that does not parse is shown as it came.
 */
export function formatHistoryValue(raw: string): string {
  if (raw === "" || raw === "null") return "not set";
  try {
    return valueWords(JSON.parse(raw));
  } catch {
    return raw;
  }
}
