// Copyright (c) 2025 Reliant Labs

/**
 * The app's `/forge/demo` and `/demo` (routes.tsx, both brands): the forge
 * demo has one home, the demo bundle on the demo host (demoAppOrigin), so
 * both leave for its `/forge/demo`, query kept. A `?tour=` link (the forge
 * journey's steps, the landing page's handoff) carries the journey there,
 * where the demo shell opens the console page the step names.
 *
 * The desktop app opens the demo in the browser and stays on its home.
 */

import { createRoute, redirect, type AnyRoute } from "@tanstack/react-router";

import { isElectron } from "@/lib/constants";
import { openExternalLink } from "@/lib/open-link";
import { demoAppOrigin } from "@/lib/shell";

import { FORGE_DEMO_PATH } from "./demoPath";

/** The demo bundle's `/forge/demo` with this query (`?…`, or empty). */
export function forgeDemoHref(searchStr: string): string {
  return `${demoAppOrigin()}${FORGE_DEMO_PATH}${searchStr}`;
}

/** Leaves for the demo bundle; always throws the router's redirect. */
export function leaveForDemo({ location }: { location: { searchStr: string } }): never {
  const href = forgeDemoHref(location.searchStr);
  if (isElectron()) {
    void openExternalLink(href);
    throw redirect({ href: "/", replace: true });
  }
  // An absolute href is a document navigation (router-core's reloadDocument).
  throw redirect({ href, replace: true });
}

export function createForgeDemoHandoffRoutes<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  return [
    createRoute({ getParentRoute, path: FORGE_DEMO_PATH, beforeLoad: leaveForDemo }),
    createRoute({ getParentRoute, path: "/demo", beforeLoad: leaveForDemo }),
  ] as const;
}
