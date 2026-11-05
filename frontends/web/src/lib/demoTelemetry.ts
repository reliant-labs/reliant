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
 * whatever the config says: telemetry does not start at all.
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

/** What the demo records for THIS browser: the config, unless it opted out. */
export function demoTelemetry(optedOut: boolean = browserOptedOut()): DemoTelemetry {
  return optedOut ? "off" : configuredDemoTelemetry();
}

/** The demo banner's one-line notice for a level, and whether the browser opted out. */
export function demoTelemetryNotice(level: DemoTelemetry, optedOut: boolean): string | null {
  if (optedOut) return "Your browser asked not to be tracked, so this demo records nothing.";
  if (level === "replay") return "This demo records usage and session replays to improve the product.";
  if (level === "analytics") return "This demo counts page views and tour steps to improve the product.";
  return null;
}
