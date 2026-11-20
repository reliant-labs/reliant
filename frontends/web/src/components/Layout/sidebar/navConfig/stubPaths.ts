// Copyright (c) 2026 Reliant Labs

/**
 * STUBS for path builders two parallel U6c branches add (orchestrator
 * briefing, "Path builders"). Each returns what its builder will, from the
 * builders that exist today, so the nav links somewhere real now and the merge
 * swaps the import. Nothing here spells a path.
 *
 * TODO(U6c ACCOUNT): usageBillingPath, sandboxesPath → lib/paths.
 * TODO(U6c PAGES): analyticsPath, speedInsightsPath → consolePath(app, { kind: "analytics", tab }) /
 *   { kind: "speed-insights" } in Forge/Console/paths.ts.
 */

import { settingsPath, usagePath, withSearch, type PathContext } from "@/lib/paths";

import { consolePath } from "../../../Forge/Console/paths";

/** The org's Usage & billing page (ACCOUNT: `/usage`, org form via withOrg). */
export function usageBillingPath(ctx?: PathContext): string {
  return usagePath(ctx);
}

/** The Agent sandboxes page (ACCOUNT: the machines page, or its `/machines` alias). */
export function sandboxesPath(): string {
  return settingsPath("machines");
}

/** The built-in Product analytics dashboards' tabs (PAGES). */
export const ANALYTICS_TABS = ["overview", "users", "product", "acquisition"] as const;
export type AnalyticsTab = (typeof ANALYTICS_TABS)[number] | "events";

/** The Analytics page at one tab (PAGES' `{ kind: "analytics", tab }`); overview is the bare page. */
export function analyticsPath(app: string, tab?: AnalyticsTab): string {
  return withSearch(consolePath(app, { kind: "extension", id: "analytics", segment: "analytics" }), {
    tab: tab === "overview" ? undefined : tab,
  });
}

/** Speed insights: web vitals per page (PAGES' `{ kind: "speed-insights" }`). */
export function speedInsightsPath(app: string): string {
  return consolePath(app, { kind: "extension", id: "speed-insights", segment: "speed-insights" });
}
