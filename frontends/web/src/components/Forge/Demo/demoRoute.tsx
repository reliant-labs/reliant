// Copyright (c) 2025 Reliant Labs

/**
 * `/forge/demo` as a route the app's tree mounts on its root, public, with
 * `/demo` redirecting to it (routes.tsx, on both hosts).
 *
 * The page itself (DemoConsole) is its own lazy chunk, fixtures included, so
 * nobody pays for it until it is opened.
 */

import { createRoute, redirect, type AnyRoute, type RouteComponent } from "@tanstack/react-router";
import { z } from "zod";

import { retainTourParam } from "@/components/Onboarding/tourParam";
import { lazyRoute } from "@/lib/lazyRoute";
import { obsSearchShape, forgeConsoleDataSearchSchema, forgeConsoleStorageSearchSchema } from "@/routeSchemas";

import { DEMO_SECTIONS, FORGE_DEMO_PATH } from "./demoPath";

export { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

/** The panel's search keys (obsSearch.ts), the tour's, and the demo's own section. Each key drops itself rather than failing the route. */
export const forgeDemoSearchSchema = z.object({
  ...obsSearchShape,
  ...forgeConsoleDataSearchSchema.omit({ env: true }).shape,
  ...forgeConsoleStorageSearchSchema.omit({ env: true }).shape,
  browser: z.enum(["data", "storage"]).optional().catch(undefined),
  tour: z.string().optional().catch(undefined),
  section: z.enum(DEMO_SECTIONS).optional().catch(undefined),
});

/** The demo page, loaded on first visit. */
export const ForgeDemoPage = lazyRoute(() => import("./DemoConsole"), "DemoConsole");

/**
 * `/forge/demo` on `getParentRoute`. `component` replaces the panel: the app's
 * tree sends a signed-in person to the sample app in the shell instead
 * (Forge/Sample/ForgeDemoEntry.tsx); the demo host keeps the panel.
 */
export function createForgeDemoRoute<TParent extends AnyRoute>(
  getParentRoute: () => TParent,
  component: RouteComponent = ForgeDemoPage,
) {
  return createRoute({
    getParentRoute,
    path: FORGE_DEMO_PATH,
    validateSearch: forgeDemoSearchSchema,
    // The forge shell's root has no retainTourParam of its own: the demo keeps `?tour=` itself.
    search: { middlewares: [retainTourParam as never] },
    component,
  });
}

/** `/demo` → `/forge/demo`, keeping the query (a `?tour=` from a link). For the forge shell. */
export function createForgeDemoRedirectRoute<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  return createRoute({
    getParentRoute,
    path: "/demo",
    beforeLoad: ({ location }) => {
      throw redirect({ href: `${FORGE_DEMO_PATH}${location.searchStr}`, replace: true });
    },
  });
}
