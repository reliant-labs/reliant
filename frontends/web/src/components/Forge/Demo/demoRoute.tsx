// Copyright (c) 2025 Reliant Labs

/**
 * `/forge/demo` and `/demo`, in both route trees.
 *
 * The demo has one home, the demo bundle (demo.reliantlabs.io, its shell
 * demoShellRoutes.tsx): there `/forge/demo` is the demo's own pages
 * (createForgeDemoRoute) and `/demo` points at it. The app's tree (routes.tsx,
 * both brands) hands both off to it (demoHandoff.ts), query kept, so a
 * `?tour=` link carries the forge journey there.
 *
 * The page itself (DemoConsole) is its own lazy chunk, fixtures included, so
 * nobody pays for it until it is opened.
 */

import { createRoute, redirect, type AnyRoute } from "@tanstack/react-router";
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

export function createForgeDemoRoute<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  return createRoute({
    getParentRoute,
    path: FORGE_DEMO_PATH,
    validateSearch: forgeDemoSearchSchema,
    // The demo shell's root keeps `?tour=` too; the route keeps it wherever it is mounted.
    search: { middlewares: [retainTourParam as never] },
    component: ForgeDemoPage,
  });
}

/** `/demo` → `/forge/demo`, keeping the query (a `?tour=` from a link). */
export function createForgeDemoRedirectRoute<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  return createRoute({
    getParentRoute,
    path: "/demo",
    beforeLoad: ({ location }) => {
      throw redirect({ href: `${FORGE_DEMO_PATH}${location.searchStr}`, replace: true });
    },
  });
}
