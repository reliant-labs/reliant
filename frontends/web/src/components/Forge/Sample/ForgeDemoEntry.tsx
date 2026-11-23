// Copyright (c) 2026 Reliant Labs

/**
 * `/forge/demo` in the app's tree (routes.tsx, both app hosts).
 *
 * Signed in, the demo is the sample app INSIDE the shell
 * (`/forge/p/sample_shop/…`, sampleApp.ts), the same console a person's own
 * app gets, so an old link (a bookmark, a `/forge/demo?view=traces&tour=…`
 * tour link) opens the page it names there. Signed out (the forge brand's
 * public demo, a landing page's `?journey=forge`), there is no shell to walk:
 * the `/forge/demo` panel stays.
 *
 * The public demo host mounts its own `/forge/demo` (demoShellRoutes.tsx) and
 * never this.
 */

import { Navigate, useSearch } from "@tanstack/react-router";

import { useAuthStore } from "@/store/authStore";

import { consolePath, isObsNavView, withSearch, type ConsolePage } from "../Console/paths";
import { ForgeDemoPage } from "../Demo/demoRoute";
import { SAMPLE_APP } from "./sampleApp";

/** The panel's keys that name nothing on the console: its sections and its embedded browsers. */
const PANEL_ONLY = new Set(["section", "browser"]);

/** A `/forge/demo` search as the sample app's console page that shows the same (its view, its open trace or issue, `tour`). */
export function sampleTargetForDemo(search: Record<string, unknown>): { to: string; search: Record<string, unknown> } {
  const { view, ...rest } = search;
  const page: ConsolePage = typeof view === "string" && isObsNavView(view) ? { kind: "obs-view", view } : { kind: "overview" };
  const kept = Object.fromEntries(Object.entries(rest).filter(([key]) => !PANEL_ONLY.has(key) && rest[key] !== undefined));
  return { to: consolePath(SAMPLE_APP, page), search: kept };
}

/** The same, as one href (tests, links). */
export function sampleHrefForDemo(search: Record<string, unknown>): string {
  const target = sampleTargetForDemo(search);
  return withSearch(target.to, target.search);
}

export function ForgeDemoEntry() {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const initialized = useAuthStore((state) => state.initialized);
  const signedIn = useAuthStore((state) => state.user != null);
  // Until the session is known, nothing: a signed-in person must not see the panel flash first.
  if (!initialized) return null;
  if (signedIn) {
    const target = sampleTargetForDemo(search);
    return <Navigate to={target.to} search={target.search as never} replace />;
  }
  return <ForgeDemoPage />;
}
