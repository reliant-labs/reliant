// Copyright (c) 2026 Reliant Labs

/**
 * `/forge/demo` and `/demo` in the app's tree (routes.tsx, both brands).
 *
 * Signed in, the demo is the sample app INSIDE the shell
 * (`/forge/p/sample_shop/…`, sampleApp.ts), the same console a person's own
 * app gets, so an old link (a bookmark, a `/forge/demo?view=traces&tour=…`
 * tour link) opens the page it names there, the tour with it, in the desktop
 * app too. Signed out (the forge brand's public demo, a landing page's
 * `?journey=forge`), there is no shell to walk: the link leaves for the demo
 * host, query kept (Forge/Demo/demoHandoff.ts).
 *
 * The demo host mounts its own `/forge/demo` (demoShellRoutes.tsx) and never
 * this.
 */

import { redirect } from "@tanstack/react-router";

import { useAuthStore } from "@/store/authStore";

import { consolePath, isObsNavView, withSearch, type ConsolePage } from "../Console/paths";
import { leaveForDemo } from "../Demo/demoHandoff";
import { SAMPLE_APP } from "./sampleApp";

/** The panel's keys that name nothing on the console: its sections and its embedded browsers. */
const PANEL_ONLY = new Set(["section", "browser"]);

/** A `/forge/demo` search as the sample app's console page that shows the same (its view, its open trace or issue, `tour`). */
export function sampleTargetForDemo(search: Record<string, unknown>): { to: string; search: Record<string, unknown> } {
  const { view, ...rest } = search;
  const page: ConsolePage = typeof view === "string" && isObsNavView(view) ? { kind: "obs-view", view } : { kind: "overview" };
  const kept = Object.fromEntries(Object.entries(rest).filter(([key, value]) => !PANEL_ONLY.has(key) && value !== undefined));
  return { to: consolePath(SAMPLE_APP, page), search: kept };
}

/** The same, as one href (tests, links). */
export function sampleHrefForDemo(search: Record<string, unknown>): string {
  const target = sampleTargetForDemo(search);
  return withSearch(target.to, target.search);
}

/**
 * The routes' `beforeLoad`: once the session is known (never a guess, so a
 * signed-in person is never sent off to the demo host first), the sample app
 * in the shell, or the demo host. Always throws the router's redirect.
 */
export async function enterForgeDemo({ location }: { location: { search: unknown; searchStr: string } }): Promise<never> {
  if (!useAuthStore.getState().initialized) await useAuthStore.getState().initialize();
  if (useAuthStore.getState().user) {
    const target = sampleTargetForDemo((location.search ?? {}) as Record<string, unknown>);
    throw redirect({ to: target.to, search: target.search as never, replace: true });
  }
  return leaveForDemo({ location });
}
