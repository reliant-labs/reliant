import type { ProviderAccount, UsageWindow } from "../../../api/provider-account-grpc";

/** At or above this fraction a window reads amber. */
export const USAGE_WARN_FRACTION = 0.8;
/** Usage older than this carries an "as of N min ago" caption. */
export const STALE_USAGE_MS = 10 * 60 * 1000;

export type UsageTone = "ok" | "warn" | "danger";

function parse(iso: string | undefined): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t;
}

/** "2h 13m", "3d 4h", "45m", "<1m". */
export function formatDuration(ms: number): string {
  const totalMin = Math.max(0, Math.floor(ms / 60_000));
  if (totalMin < 1) return "<1m";
  const days = Math.floor(totalMin / 1440);
  const hours = Math.floor((totalMin % 1440) / 60);
  const mins = totalMin % 60;
  if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  if (hours > 0) return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
  return `${mins}m`;
}

/** "resets in 2h 13m", or "" when the reset time is unknown or already past. */
export function formatResetsIn(resetsAt: string | undefined, now: Date): string {
  const t = parse(resetsAt);
  if (t === null) return "";
  const delta = t - now.getTime();
  if (delta <= 0) return "";
  return `resets in ${formatDuration(delta)}`;
}

/** "12 min ago", "3 h ago", "2 d ago"; "just now" under a minute. */
export function formatAgo(iso: string | undefined, now: Date): string {
  const t = parse(iso);
  if (t === null) return "";
  const delta = now.getTime() - t;
  if (delta < 60_000) return "just now";
  const min = Math.floor(delta / 60_000);
  if (min < 60) return `${min} min ago`;
  const hours = Math.floor(min / 60);
  if (hours < 24) return `${hours} h ago`;
  return `${Math.floor(hours / 24)} d ago`;
}

/** "3:40 PM", prefixed with the weekday when it is not today. */
export function formatClock(iso: string | undefined, now: Date): string {
  const t = parse(iso);
  if (t === null) return "";
  const d = new Date(t);
  const time = d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (d.toDateString() === now.toDateString()) return time;
  const day = d.toLocaleDateString([], { weekday: "short" });
  return `${day} ${time}`;
}

/** "Limited until 3:40 PM", or just "Limited" when the end is unknown. */
export function limitedLabel(account: ProviderAccount, now: Date): string {
  const clock = formatClock(account.limitedUntil, now);
  return clock ? `Limited until ${clock}` : "Limited";
}

export interface EffectiveWindow {
  /** 0..1, or null when the usage is unknown. */
  used: number | null;
  /** Reset time still ahead of now, or undefined when unknown or passed. */
  resetsAt: string | undefined;
  /** The window rolled over since it was observed, so it is a new one at 0. */
  rolledOver: boolean;
}

/**
 * A window as it applies at `now`. A window whose reset has passed is a new
 * window (used 0, reset unknown); one observed longer ago than its own length
 * is unknown. This mirrors the server's accountlimits.Effective so the bars
 * never keep showing a full window that already reset.
 */
export function effectiveWindow(
  w: UsageWindow,
  observedAt: string | undefined,
  now: Date,
): EffectiveWindow {
  const reset = parse(w.resetsAt);
  if (reset !== null && reset <= now.getTime()) {
    return { used: 0, resetsAt: undefined, rolledOver: true };
  }
  if (w.usedFraction === undefined) {
    return { used: null, resetsAt: w.resetsAt, rolledOver: false };
  }
  const observed = parse(observedAt);
  if (
    reset === null &&
    observed !== null &&
    w.lengthSeconds > 0 &&
    now.getTime() - observed > w.lengthSeconds * 1000
  ) {
    return { used: null, resetsAt: undefined, rolledOver: false };
  }
  return {
    used: Math.min(1, Math.max(0, w.usedFraction)),
    resetsAt: w.resetsAt,
    rolledOver: false,
  };
}

/** Red when the window is what benched the account, amber from 80 %. */
export function usageTone(
  used: number | null,
  window: UsageWindow,
  account: ProviderAccount,
): UsageTone {
  if (used === null) return "ok";
  if (account.status === "limited" && (window.name === account.limitedWindow || used >= 1)) {
    return "danger";
  }
  return used >= USAGE_WARN_FRACTION ? "warn" : "ok";
}

export function isUsageStale(account: ProviderAccount, now: Date): boolean {
  const t = parse(account.usageObservedAt);
  return t !== null && now.getTime() - t >= STALE_USAGE_MS;
}

/** Readable form of a normalized plan id when the server sent no display form. */
export function planLabel(account: Pick<ProviderAccount, "plan" | "planDisplay">): string {
  if (account.planDisplay) return account.planDisplay;
  if (!account.plan) return "";
  return account.plan
    .split(/[_-]/)
    .filter(Boolean)
    .map((p) => (/^\d+x$/i.test(p) ? `${p.slice(0, -1)}×` : p[0].toUpperCase() + p.slice(1)))
    .join(" ");
}

/** An account's accounts of one provider, in priority order. */
export function sortByPriority(accounts: ProviderAccount[]): ProviderAccount[] {
  return [...accounts].sort(
    (a, b) => a.priority - b.priority || a.createdAt.localeCompare(b.createdAt),
  );
}
