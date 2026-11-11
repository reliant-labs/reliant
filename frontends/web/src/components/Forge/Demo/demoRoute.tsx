// Copyright (c) 2025 Reliant Labs

/**
 * `/forge/demo` as a route either shell mounts: Reliant under its
 * authenticated layout (routes.tsx), the forge shell (forge.reliantlabs.io)
 * on its root, public, with `/demo` redirecting to it (buildForgeShellTree).
 *
 * The page itself (DemoConsole) is its own lazy chunk, fixtures included, so
 * neither shell pays for it until it is opened.
 */

import { createRoute, redirect, type AnyRoute } from "@tanstack/react-router";
import { z } from "zod";

import { retainTourParam } from "@/components/Onboarding/tourParam";
import { lazyRoute } from "@/lib/lazyRoute";
import { obsSearchShape, forgeConsoleDataSearchSchema, forgeConsoleStorageSearchSchema } from "@/routeSchemas";

import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

export { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

/** The panel's search keys (obsSearch.ts), the tour's, and the demo's own section. Each key drops itself rather than failing the route. */
export const forgeDemoSearchSchema = z.object({
  ...obsSearchShape,
  ...forgeConsoleDataSearchSchema.omit({ env: true }).shape,
  ...forgeConsoleStorageSearchSchema.omit({ env: true }).shape,
  browser: z.enum(["data", "storage"]).optional().catch(undefined),
  tour: z.string().optional().catch(undefined),
  section: z.enum([DEMO_SECTION_BUILT]).optional().catch(undefined),
});

/** The demo page, loaded on first visit. */
export const ForgeDemoPage = lazyRoute(() => import("./DemoConsole"), "DemoConsole");

export function createForgeDemoRoute<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  return createRoute({
    getParentRoute,
    path: FORGE_DEMO_PATH,
    validateSearch: forgeDemoSearchSchema,
    // The forge shell's root has no retainTourParam of its own: the demo keeps `?tour=` itself.
    search: { middlewares: [retainTourParam as never] },
    component: ForgeDemoPage,
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
