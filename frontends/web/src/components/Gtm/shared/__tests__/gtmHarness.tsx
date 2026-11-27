// Copyright (c) 2026 Reliant Labs

/**
 * Renders a GTM view the way GtmShell hands it its org, on a memory router
 * at `path`, with GtmService answered by `handlers` (mockTransport keys,
 * "GtmService.ListIdeas" …). For the GTM page tests: they render the view
 * under the gate, not the gate itself (GtmShell.test.tsx covers that).
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";

import { setTransport } from "@/lib/connect";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";

import { GtmOrgProvider } from "../GtmShell";

export const TEST_ORG = "org-1";

export function renderGtm(ui: ReactElement, handlers: MockHandlers, { path = "/", routePath = path.split("?")[0] }: { path?: string; routePath?: string } = {}) {
  setTransport(mockTransport(handlers));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 }, mutations: { retry: false } } });
  const root = createRootRoute();
  const page = createRoute({ getParentRoute: () => root, path: routePath, component: () => <GtmOrgProvider value={TEST_ORG}>{ui}</GtmOrgProvider> });
  const router = createRouter({ routeTree: root.addChildren([page]), history: createMemoryHistory({ initialEntries: [path] }) });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...result, queryClient, router };
}
