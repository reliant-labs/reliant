// Copyright (c) 2025 Reliant Labs

/**
 * Shared harness for the automation component tests: a memory router (the
 * pages use Link/useNavigate) and a fresh QueryClient per render.
 */

import type { ReactNode } from "react";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

export function renderAtRoute(ui: ReactNode, path = "/automations") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: path.startsWith("/automations/") ? "/automations/$triggerId" : "/automations",
    component: () => <>{ui}</>,
  });
  // Link targets have to exist for the router to build their hrefs.
  const otherRoutes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/project/$projectId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/runs/$runId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflow" }),
    path.startsWith("/automations/")
      ? createRoute({ getParentRoute: () => rootRoute, path: "/automations" })
      : createRoute({ getParentRoute: () => rootRoute, path: "/automations/$triggerId" }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren([pageRoute, ...otherRoutes]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...result, router, queryClient };
}

/** A timestamp `offsetMs` from now, as the server renders it. */
export function isoFromNow(offsetMs: number): string {
  return new Date(Date.now() + offsetMs).toISOString();
}

export const HOUR = 60 * 60 * 1000;
