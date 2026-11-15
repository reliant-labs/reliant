// Copyright (c) 2025 Reliant Labs

/**
 * What the public demo (lib/shell.ts, `demo`) records about its visitors.
 *
 * Nobody signs in on the demo host, so every visitor is an anonymous demo
 * visitor: the web runtime's attribution gives each browser a stable visitor
 * id (`visitor.id` on every event), adopting the landing page's `rid` when a
 * landing link brought them, so the landing → demo funnel joins and unique
 * visitors count the way the landing page counts them (distinct `visitor.id`).
 * The user id is never set.
 *
 * The level is the runtime config's DEMO_TELEMETRY (owner default, 2026-11-05:
 * `replay`): page and tour analytics plus session replay, which the runtime
 * records with all text and inputs masked, media blocked and no canvas. A
 * browser that sends Do Not Track or Global Privacy Control is never recorded,
 * whatever the config says: telemetry does not start at all. A phone gets
 * analytics without the replay (DEMO_REPLAY_ON_PHONES).
 *
 * Dependency-free beyond the generated config, like telemetryEndpoint.ts:
 * telemetry.ts and the demo banner both read it.
 */

import { loadConfig } from "./config_gen";

export type DemoTelemetry = "replay" | "analytics" | "off";

type PrivacySignals = {
  doNotTrack?: string | null;
  globalPrivacyControl?: boolean;
  msDoNotTrack?: string | null;
};

/** Whether the browser asked not to be tracked: DNT (`1`/`yes`) or GPC. */
export function browserOptedOut(
  nav: PrivacySignals | undefined = typeof navigator !== "undefined" ? (navigator as PrivacySignals) : undefined,
  win: { doNotTrack?: string | null } | undefined = typeof window !== "undefined" ? (window as { doNotTrack?: string | null }) : undefined,
): boolean {
  if (nav?.globalPrivacyControl === true) return true;
  const dnt = nav?.doNotTrack ?? win?.doNotTrack ?? nav?.msDoNotTrack ?? null;
  return dnt === "1" || dnt === "yes";
}

/** DEMO_TELEMETRY as configured, ignoring the browser. */
export function configuredDemoTelemetry(): DemoTelemetry {
  let value = "";
  try {
    value = loadConfig().DEMO_TELEMETRY;
  } catch {
    // An invalid config document is configGuard's to report.
  }
  return value === "analytics" || value === "off" ? value : "replay";
}

/**
 * Whether the demo records session replay on phones (owner, 2026-11-15: no).
 * On a phone the replay recorder cost every tap 15-45%, froze scrolling for
 * 300-400 ms every few seconds and kept the main thread busy seconds longer
 * after load (research/2026-11-10-demo-errors/mobile-perf-findings.md), so a
 * phone records analytics only; desktops still record replays. `true` records
 * phones again.
 */
export const DEMO_REPLAY_ON_PHONES = false;

/** The widest viewport (CSS px) counted as a phone. */
export const PHONE_MAX_WIDTH = 768;

type MediaQueries = { matchMedia?: (query: string) => { matches: boolean } };

/** A phone: a coarse (touch) primary pointer on a viewport no wider than PHONE_MAX_WIDTH. */
export function isPhone(win: MediaQueries | undefined = typeof window !== "undefined" ? window : undefined): boolean {
  const matches = (query: string) => win?.matchMedia?.(query)?.matches ?? false;
  return matches("(pointer: coarse)") && matches(`(max-width: ${PHONE_MAX_WIDTH}px)`);
}

let phoneAtLoad: boolean | null = null;
/** isPhone() as of the page load: a phone turned sideways mid-visit is still a phone. */
function isPhoneAtLoad(): boolean {
  return (phoneAtLoad ??= isPhone());
}

/** What the demo records for THIS browser: the config, unless it opted out; analytics in place of replay on a phone. */
export function demoTelemetry(optedOut: boolean = browserOptedOut(), phone: boolean = isPhoneAtLoad()): DemoTelemetry {
  if (optedOut) return "off";
  const level = configuredDemoTelemetry();
  return level === "replay" && phone && !DEMO_REPLAY_ON_PHONES ? "analytics" : level;
}

/** The demo banner's one-line notice for a level, and whether the browser opted out. */
export function demoTelemetryNotice(level: DemoTelemetry, optedOut: boolean): string | null {
  if (optedOut) return "Your browser asked not to be tracked, so this demo records nothing.";
  if (level === "replay") return "This demo records usage and session replays to improve the product.";
  if (level === "analytics") return "This demo counts page views and tour steps to improve the product.";
  return null;
}
