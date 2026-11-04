// Copyright (c) 2025 Reliant Labs

/**
 * What the composer's account chip says about the account a chat is using now.
 * Pure: no React, no RPCs.
 *
 * The chip is the exception, not a gauge: it is absent until the account is at
 * or above 80% in a window, or limited. Everything else about the account
 * lives in Settings → Providers.
 */

import type { ProviderAccount, UsageWindow } from "@/types/providerAccount";
import {
  USAGE_WARN_FRACTION,
  effectiveWindow,
  limitedLabel,
  sortByPriority,
} from "@/components/Settings/providers/accountFormat";

export type AccountChipState =
  /** The account is still serving, but a window is nearly spent. */
  | { kind: "usage"; percent: number; windowLabel: string }
  /** Limited, and a sibling account of the provider can take over. */
  | { kind: "limited_next"; next: ProviderAccount }
  /** Limited, and no sibling account can take over. */
  | { kind: "limited_until" };

/** A window scoped to one model ("7d_opus", "7d.opus") only applies to that model. */
const MODEL_SCOPED = /[._]/;

/** "5h" / "7d" read as they are; anything else uses the provider's label. */
export function windowShortName(w: Pick<UsageWindow, "name" | "label">): string {
  return /^\d+[hdm]$/.test(w.name) ? w.name : w.label || w.name;
}

/** Limited right now: a bench whose end has passed no longer counts. */
export function isLimitedNow(account: ProviderAccount, now: Date): boolean {
  if (account.status !== "limited") return false;
  const until = account.limitedUntil ? Date.parse(account.limitedUntil) : NaN;
  return Number.isNaN(until) || until > now.getTime();
}

function canServe(account: ProviderAccount, now: Date): boolean {
  if (account.status === "paused" || account.status === "needs_sign_in") return false;
  return !isLimitedNow(account, now);
}

/** The account the router moves to next: the best-ordered sibling that can serve. */
export function nextAccountOf(
  active: ProviderAccount,
  accounts: ProviderAccount[],
  now: Date,
): ProviderAccount | undefined {
  return sortByPriority(
    accounts.filter((a) => a.provider === active.provider && a.id !== active.id),
  ).find((a) => canServe(a, now));
}

/** The fullest unscoped window that is still counting at `now`. */
function fullestWindow(
  account: ProviderAccount,
  now: Date,
): { used: number; window: UsageWindow } | undefined {
  let best: { used: number; window: UsageWindow } | undefined;
  for (const window of account.windows) {
    if (MODEL_SCOPED.test(window.name)) continue;
    const { used } = effectiveWindow(window, account.usageObservedAt, now);
    if (used !== null && (!best || used > best.used)) best = { used, window };
  }
  return best;
}

/** The chip's state, or null when the chip stays hidden. */
export function accountChipState(
  account: ProviderAccount,
  accounts: ProviderAccount[],
  now: Date,
): AccountChipState | null {
  if (isLimitedNow(account, now)) {
    const next = nextAccountOf(account, accounts, now);
    return next ? { kind: "limited_next", next } : { kind: "limited_until" };
  }
  const fullest = fullestWindow(account, now);
  if (fullest && fullest.used >= USAGE_WARN_FRACTION) {
    return {
      kind: "usage",
      percent: Math.round(fullest.used * 100),
      windowLabel: windowShortName(fullest.window),
    };
  }
  return null;
}

/** "alice@" for "alice@acme.com"; the whole label when that would be ambiguous or is no email. */
export function shortAccountLabel(account: ProviderAccount, siblings: ProviderAccount[]): string {
  const at = account.label.indexOf("@");
  if (at <= 0) return account.label;
  const short = account.label.slice(0, at + 1);
  const clash = siblings.some(
    (a) => a.id !== account.id && a.provider === account.provider && a.label.startsWith(short),
  );
  return clash ? account.label : short;
}

export interface ChipText {
  /** The visible words. */
  label: string;
  /** What assistive tech and the title attribute read. */
  full: string;
}

/**
 * The chip's words. `compact` (mobile) drops the account name: the popover
 * names it, and a phone's composer row has no room for it.
 */
export function accountChipText(
  state: AccountChipState,
  account: ProviderAccount,
  accounts: ProviderAccount[],
  now: Date,
  compact: boolean,
): ChipText {
  switch (state.kind) {
    case "usage": {
      const base = `${state.percent}% of ${state.windowLabel}`;
      const who = shortAccountLabel(account, accounts);
      return {
        label: compact ? base : `${base} · ${who}`,
        full: `${account.label} has used ${base}`,
      };
    }
    case "limited_next": {
      const who = shortAccountLabel(state.next, accounts);
      return {
        label: compact ? "Limited · next account" : `Limited · next account ${who}`,
        full: `${account.label} is limited; the next request uses ${state.next.label}`,
      };
    }
    case "limited_until": {
      const label = limitedLabel(account, now);
      return { label, full: `${account.label}: ${label}` };
    }
  }
}
